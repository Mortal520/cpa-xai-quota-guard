package xaiquota

import (
	"fmt"
	"io"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultPatrolModel is the free-tier-friendly probe model.
// Paid models (e.g. grok-3 full) often return personal-team-blocked:spending-limit
// even when free-tier chat still works — causing false dead/cooldown signals.
const DefaultPatrolModel = "grok-4.5-build-free"

// SuggestedPatrolModels are common xAI ids shown when live /models is empty.
var SuggestedPatrolModels = []string{
	"grok-4.5-build-free",
	"grok-3-mini",
	"grok-3",
	"grok-2-1212",
	"grok-2",
}

// patrolState tracks the in-progress or last-completed sweep.
type patrolState struct {
	mu            sync.Mutex
	running       bool
	startedAtMS   int64
	completedAtMS int64
	totalCandidates int
	totalProbed   int
	totalDeleted  int
	totalErrors   int
	totalAlive    int
	totalSkipped  int
	workers       int
	lastError     string
	lastSweepLog  []patrolLogEntry
	stopRequested bool
}

type patrolLogEntry struct {
	TimeMS    int64  `json:"time_ms"`
	AuthIndex string `json:"auth_index"`
	FileName  string `json:"file_name"`
	Account   string `json:"account"`
	Action    string `json:"action"` // "alive", "deleted", "error", "cooldown_skip"
	Reason    string `json:"reason"`
	HTTPCode  int    `json:"http_code,omitempty"`
}

// PatrolStatus is the JSON view for the UI.
type PatrolStatus struct {
	Running         bool             `json:"running"`
	StartedAtMS     int64            `json:"started_at_ms"`
	CompletedAtMS   int64            `json:"completed_at_ms"`
	TotalCandidates int              `json:"total_candidates"`
	TotalProbed     int              `json:"total_probed"`
	TotalDeleted    int              `json:"total_deleted"`
	TotalErrors     int              `json:"total_errors"`
	TotalAlive      int              `json:"total_alive"`
	TotalSkipped    int              `json:"total_skipped"`
	Workers         int              `json:"workers"`
	LastError       string           `json:"last_error,omitempty"`
	RecentLog       []patrolLogEntry `json:"recent_log,omitempty"`
}

// authFileJSON is the on-disk structure of a CPA auth file.
type authFileJSON struct {
	AccessToken string            `json:"access_token"`
	BaseURL     string            `json:"base_url"`
	AuthKind    string            `json:"auth_kind"`
	Type        string            `json:"type"`
	Email       string            `json:"email"`
	Disabled    bool              `json:"disabled"`
	Headers     map[string]string `json:"headers"`
}

// probeResult holds the outcome of probing one credential.
type probeResult struct {
	authIndex string
	fileName  string
	account   string
	action    string // "alive", "deleted", "error", "cooldown_skip"
	reason    string
	httpCode  int
	modelUsed string
}

// PatrolSweep iterates all enabled xAI auth files with a worker pool,
// probes the upstream directly, and deletes dead credentials.
func (g *Guard) PatrolSweep() PatrolStatus {
	g.patrol.mu.Lock()
	if g.patrol.running {
		g.patrol.mu.Unlock()
		return g.PatrolStatus()
	}
	g.patrol.running = true
	g.patrol.startedAtMS = time.Now().UnixMilli()
	g.patrol.completedAtMS = 0
	g.patrol.totalCandidates = 0
	g.patrol.totalProbed = 0
	g.patrol.totalDeleted = 0
	g.patrol.totalErrors = 0
	g.patrol.totalAlive = 0
	g.patrol.totalSkipped = 0
	g.patrol.workers = 0
	g.patrol.lastError = ""
	g.patrol.lastSweepLog = nil
	g.patrol.stopRequested = false
	g.patrol.mu.Unlock()

	defer func() {
		g.patrol.mu.Lock()
		g.patrol.running = false
		g.patrol.completedAtMS = time.Now().UnixMilli()
		g.patrol.mu.Unlock()
	}()

	cfg := g.Config()
	if !cfg.Enabled {
		g.setPatrolError("plugin disabled")
		return g.PatrolStatus()
	}
	if g.auth == nil {
		g.setPatrolError("auth lookup nil")
		return g.PatrolStatus()
	}
	authDir := strings.TrimSpace(cfg.PatrolAuthDir)
	if authDir == "" {
		g.setPatrolError("patrol_auth_dir not configured")
		return g.PatrolStatus()
	}

	files, err := g.auth.List()
	if err != nil {
		g.setPatrolError(fmt.Sprintf("list auth files: %v", err))
		return g.PatrolStatus()
	}

	// Full sweep: all enabled xAI, plus plugin_auto spending_limit accounts
	// (disabled for cooldown but must be re-probed for recovery).
	// Never include user_manual or 429 free-usage cooldown-only disables here
	// unless they are spending_limit (signal-gated).
	candidates := make([]AuthFile, 0, len(files))
	for _, f := range files {
		if !IsXAIProvider(f.Provider, "") {
			continue
		}
		if !f.Disabled {
			candidates = append(candidates, f)
			continue
		}
		live := g.storeGet(f.AuthIndex)
		if live != nil && live.State == StateAutoDisabled && live.DisableSource == SourcePluginAuto &&
			live.Owner == Owner && !live.PreDisabled && live.Signal == "spending_limit" {
			candidates = append(candidates, f)
		}
	}

	batchLimit := cfg.PatrolBatchSize
	if batchLimit > 0 && batchLimit < len(candidates) {
		candidates = candidates[:batchLimit]
	}

	workers := cfg.PatrolConcurrency
	if workers <= 0 {
		workers = 8
	}
	if workers > len(candidates) && len(candidates) > 0 {
		workers = len(candidates)
	}

	g.patrol.mu.Lock()
	g.patrol.totalCandidates = len(candidates)
	g.patrol.workers = workers
	g.patrol.mu.Unlock()

	if len(candidates) == 0 {
		return g.PatrolStatus()
	}

	probeTimeout := time.Duration(cfg.PatrolTimeout) * time.Second
	if probeTimeout <= 0 {
		probeTimeout = 10 * time.Second
	}

	client := g.newPatrolHTTPClient(probeTimeout, cfg.PatrolProxyURL)

	jobs := make(chan AuthFile, workers*2)
	var wg sync.WaitGroup
	var stopFlag int32

	// Watch stop request in a light loop via atomic.
	go func() {
		for {
			g.patrol.mu.Lock()
			stop := g.patrol.stopRequested
			running := g.patrol.running
			g.patrol.mu.Unlock()
			if stop || !running {
				atomic.StoreInt32(&stopFlag, 1)
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
	}()

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range jobs {
				if atomic.LoadInt32(&stopFlag) == 1 {
					return
				}
				result := g.probeOneCredential(f, authDir, client)
				g.recordProbeResult(result)
			}
		}()
	}

	for _, f := range candidates {
		if atomic.LoadInt32(&stopFlag) == 1 {
			break
		}
		jobs <- f
	}
	close(jobs)
	wg.Wait()

	return g.PatrolStatus()
}

func (g *Guard) setPatrolError(msg string) {
	g.patrol.mu.Lock()
	g.patrol.lastError = msg
	g.patrol.mu.Unlock()
}

func (g *Guard) newPatrolHTTPClient(timeout time.Duration, proxyURL string) *http.Client {
	tr := &http.Transport{
		MaxIdleConns:        64,
		MaxIdleConnsPerHost: 32,
		IdleConnTimeout:     30 * time.Second,
	}
	if strings.TrimSpace(proxyURL) != "" {
		if u, err := url.Parse(strings.TrimSpace(proxyURL)); err == nil {
			tr.Proxy = http.ProxyURL(u)
		}
	}
	return &http.Client{Timeout: timeout, Transport: tr}
}

func (g *Guard) recordProbeResult(r probeResult) {
	g.patrol.mu.Lock()
	g.patrol.totalProbed++
	switch r.action {
	case "deleted":
		g.patrol.totalDeleted++
	case "error":
		g.patrol.totalErrors++
	case "cooldown_skip", "cooldown":
		g.patrol.totalSkipped++
	case "reenabled":
		g.patrol.totalAlive++
	case "alive":
		g.patrol.totalAlive++
	default:
		g.patrol.totalAlive++
	}
	entry := patrolLogEntry{
		TimeMS:    time.Now().UnixMilli(),
		AuthIndex: r.authIndex,
		FileName:  r.fileName,
		Account:   r.account,
		Action:    r.action,
		Reason:    r.reason,
		HTTPCode:  r.httpCode,
	}
	if len(g.patrol.lastSweepLog) >= 500 {
		g.patrol.lastSweepLog = g.patrol.lastSweepLog[len(g.patrol.lastSweepLog)-499:]
	}
	g.patrol.lastSweepLog = append(g.patrol.lastSweepLog, entry)
	g.patrol.mu.Unlock()
}

// probeOneCredential reads the auth file, extracts token, sends a minimal probe.
func (g *Guard) probeOneCredential(f AuthFile, authDir string, client *http.Client) probeResult {
	filePath := filepath.Join(authDir, f.Name)
	raw, err := os.ReadFile(filePath)
	if err != nil {
		if !strings.HasSuffix(f.Name, ".json") {
			raw, err = os.ReadFile(filePath + ".json")
		}
	}
	if err != nil {
		return probeResult{
			authIndex: f.AuthIndex,
			fileName:  f.Name,
			account:   f.Account,
			action:    "error",
			reason:    fmt.Sprintf("read auth file: %v", err),
		}
	}

	var af authFileJSON
	if err := json.Unmarshal(raw, &af); err != nil {
		return probeResult{
			authIndex: f.AuthIndex,
			fileName:  f.Name,
			account:   f.Account,
			action:    "error",
			reason:    fmt.Sprintf("parse auth file: %v", err),
		}
	}
	if af.AccessToken == "" {
		return probeResult{
			authIndex: f.AuthIndex,
			fileName:  f.Name,
			account:   f.Account,
			action:    "error",
			reason:    "no access_token in auth file",
		}
	}

	// Note: spending_limit cooldowns are intentionally re-probed (included in candidates).
	// Free-usage cooldowns are disabled and not in candidates, so no skip needed here.
	live := g.storeGet(f.AuthIndex)

	baseURL := strings.TrimRight(strings.TrimSpace(af.BaseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.x.ai/v1"
	}

	model := strings.TrimSpace(g.Config().PatrolModel)
	if model == "" {
		model = DefaultPatrolModel
	}
	probePayload, _ := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": 1,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
	})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/chat/completions", strings.NewReader(string(probePayload)))
	if err != nil {
		return probeResult{
			authIndex: f.AuthIndex,
			fileName:  f.Name,
			account:   f.Account,
			action:    "error",
			reason:    fmt.Sprintf("build request: %v", err),
		}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+af.AccessToken)
	for k, v := range af.Headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return probeResult{
			authIndex: f.AuthIndex,
			fileName:  f.Name,
			account:   f.Account,
			action:    "error",
			reason:    fmt.Sprintf("probe request failed (network): %v", err),
		}
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(resp.Body)
	bodyStr := string(bodyBytes)
	code := resp.StatusCode
	_ = model // used in results

	// Outcomes:
	// - 200 / non-dead 5xx: alive; if was spending_limit cooldown → re-enable
	// - 429 free-usage: alive (quota window); if was spending_limit → re-enable (free tier usable)
	// - 402 spending-limit: soft-disable (plugin_auto, signal=spending_limit), do NOT delete
	// - 403/401: delete dead credential
	reenableIfSpending := func(reason string) probeResult {
		if live != nil && live.State == StateAutoDisabled && live.DisableSource == SourcePluginAuto &&
			live.Signal == "spending_limit" && live.Owner == Owner && !live.PreDisabled {
			if g.auth != nil {
				if _, err := g.auth.SetDisabled(f.AuthIndex, false); err != nil {
					return probeResult{
						authIndex: f.AuthIndex, fileName: f.Name, account: f.Account,
						action: "error", reason: fmt.Sprintf("re-enable failed: %v", err), httpCode: code,
					}
				}
			}
			_ = g.storeMarkActive(f.AuthIndex)
			g.logf("info", "patrol 探测恢复，已启用 spending_limit 账号 auth=%s reason=%s", f.AuthIndex, reason)
			g.NotifyWebhook("patrol_spending_recovered", map[string]any{
				"auth_index": f.AuthIndex, "file_name": f.Name, "account": f.Account,
				"http_code": code, "reason": reason,
			})
			return probeResult{
				authIndex: f.AuthIndex, fileName: f.Name, account: f.Account,
				action: "reenabled", reason: reason + " · model=" + model, httpCode: code, modelUsed: model,
			}
		}
		return probeResult{
			authIndex: f.AuthIndex, fileName: f.Name, account: f.Account,
			action: "alive", reason: reason + " · model=" + model, httpCode: code, modelUsed: model,
		}
	}

	if code == http.StatusOK {
		return reenableIfSpending("200 OK")
	}
	if code == http.StatusTooManyRequests {
		// 429 free-usage means free tier still works → treat as recovered for spending cooldown
		return reenableIfSpending("429 rate-limited (free quota window; not spending-limit)")
	}
	if IsSpendingLimitBlocked(code, bodyStr) {
		// Soft-disable only (distinct signal from 429 free-usage).
		match, ok := MatchSpendingLimitQuota(MatchInput{
			Provider: "xai", Failed: true, StatusCode: code, Body: bodyStr, Now: time.Now(),
			MaxResetSeconds: g.Config().MaxResetSeconds,
		})
		if !ok {
			return probeResult{
				authIndex: f.AuthIndex, fileName: f.Name, account: f.Account,
				action: "error", reason: "spending-limit body unmatched", httpCode: code,
			}
		}
		// Already under our spending cooldown → extend recover, keep disabled.
		if live != nil && live.State == StateAutoDisabled && live.DisableSource == SourcePluginAuto &&
			live.Signal == "spending_limit" && live.Owner == Owner && !live.PreDisabled {
			rec := *live
			rec.RecoverAtMS = match.RecoverAt.UnixMilli()
			rec.Reason = match.Reason
			rec.Signal = match.Signal
			_ = g.storeUpsert(rec)
			return probeResult{
				authIndex: f.AuthIndex, fileName: f.Name, account: f.Account,
				action: "cooldown", reason: fmt.Sprintf("spending-limit still active (model=%s)", model), httpCode: code, modelUsed: model,
			}
		}
		// Disable if currently enabled.
		if g.auth != nil && !f.Disabled {
			prev, err := g.auth.SetDisabled(f.AuthIndex, true)
			if err != nil {
				return probeResult{
					authIndex: f.AuthIndex, fileName: f.Name, account: f.Account,
					action: "error", reason: fmt.Sprintf("disable failed: %v", err), httpCode: code,
				}
			}
			if prev {
				// External disable → do not own.
				return probeResult{
					authIndex: f.AuthIndex, fileName: f.Name, account: f.Account,
					action: "cooldown_skip", reason: "already disabled externally", httpCode: code,
				}
			}
		}
		nowMS := time.Now().UnixMilli()
		_ = g.storeUpsert(AccountRecord{
			AuthIndex: f.AuthIndex, FileName: f.Name, Provider: "xai", Account: f.Account,
			DisableSource: SourcePluginAuto, State: StateAutoDisabled,
			RecoverAtMS: match.RecoverAt.UnixMilli(), DisabledAtMS: nowMS,
			PreDisabled: false, Owner: Owner, Reason: match.Reason, Signal: match.Signal,
		})
		g.logf("warn", "patrol spending-limit 已禁用 auth=%s recover_at=%s", f.AuthIndex, match.RecoverAt.Format(time.RFC3339))
		g.NotifyWebhook("patrol_spending_disable", map[string]any{
			"auth_index": f.AuthIndex, "file_name": f.Name, "account": f.Account,
			"http_code": code, "recover_at": match.RecoverAt.Format(time.RFC3339),
		})
		return probeResult{
			authIndex: f.AuthIndex, fileName: f.Name, account: f.Account,
			action: "cooldown", reason: fmt.Sprintf("model=%s · %s", model, truncate(bodyStr, 180)), httpCode: code, modelUsed: model,
		}
	}
	if IsPermissionDenied(code, bodyStr) || IsInvalidCredentials(code, bodyStr) {
		if err := g.auth.Delete(f.AuthIndex); err != nil {
			return probeResult{
				authIndex: f.AuthIndex, fileName: f.Name, account: f.Account,
				action: "error", reason: fmt.Sprintf("delete failed: %v", err), httpCode: code,
			}
		}
		_ = g.storeRemove(f.AuthIndex)
		if g.store != nil {
			_ = g.store.AppendDelete(DeleteEvent{
				AuthIndex: f.AuthIndex, FileName: f.Name, Account: f.Account, Provider: "xai",
				Reason: fmt.Sprintf("patrol: %s", truncate(bodyStr, 240)), DeletedAtMS: time.Now().UnixMilli(),
			})
		}
		g.logf("warn", "patrol 删除死号 auth=%s file=%s code=%d reason=%s", f.AuthIndex, f.Name, code, truncate(bodyStr, 120))
		g.NotifyWebhook("patrol_dead_credential_delete", map[string]any{
			"auth_index": f.AuthIndex, "file_name": f.Name, "account": f.Account,
			"http_code": code, "reason": truncate(bodyStr, 160),
		})
		return probeResult{
			authIndex: f.AuthIndex, fileName: f.Name, account: f.Account,
			action: "deleted", reason: fmt.Sprintf("model=%s · %s", model, truncate(bodyStr, 180)), httpCode: code, modelUsed: model,
		}
	}

	// Other codes: if probing a spending cooldown account, keep disabled (not recovered yet).
	if live != nil && live.State == StateAutoDisabled && live.Signal == "spending_limit" {
		return probeResult{
			authIndex: f.AuthIndex, fileName: f.Name, account: f.Account,
			action: "cooldown", reason: fmt.Sprintf("HTTP %d (spending cooldown not recovered)", code), httpCode: code,
		}
	}
	return probeResult{
		authIndex: f.AuthIndex, fileName: f.Name, account: f.Account,
		action: "alive", reason: fmt.Sprintf("HTTP %d (not a dead-credential signal)", code), httpCode: code,
	}
}

