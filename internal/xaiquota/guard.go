package xaiquota

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Config controls runtime behaviour of the guard.
type Config struct {
	Enabled         bool
	TickSeconds     float64
	ManagementURL   string
	ManagementKey   string
	StatePath       string
	MaxResetSeconds float64
	// MinResetSeconds floors recovered wait when parsed reset is too short (0 = no floor).
	MinResetSeconds float64
	// IncludeUnobservedQuotaEst adds unobserved xAI accounts * DefaultFreeLimit into quota_total_est.
	IncludeUnobservedQuotaEst bool
	// CPAMP integration (optional backfill / deep link).
	CPAMPURL      string
	CPAMPAdminKey string
	// WebhookURL receives cooldown/delete JSON posts (optional).
	WebhookURL string

	// Patrol (proactive credential sweep).
	PatrolEnabled   bool
	PatrolInterval  float64
	PatrolTimeout   float64
	PatrolBatchSize int
	PatrolAuthDir    string
	PatrolProxyURL  string
}

// Defaults returns safe defaults. enabled=false until configured.
func Defaults() Config {
	return Config{
		Enabled:                   false,
		TickSeconds:               15,
		StatePath:                 "data/cpa-xai-quota-guard-state.json",
		MaxResetSeconds:           86400,
		MinResetSeconds:           0,
		IncludeUnobservedQuotaEst: true,
			PatrolEnabled:    false,
			PatrolInterval:   3600,
			PatrolTimeout:    15,
			PatrolBatchSize:  0,
			PatrolAuthDir:    "",
	}
}

// AuthFileLookup returns current auth file metadata from management API.
type AuthFileLookup interface {
	List() ([]AuthFile, error)
	SetDisabled(authIndex string, disabled bool) (prevDisabled bool, err error)
	Delete(authIndex string) error
}

// AuthFile is the management API subset we need.
type AuthFile struct {
	AuthIndex string
	Name      string
	Provider  string
	Account   string
	Disabled  bool
	Success   int64
	Failed    int64
}

// Logger writes plugin logs.
type Logger interface {
	Log(level, message string)
}

// UsageEvent is the plugin-side view of a usage failure.
type UsageEvent struct {
	AuthIndex       string
	Provider        string
	AuthType        string
	Account         string
	Failed          bool
	StatusCode      int
	Body            string
	ResponseHeaders map[string][]string
	EventHash       string
	// Token accounting (from usage detail when available).
	InputTokens     int64
	OutputTokens    int64
	ReasoningTokens int64
	TotalTokens     int64
}

// Guard owns the disable/recover state machine.
type Guard struct {
	mu     sync.Mutex
	cfg    Config
	store  *Store
	auth   AuthFileLookup
	logger Logger

	stopCh chan struct{}
	wg     sync.WaitGroup
	patrol patrolState
}

// NewGuard constructs a guard with durable state.
func NewGuard(cfg Config, auth AuthFileLookup, logger Logger) (*Guard, error) {
	store, err := NewStore(cfg.StatePath)
	if err != nil {
		return nil, err
	}
	g := &Guard{
		cfg:    cfg,
		store:  store,
		auth:   auth,
		logger: logger,
	}
	return g, nil
}

func (g *Guard) ApplyConfig(cfg Config) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if cfg.TickSeconds <= 0 {
		cfg.TickSeconds = 15
	}
	if cfg.MaxResetSeconds <= 0 {
		cfg.MaxResetSeconds = 86400
	}
	if cfg.StatePath == "" {
		cfg.StatePath = "data/cpa-xai-quota-guard-state.json"
	}
	// Reload store if path changed.
	if g.store == nil || g.store.Path() != cfg.StatePath {
		store, err := NewStore(cfg.StatePath)
		if err != nil {
			g.logf("error", "reload state failed: %v", err)
		} else {
			g.store = store
		}
	}
	g.cfg = cfg
}

func (g *Guard) Config() Config {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.cfg
}

func (g *Guard) Snapshot() map[string]AccountRecord {
	g.mu.Lock()
	store := g.store
	g.mu.Unlock()
	if store == nil {
		return map[string]AccountRecord{}
	}
	return store.Snapshot()
}

// StartTicker starts background recovery scans.
func (g *Guard) StartTicker() {
	g.mu.Lock()
	if g.stopCh != nil {
		g.mu.Unlock()
		return
	}
	g.stopCh = make(chan struct{})
	g.mu.Unlock()
	g.wg.Add(1)
	go g.tickerLoop()
}