// PatrolStatus returns the current patrol state for the UI.
func (g *Guard) PatrolStatus() PatrolStatus {
	g.patrol.mu.Lock()
	defer g.patrol.mu.Unlock()

	log := make([]patrolLogEntry, len(g.patrol.lastSweepLog))
	copy(log, g.patrol.lastSweepLog)
	// newest first for UI
	for i, j := 0, len(log)-1; i < j; i, j = i+1, j-1 {
		log[i], log[j] = log[j], log[i]
	}

	return PatrolStatus{
		Running:         g.patrol.running,
		StartedAtMS:     g.patrol.startedAtMS,
		CompletedAtMS:   g.patrol.completedAtMS,
		TotalCandidates: g.patrol.totalCandidates,
		TotalProbed:     g.patrol.totalProbed,
		TotalDeleted:    g.patrol.totalDeleted,
		TotalErrors:     g.patrol.totalErrors,
		TotalAlive:      g.patrol.totalAlive,
		TotalSkipped:    g.patrol.totalSkipped,
		Workers:         g.patrol.workers,
		LastError:       g.patrol.lastError,
		RecentLog:       log,
	}
}

// PatrolStop signals an in-progress sweep to stop after current in-flight probes.
func (g *Guard) PatrolStop() {
	g.patrol.mu.Lock()
	g.patrol.stopRequested = true
	g.patrol.mu.Unlock()
}

// PatrolRunOnce triggers an async manual sweep if not already running.
func (g *Guard) PatrolRunOnce() PatrolStatus {
	g.patrol.mu.Lock()
	if g.patrol.running {
		g.patrol.mu.Unlock()
		return g.PatrolStatus()
	}
	g.patrol.mu.Unlock()
	// Mark running ASAP so UI sees activity before goroutine starts.
	// PatrolSweep re-checks and sets counters.
	go g.PatrolSweep()
	// Small spin so first status after POST often shows running=true.
	for i := 0; i < 20; i++ {
		st := g.PatrolStatus()
		if st.Running || st.LastError != "" {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	return g.PatrolStatus()
}

// ListPatrolModels uses one enabled xAI credential to GET /models from upstream.
// Falls back to SuggestedPatrolModels when no credential or request fails.
func (g *Guard) ListPatrolModels() (models []string, source string, errMsg string) {
	seen := map[string]bool{}
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		models = append(models, id)
	}
	for _, s := range SuggestedPatrolModels {
		add(s)
	}
	source = "suggested"
	cfg := g.Config()
	if g.auth == nil {
		return models, source, "no auth lookup"
	}
	files, err := g.auth.List()
	if err != nil {
		return models, source, err.Error()
	}
	authDir := strings.TrimSpace(cfg.PatrolAuthDir)
	if authDir == "" {
		return models, source, "patrol_auth_dir empty"
	}
	var pick *AuthFile
	for i := range files {
		f := &files[i]
		if !IsXAIProvider(f.Provider, "") {
			continue
		}
		if f.Disabled {
			continue
		}
		pick = f
		break
	}
	if pick == nil {
		return models, source, "no enabled xAI credential"
	}
	filePath := filepath.Join(authDir, pick.Name)
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return models, source, err.Error()
	}
	var af authFileJSON
	if err := json.Unmarshal(raw, &af); err != nil {
		return models, source, err.Error()
	}
	if af.AccessToken == "" {
		return models, source, "no access_token"
	}
	baseURL := strings.TrimRight(strings.TrimSpace(af.BaseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.x.ai/v1"
	}
	client := g.newPatrolHTTPClient(12*time.Second, cfg.PatrolProxyURL)
	req, err := http.NewRequest(http.MethodGet, baseURL+"/models", nil)
	if err != nil {
		return models, source, err.Error()
	}
	req.Header.Set("Authorization", "Bearer "+af.AccessToken)
	for k, v := range af.Headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return models, source, err.Error()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return models, source, fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 160))
	}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		// try raw array
		var arr []struct {
			ID string `json:"id"`
		}
		if err2 := json.Unmarshal(body, &arr); err2 != nil {
			return models, source, "parse models: " + err.Error()
		}
		for _, m := range arr {
			add(m.ID)
		}
	} else {
		for _, m := range parsed.Data {
			add(m.ID)
		}
	}
	if len(parsed.Data) > 0 || len(models) > len(SuggestedPatrolModels) {
		source = "credential:" + pick.Name
	}
	// ensure current configured model is listed
	add(cfg.PatrolModel)
	add(DefaultPatrolModel)
	return models, source, ""
}