// StopTicker stops background recovery.
func (g *Guard) StopTicker() {
	g.mu.Lock()
	if g.stopCh == nil {
		g.mu.Unlock()
		return
	}
	close(g.stopCh)
	g.stopCh = nil
	g.mu.Unlock()
	g.wg.Wait()
}

func (g *Guard) tickerLoop() {
	defer g.wg.Done()
	var patrolNext time.Time
	for {
		cfg := g.Config()
		interval := time.Duration(cfg.TickSeconds * float64(time.Second))
		if interval <= 0 {
			interval = 15 * time.Second
		}
		timer := time.NewTimer(interval)
		g.Tick()

		// Patrol scheduling
		if cfg.PatrolEnabled && cfg.PatrolAuthDir != "" {
			patrolInterval := time.Duration(cfg.PatrolInterval) * time.Second
			if patrolInterval <= 0 { patrolInterval = 3600 * time.Second }
			now := time.Now()
			if patrolNext.IsZero() || now.After(patrolNext) {
				patrolNext = now.Add(patrolInterval)
				go g.PatrolSweep()
			}
		}
		g.mu.Lock()
		stop := g.stopCh
		g.mu.Unlock()
		select {
		case <-stop:
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// HandleUsage processes one usage event.
func (g *Guard) HandleUsage(ev UsageEvent) {
	cfg := g.Config()
	if !cfg.Enabled {
		return
	}
	if !IsXAIProvider(ev.Provider, ev.AuthType) {
		return
	}

	// Accumulate metrics for every xAI request (success + failure).
	// CPAMP gets the same tokens from CPA usage queue; do not skip successes.
	g.recordUsageMetrics(ev)

	if !ev.Failed {
		return
	}
	authIndex := trim(ev.AuthIndex)
	if authIndex == "" {
		return
	}
	// Dead credentials: 403 permission-denied / 401 invalid / 402 spending-limit → delete immediately.
	if IsPermissionDenied(ev.StatusCode, ev.Body) || IsInvalidCredentials(ev.StatusCode, ev.Body) || IsSpendingLimitBlocked(ev.StatusCode, ev.Body) {
		g.deleteForDeadCredential(authIndex, ev)
		return
	}

	headers := headerFromMap(ev.ResponseHeaders)
	match, ok := MatchShortWindowQuota(MatchInput{
		Provider:        ev.Provider,
		AuthType:        ev.AuthType,
		Failed:          true,
		StatusCode:      ev.StatusCode,
		Body:            ev.Body,
		ResponseHeaders: headers,
		Now:             time.Now(),
		MaxResetSeconds: cfg.MaxResetSeconds,
	})
	if !ok {
		// Still capture free-usage actual/limit if present.
		if actual, limit, pok := ParseFreeUsageTokens(ev.Body); pok {
			_ = g.storeObserveQuota(authIndex, actual, limit)
		}
		// Time parse failure or non-short-window: silent skip with log.
		if ev.StatusCode == 429 {
			g.logf("info", "xAI 429 未满足短时额度条件(信号/重置时间)，跳过 auth=%s", authIndex)
		}
		return
	}
	if actual, limit, pok := ParseFreeUsageTokens(ev.Body); pok {
		_ = g.storeObserveQuota(authIndex, actual, limit)
	}
	// Floor recover wait if MinResetSeconds configured.
	if cfg.MinResetSeconds > 0 {
		minAt := time.Now().Add(time.Duration(cfg.MinResetSeconds) * time.Second)
		if match.RecoverAt.Before(minAt) {
			match.RecoverAt = minAt
		}
	}

	g.disableForMatch(authIndex, ev, match)
}

func (g *Guard) disableForMatch(authIndex string, ev UsageEvent, match MatchResult) {
	cfg := g.Config()
	if cfg.ManagementURL == "" || cfg.ManagementKey == "" {
		g.logf("warn", "management 未配置，仅记录不禁用 auth=%s recover_at=%s", authIndex, match.RecoverAt.Format(time.RFC3339))
		return
	}
	if g.auth == nil {
		g.logf("error", "auth lookup 未注入，跳过禁用 auth=%s", authIndex)
		return
	}

	// Ownership / manual-disable protection.
	current, err := g.findAuth(authIndex)
	if err != nil {
		g.logf("error", "查询 auth-files 失败 auth=%s: %v", authIndex, err)
		return
	}
	if current == nil {
		g.logf("warn", "auth 不存在，跳过禁用 auth=%s", authIndex)
		return
	}

	// Already disabled without our ownership => user_manual.
	existing := g.storeGet(authIndex)
	if current.Disabled {
		if existing != nil && existing.State == StateAutoDisabled && existing.DisableSource == SourcePluginAuto && existing.Owner == Owner && !existing.PreDisabled {
			// Extend cooldown.
			rec := *existing
			rec.RecoverAtMS = match.RecoverAt.UnixMilli()
			rec.Reason = match.Reason
			rec.Signal = match.Signal
			rec.LastEventHash = ev.EventHash
			if rec.Account == "" {
				rec.Account = firstNonEmpty(ev.Account, current.Account)
			}
			if rec.FileName == "" {
				rec.FileName = current.Name
			}
			if err := g.storeUpsert(rec); err != nil {
				g.logf("error", "延长冷却写状态失败 auth=%s: %v", authIndex, err)
				return
			}
			g.logf("info", "已延长 plugin_auto 冷却 auth=%s recover_at=%s", authIndex, match.RecoverAt.Format(time.RFC3339))
			return
		}
		// Mark user manual and never auto-enable.
		rec := AccountRecord{
			AuthIndex:     authIndex,
			FileName:      current.Name,
			Provider:      "xai",
			Account:       firstNonEmpty(ev.Account, current.Account),
			DisableSource: SourceUserManual,
			State:         StateUserManualDisabled,
			DisabledAtMS:  time.Now().UnixMilli(),
			PreDisabled:   true,
			Reason:        "already_disabled_without_plugin_ownership",
			LastEventHash: ev.EventHash,
		}
		_ = g.storeUpsert(rec)
		g.logf("info", "账号已禁用且无本插件所有权，标记 user_manual，跳过 auth=%s", authIndex)
		return
	}

	prev, err := g.auth.SetDisabled(authIndex, true)
	if err != nil {
		g.logf("error", "禁用失败 auth=%s: %v", authIndex, err)
		return
	}
	if prev {
		// Race: became disabled between list and patch.
		rec := AccountRecord{
			AuthIndex:     authIndex,
			FileName:      current.Name,
			Provider:      "xai",
			Account:       firstNonEmpty(ev.Account, current.Account),
			DisableSource: SourceUserManual,
			State:         StateUserManualDisabled,
			DisabledAtMS:  time.Now().UnixMilli(),
			PreDisabled:   true,
			Reason:        "pre_disabled_race",
			LastEventHash: ev.EventHash,
		}
		_ = g.storeUpsert(rec)
		g.logf("info", "禁用竞态：账号此前已禁用，标记 user_manual auth=%s", authIndex)
		return
	}

	nowMS := time.Now().UnixMilli()
	rec := AccountRecord{
		AuthIndex:     authIndex,
		FileName:      current.Name,
		Provider:      "xai",
		Account:       firstNonEmpty(ev.Account, current.Account),
		DisableSource: SourcePluginAuto,
		State:         StateAutoDisabled,
		RecoverAtMS:   match.RecoverAt.UnixMilli(),
		DisabledAtMS:  nowMS,
		PreDisabled:   false,
		Owner:         Owner,
		Reason:        match.Reason,
		Signal:        match.Signal,
		LastEventHash: ev.EventHash,
	}
	if err := g.storeUpsert(rec); err != nil {
		g.logf("error", "写状态失败 auth=%s: %v", authIndex, err)
		return
	}
	g.logf("warn", "xAI 短时额度耗尽，已禁用 auth=%s file=%s recover_at=%s signal=%s",
		authIndex, current.Name, match.RecoverAt.Format(time.RFC3339), match.Signal)
	g.NotifyWebhook("quota_cooldown", map[string]any{
		"auth_index": authIndex,
		"recover_at": match.RecoverAt.Format(time.RFC3339),
		"signal":     match.Signal,
	})
}

// Tick recovers due plugin_auto cooldowns.
func (g *Guard) Tick() {
	cfg := g.Config()
	if !cfg.Enabled {
		return
	}
	if cfg.ManagementURL == "" || cfg.ManagementKey == "" || g.auth == nil {
		return
	}
	due := g.storeDue(time.Now())
	for _, rec := range due {
		// Re-check ownership and current disabled state.
		current, err := g.findAuth(rec.AuthIndex)
		if err != nil {
			g.logf("error", "恢复前查询失败 auth=%s: %v", rec.AuthIndex, err)
			continue
		}
		if current == nil {
			_ = g.storeMarkActive(rec.AuthIndex) // drop missing
			continue
		}
		// Fresh state read.
		live := g.storeGet(rec.AuthIndex)
		if live == nil || live.DisableSource != SourcePluginAuto || live.State != StateAutoDisabled || live.PreDisabled || (live.Owner != "" && live.Owner != Owner) {
			continue
		}
		if !current.Disabled {
			// Already enabled externally; clear our record.
			_ = g.storeMarkActive(rec.AuthIndex)
			g.logf("info", "账号已外部启用，清除 cooldown auth=%s", rec.AuthIndex)
			continue
		}
		if _, err := g.auth.SetDisabled(rec.AuthIndex, false); err != nil {
			g.logf("error", "自动恢复启用失败 auth=%s: %v", rec.AuthIndex, err)
			continue
		}
		_ = g.storeMarkActive(rec.AuthIndex)
		g.logf("info", "xAI 额度重置到达，已自动启用 auth=%s file=%s", rec.AuthIndex, rec.FileName)
	}
}


func (g *Guard) deleteForDeadCredential(authIndex string, ev UsageEvent) {
	cfg := g.Config()
	if cfg.ManagementURL == "" || cfg.ManagementKey == "" || g.auth == nil {
		g.logf("warn", "死号删除但 management 未配置，无法删除 auth=%s", authIndex)
		return
	}
	current, err := g.findAuth(authIndex)
	if err != nil {
		g.logf("error", "死号删除查询 auth 失败 auth=%s: %v", authIndex, err)
		return
	}
	if current == nil {
		g.logf("info", "死号账号已不存在 auth=%s", authIndex)
		_ = g.storeMarkActive(authIndex)
		return
	}
	if err := g.auth.Delete(authIndex); err != nil {
		g.logf("error", "死号删除失败 auth=%s file=%s: %v", authIndex, current.Name, err)
		return
	}
	_ = g.storeMarkActive(authIndex)
	if g.store != nil {
		_ = g.store.AppendDelete(DeleteEvent{
			AuthIndex:   authIndex,
			FileName:    current.Name,
			Account:     firstNonEmpty(ev.Account, current.Account),
			Provider:    firstNonEmpty(ev.Provider, current.Provider, "xai"),
			Reason:      truncate(ev.Body, 240),
			DeletedAtMS: time.Now().UnixMilli(),
		})
	}
	g.logf("warn", "xAI 凭证失效/无权限，已删除账号 auth=%s file=%s account=%s reason=%s",
		authIndex, current.Name, firstNonEmpty(ev.Account, current.Account), truncate(ev.Body, 160))
	g.NotifyWebhook("dead_credential_delete", map[string]any{
		"auth_index": authIndex,
		"file_name":  current.Name,
		"account":    firstNonEmpty(ev.Account, current.Account),
	})
}

// ListDeletes returns recent permission-denied deletions.
func (g *Guard) ListDeletes(limit int) []DeleteEvent {
	if g.store == nil {
		return nil
	}
	return g.store.ListDeletes(limit)
}

func (g *Guard) recordUsageMetrics(ev UsageEvent) {
	tokens := ev.TotalTokens
	if tokens <= 0 {
		tokens = ev.InputTokens + ev.OutputTokens + ev.ReasoningTokens
	}
	// Always count the event for success/failed counters; tokens may be 0 when host omits Detail.
	if g.store != nil {
		_ = g.store.AddUsageEvent(ev.AuthIndex, tokens, ev.Failed, time.Now())
	}
}

func (g *Guard) ObserveQuota(authIndex string, actual, limit int64) {
	_ = g.storeObserveQuota(authIndex, actual, limit)
}

func (g *Guard) storeObserveQuota(authIndex string, actual, limit int64) error {
	if g.store == nil {
		return nil
	}
	return g.store.ObserveFreeQuota(authIndex, actual, limit, time.Now())
}

func (g *Guard) Metrics() MetricsView {
	// inventory filled by caller when management list available
	st := UsageStats{}
	if g.store != nil {
		st = g.store.GetUsageStats()
	}
	return BuildMetricsView(0, 0, 0, st)
}

func (g *Guard) SyncInventoryUsage(successSum, failedSum, estimatePerSuccess int64) {
	if g.store == nil {
		return
	}
	_ = g.store.SyncAuthCounters(successSum, failedSum, estimatePerSuccess, time.Now())
}

func (g *Guard) MetricsWithInventory(xaiTotal, xaiEnabled, xaiDisabled int) MetricsView {
	st := UsageStats{}
	if g.store != nil {
		st = g.store.GetUsageStats()
	}
	cfg := g.Config()
	v := BuildMetricsViewOpts(xaiTotal, xaiEnabled, xaiDisabled, st, cfg.IncludeUnobservedQuotaEst)
	// Real tokens come from usage.handle. Strip legacy success×8000 estimates from display.
	v.EstimatePerSuccess = 0
	if v.EstimatedToday > 0 {
		if v.UsedTodayDisplay >= v.EstimatedToday {
			v.UsedTodayDisplay -= v.EstimatedToday
		}
		v.EstimatedToday = 0
	}
	return v
}


// UsageAndQuotaMaps returns one-shot usage/quota maps (single store read).
func (g *Guard) UsageAndQuotaMaps() (map[string]AccountUsageSnapshot, map[string]*AccountQuotaSnapshot) {
	st := UsageStats{}
	if g.store != nil {
		st = g.store.GetUsageStats()
	}
	usage := map[string]AccountUsageSnapshot{}
	if st.UsageByAuth != nil {
		for k, v := range st.UsageByAuth {
			if v != nil {
				usage[k] = *v
			}
		}
	}
	quota := map[string]*AccountQuotaSnapshot{}
	if st.QuotaByAuth != nil {
		for k, v := range st.QuotaByAuth {
			quota[k] = v
		}
	}
	return usage, quota
}

// AccountUsage returns per-auth usage snapshot (may be nil fields as zero).
func (g *Guard) AccountUsage(authIndex string) AccountUsageSnapshot {
	st := UsageStats{}
	if g.store != nil {
		st = g.store.GetUsageStats()
	}
	if st.UsageByAuth != nil {
		if u := st.UsageByAuth[authIndex]; u != nil {
			return *u
		}
	}
	return AccountUsageSnapshot{AuthIndex: authIndex}
}

// AccountQuota returns free-usage snapshot for auth if known.
func (g *Guard) AccountQuota(authIndex string) *AccountQuotaSnapshot {
	st := UsageStats{}
	if g.store != nil {
		st = g.store.GetUsageStats()
	}
	if st.QuotaByAuth == nil {
		return nil
	}
	return st.QuotaByAuth[authIndex]
}

func (g *Guard) findAuth(authIndex string) (*AuthFile, error) {
	files, err := g.auth.List()
	if err != nil {
		return nil, err
	}
	for i := range files {
		if files[i].AuthIndex == authIndex {
			f := files[i]
			return &f, nil
		}
	}
	return nil, nil
}

func (g *Guard) storeGet(authIndex string) *AccountRecord {
	g.mu.Lock()
	store := g.store
	g.mu.Unlock()
	if store == nil {
		return nil
	}
	return store.Get(authIndex)
}

func (g *Guard) storeUpsert(rec AccountRecord) error {
	g.mu.Lock()
	store := g.store
	g.mu.Unlock()
	if store == nil {
		return fmt.Errorf("store nil")
	}
	return store.Upsert(rec)
}

func (g *Guard) storeMarkActive(authIndex string) error {
	g.mu.Lock()
	store := g.store
	g.mu.Unlock()
	if store == nil {
		return nil
	}
	return store.MarkActive(authIndex)
}

func (g *Guard) storeDue(now time.Time) []AccountRecord {
	g.mu.Lock()
	store := g.store
	g.mu.Unlock()
	if store == nil {
		return nil
	}
	return store.DueAutoDisabled(now)
}

func (g *Guard) logf(level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if g.logger != nil {
		g.logger.Log(level, msg)
	} else {
		log.Printf("[cpa-xai-quota-guard][%s] %s", level, msg)
	}
}

func trim(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 {
		c := s[len(s)-1]
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if trim(v) != "" {
			return trim(v)
		}
	}
	return ""
}

func headerFromMap(m map[string][]string) http.Header {
	if m == nil {
		return nil
	}
	return http.Header(m)
}

// BackfillFromCPAMP pulls today's xAI total_tokens from CPAMP analytics and floors local used_today.
func (g *Guard) BackfillFromCPAMP(ctx context.Context) (map[string]any, error) {
	cfg := g.Config()
	client := NewCPAMPClient(cfg.CPAMPURL, cfg.CPAMPAdminKey)
	if !client.Enabled() {
		return nil, fmt.Errorf("cpamp_url/cpamp_admin_key not configured")
	}
	now := time.Now()
	fromMS, toMS := DayRangeShanghai(now)
	sum, err := client.FetchXAISummary(ctx, fromMS, toMS)
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "127.0.0.1") && strings.Contains(msg, "connection refused") {
			return nil, fmt.Errorf("%w; plugin runs in container: use host LAN IP (e.g. http://10.10.10.5:18317) instead of 127.0.0.1", err)
		}
		return nil, err
	}
	applied := false
	if g.store != nil {
		applied, err = g.store.ApplyCalendarBackfill(DayKeyShanghai(now), sum.TotalTokens, sum.TotalCalls, "cpamp_backfill", now)
		if err != nil {
			return nil, err
		}
	}
	return map[string]any{
		"source":         "cpamp_analytics",
		"day_key":        DayKeyShanghai(now),
		"from_ms":        fromMS,
		"to_ms":          toMS,
		"cpamp_tokens":   sum.TotalTokens,
		"cpamp_calls":    sum.TotalCalls,
		"cpamp_success":  sum.SuccessCalls,
		"cpamp_failure":  sum.FailureCalls,
		"applied":        applied,
	}, nil
}

// NotifyWebhook posts event payload if webhook_url configured.
func (g *Guard) NotifyWebhook(event string, fields map[string]any) {
	cfg := g.Config()
	if strings.TrimSpace(cfg.WebhookURL) == "" {
		return
	}
	payload := map[string]any{
		"plugin":    "cpa-xai-quota-guard",
		"event":     event,
		"ts_ms":     time.Now().UnixMilli(),
	}
	for k, v := range fields {
		payload[k] = v
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if err := PostWebhook(ctx, cfg.WebhookURL, payload); err != nil {
			g.logf("warn", "webhook %s failed: %v", event, err)
		}
	}()
}

// HealthCheck returns self-check diagnostics.
func (g *Guard) HealthCheck() map[string]any {
	cfg := g.Config()
	st := UsageStats{}
	if g.store != nil {
		st = g.store.GetUsageStats()
	}
	st = *EnsureUsageStats(&st)
	mgmtOK := cfg.ManagementURL != "" && cfg.ManagementKey != ""
	authOK := false
	authErr := ""
	xaiN := 0
	if g.auth != nil && mgmtOK {
		files, err := g.auth.List()
		if err != nil {
			authErr = err.Error()
		} else {
			authOK = true
			for _, f := range files {
				if IsXAIProvider(f.Provider, "") {
					xaiN++
				}
			}
		}
	}
	detailOK := st.ZeroTokenStreak < ZeroTokenAlertThreshold
	return map[string]any{
		"enabled":                 cfg.Enabled,
		"management_configured":   mgmtOK,
		"auth_list_ok":            authOK,
		"auth_list_error":         authErr,
		"xai_auth_files":          xaiN,
		"state_path":              cfg.StatePath,
		"usage_day_key":           st.DayKey,
		"used_today":              st.UsedToday,
		"requests_today":          st.RequestsToday,
		"zero_token_streak":       st.ZeroTokenStreak,
		"detail_tokens_healthy":   detailOK,
		"cpamp_configured":        strings.TrimSpace(cfg.CPAMPURL) != "" && strings.TrimSpace(cfg.CPAMPAdminKey) != "",
		"webhook_configured":      strings.TrimSpace(cfg.WebhookURL) != "",
		"include_unobserved_est":  cfg.IncludeUnobservedQuotaEst,
		"min_reset_seconds":       cfg.MinResetSeconds,
		"max_reset_seconds":       cfg.MaxResetSeconds,
		"ok":                      cfg.Enabled && mgmtOK && authOK && detailOK,
	}
}


func (g *Guard) UsageByAuthMap() map[string]AccountUsageSnapshot {
	st := UsageStats{}
	if g.store != nil {
		st = g.store.GetUsageStats()
	}
	out := map[string]AccountUsageSnapshot{}
	for k, v := range st.UsageByAuth {
		if v != nil {
			out[k] = *v
		}
	}
	return out
}
