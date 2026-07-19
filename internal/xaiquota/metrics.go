package xaiquota

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultFreeLimit is the free-tier rolling window token limit used for daily pool estimates.
// Product default: 2M tokens / account / rolling 24h (observed in live free-usage limit samples).
const DefaultFreeLimit int64 = 2_000_000

// ZeroTokenAlertThreshold consecutive successful xAI events with empty Detail.
const ZeroTokenAlertThreshold int64 = 5

var reTokensActualLimit = regexp.MustCompile(`(?i)tokens\s*\(\s*actual\s*/\s*limit\s*\)\s*:\s*([0-9][0-9_,\.]*)\s*/\s*([0-9][0-9_,\.]*)`)

// AccountQuotaSnapshot is the latest free-usage actual/limit observed for one auth.
type AccountQuotaSnapshot struct {
	AuthIndex   string `json:"auth_index"`
	Actual      int64  `json:"actual"`
	Limit       int64  `json:"limit"`
	UpdatedAtMS int64  `json:"updated_at_ms"`
	Source      string `json:"source,omitempty"`
}

// AccountUsageSnapshot is per-auth plugin-side usage (calendar day + lifetime).
type AccountUsageSnapshot struct {
	AuthIndex     string `json:"auth_index"`
	UsedToday     int64  `json:"used_today"`
	UsedTotal     int64  `json:"used_total"`
	RequestsToday int64  `json:"requests_today"`
	RequestsTotal int64  `json:"requests_total"`
	SuccessTotal  int64  `json:"success_total"`
	FailedTotal   int64  `json:"failed_total"`
	LastTokens    int64  `json:"last_tokens"`
	LastAtMS      int64  `json:"last_at_ms,omitempty"`
	LastFailed    bool   `json:"last_failed,omitempty"`
	ZeroTokenOK   int64  `json:"zero_token_success,omitempty"`
}

// UsageStats is durable plugin-side usage/quota aggregation for xAI only.
type UsageStats struct {
	DayKey              string                           `json:"day_key"`
	UsedToday           int64                            `json:"used_today"`
	UsedTotal           int64                            `json:"used_total"`
	RequestsToday       int64                            `json:"requests_today"`
	RequestsTotal       int64                            `json:"requests_total"`
	SuccessEvents       int64                            `json:"success_events"`
	FailedEvents        int64                            `json:"failed_events"`
	EstimatedToday      int64                            `json:"estimated_today"`
	EstimatedTotal      int64                            `json:"estimated_total"`
	LastSuccessSum      int64                            `json:"last_success_sum"`
	LastFailedSum       int64                            `json:"last_failed_sum"`
	LastEventAtMS       int64                            `json:"last_event_at_ms,omitempty"`
	QuotaByAuth         map[string]*AccountQuotaSnapshot `json:"quota_by_auth,omitempty"`
	UsageByAuth         map[string]*AccountUsageSnapshot `json:"usage_by_auth,omitempty"`
	DefaultLimitPerAcct int64                            `json:"default_limit_per_acct,omitempty"`
	// Detail health
	ZeroTokenSuccessToday  int64 `json:"zero_token_success_today,omitempty"`
	ZeroTokenSuccessTotal  int64 `json:"zero_token_success_total,omitempty"`
	ZeroTokenStreak        int64 `json:"zero_token_streak,omitempty"`
	LastZeroTokenAtMS      int64 `json:"last_zero_token_at_ms,omitempty"`
	LastNonZeroTokenAtMS   int64  `json:"last_non_zero_token_at_ms,omitempty"`
	BackfillSource         string `json:"backfill_source,omitempty"`
	BackfillAtMS           int64  `json:"backfill_at_ms,omitempty"`
	BackfillTokensFloor    int64  `json:"backfill_tokens_floor,omitempty"`
}

// MetricsView is the computed dashboard payload.
type MetricsView struct {
	XAITotal           int    `json:"xai_total"`
	XAIEnabled         int    `json:"xai_enabled"`
	XAIDisabled        int    `json:"xai_disabled"`
	QuotaTotalEst      int64  `json:"quota_total_est"`
	QuotaUsedKnown     int64  `json:"quota_used_known"`
	QuotaLimitKnown    int64  `json:"quota_limit_known"`
	QuotaKnownAccounts int    `json:"quota_known_accounts"`
	// Known-only pool (no unobserved * 1e6). Prefer this for honest display.
	QuotaTotalKnownOnly int64 `json:"quota_total_known_only"`
	UnobservedAccounts  int   `json:"unobserved_accounts"`
	IncludeUnobservedEst bool `json:"include_unobserved_est"`

	UsedToday        int64  `json:"used_today"`
	UsedTotal        int64  `json:"used_total"`
	UsedTodayDisplay int64  `json:"used_today_display"`
	UsedTotalDisplay int64  `json:"used_total_display"`
	RequestsToday    int64  `json:"requests_today"`
	RequestsTotal    int64  `json:"requests_total"`
	EstimatedToday   int64  `json:"estimated_today"`
	DefaultLimitPerAcct int64 `json:"default_limit_per_acct"`
	EstimatePerSuccess  int64 `json:"estimate_per_success"`
	DayKey              string `json:"day_key"`

	// Rolling 24h free-usage pool (from xAI free-usage actual/limit snapshots).
	RollingUsedKnown  int64 `json:"rolling_used_known"`
	RollingLimitKnown int64 `json:"rolling_limit_known"`
	RollingAccounts   int   `json:"rolling_accounts"`

	// Detail health
	ZeroTokenSuccessToday int64  `json:"zero_token_success_today"`
	ZeroTokenStreak       int64  `json:"zero_token_streak"`
	DetailMissingAlert    bool   `json:"detail_missing_alert"`
	DetailAlertMessage    string `json:"detail_alert_message,omitempty"`
	BackfillSource         string `json:"backfill_source,omitempty"`
	BackfillAtMS           int64  `json:"backfill_at_ms,omitempty"`
	BackfillTokensFloor    int64  `json:"backfill_tokens_floor,omitempty"`

	Note string `json:"note,omitempty"`
}

// ParseFreeUsageTokens extracts actual/limit from Grok free-usage error bodies.
func ParseFreeUsageTokens(body string) (actual, limit int64, ok bool) {
	body = strings.TrimSpace(body)
	if body == "" {
		return 0, 0, false
	}
	m := reTokensActualLimit.FindStringSubmatch(body)
	if len(m) != 3 {
		return 0, 0, false
	}
	a, err1 := parseFlexibleInt(m[1])
	l, err2 := parseFlexibleInt(m[2])
	if err1 != nil || err2 != nil || l <= 0 {
		return 0, 0, false
	}
	return a, l, true
}

func parseFlexibleInt(s string) (int64, error) {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, ",", "")
	s = strings.ReplaceAll(s, "_", "")
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	return strconv.ParseInt(s, 10, 64)
}

// DayKeyShanghai returns YYYY-MM-DD in Asia/Shanghai.
func DayKeyShanghai(t time.Time) string {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	return t.In(loc).Format("2006-01-02")
}

// EnsureUsageStats normalizes nil maps/defaults.
func EnsureUsageStats(s *UsageStats) *UsageStats {
	if s == nil {
		s = &UsageStats{}
	}
	if s.QuotaByAuth == nil {
		s.QuotaByAuth = map[string]*AccountQuotaSnapshot{}
	}
	if s.UsageByAuth == nil {
		s.UsageByAuth = map[string]*AccountUsageSnapshot{}
	}
	// Always align with product constant so upgrades (1M→2M) apply to existing state files.
	s.DefaultLimitPerAcct = DefaultFreeLimit
	if s.DayKey == "" {
		s.DayKey = DayKeyShanghai(time.Now())
	}
	return s
}

func (s *Store) GetUsageStats() UsageStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := EnsureUsageStats(s.Usage)
	cp := *st
	cp.QuotaByAuth = map[string]*AccountQuotaSnapshot{}
	for k, v := range st.QuotaByAuth {
		if v == nil {
			continue
		}
		vv := *v
		cp.QuotaByAuth[k] = &vv
	}
	cp.UsageByAuth = map[string]*AccountUsageSnapshot{}
	for k, v := range st.UsageByAuth {
		if v == nil {
			continue
		}
		vv := *v
		cp.UsageByAuth[k] = &vv
	}
	return cp
}

func (s *Store) mutateUsage(fn func(st *UsageStats)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Usage = EnsureUsageStats(s.Usage)
	fn(s.Usage)
	s.Updated = time.Now().UnixMilli()
	return s.persistLocked()
}

// AddUsageEvent records one usage.handle event. authIndex may be empty.
func (s *Store) AddUsageEvent(authIndex string, tokens int64, failed bool, at time.Time) error {
	if tokens < 0 {
		tokens = 0
	}
	authIndex = strings.TrimSpace(authIndex)
	return s.mutateUsage(func(st *UsageStats) {
		day := DayKeyShanghai(at)
		if st.DayKey != day {
			st.DayKey = day
			st.UsedToday = 0
			st.RequestsToday = 0
			st.EstimatedToday = 0
			st.ZeroTokenSuccessToday = 0
			for _, u := range st.UsageByAuth {
				if u == nil {
					continue
				}
				u.UsedToday = 0
				u.RequestsToday = 0
			}
		}
		st.UsedToday += tokens
		st.UsedTotal += tokens
		st.RequestsToday++
		st.RequestsTotal++
		if failed {
			st.FailedEvents++
		} else {
			st.SuccessEvents++
			if tokens == 0 {
				st.ZeroTokenSuccessToday++
				st.ZeroTokenSuccessTotal++
				st.ZeroTokenStreak++
				st.LastZeroTokenAtMS = at.UnixMilli()
			} else {
				st.ZeroTokenStreak = 0
				st.LastNonZeroTokenAtMS = at.UnixMilli()
			}
		}
		st.LastEventAtMS = at.UnixMilli()

		if authIndex == "" {
			return
		}
		u := st.UsageByAuth[authIndex]
		if u == nil {
			u = &AccountUsageSnapshot{AuthIndex: authIndex}
			st.UsageByAuth[authIndex] = u
		}
		u.UsedToday += tokens
		u.UsedTotal += tokens
		u.RequestsToday++
		u.RequestsTotal++
		if failed {
			u.FailedTotal++
		} else {
			u.SuccessTotal++
			if tokens == 0 {
				u.ZeroTokenOK++
			}
		}
		u.LastTokens = tokens
		u.LastAtMS = at.UnixMilli()
		u.LastFailed = failed
	})
}

// AddUsageTokens is kept for older call sites (no per-auth).
func (s *Store) AddUsageTokens(tokens int64, failed bool, at time.Time) error {
	return s.AddUsageEvent("", tokens, failed, at)
}

// SyncAuthCounters advances request counters from CPA auth-files success/failed.
// estimatePerSuccess should normally be 0; real tokens come from usage.handle.
func (s *Store) SyncAuthCounters(successSum, failedSum, estimatePerSuccess int64, at time.Time) error {
	if estimatePerSuccess < 0 {
		estimatePerSuccess = 0
	}
	return s.mutateUsage(func(st *UsageStats) {
		day := DayKeyShanghai(at)
		if st.DayKey != day {
			st.DayKey = day
			st.UsedToday = 0
			st.RequestsToday = 0
			st.EstimatedToday = 0
			st.ZeroTokenSuccessToday = 0
			st.LastSuccessSum = successSum
			st.LastFailedSum = failedSum
			st.LastEventAtMS = at.UnixMilli()
			return
		}
		if st.LastSuccessSum == 0 && st.LastFailedSum == 0 && st.RequestsTotal == 0 && st.UsedTotal == 0 && (successSum > 0 || failedSum > 0) {
			st.LastSuccessSum = successSum
			st.LastFailedSum = failedSum
			st.LastEventAtMS = at.UnixMilli()
			return
		}
		if successSum > st.LastSuccessSum {
			delta := successSum - st.LastSuccessSum
			st.RequestsToday += delta
			st.RequestsTotal += delta
			st.SuccessEvents += delta
			if estimatePerSuccess > 0 {
				est := delta * estimatePerSuccess
				st.EstimatedToday += est
				st.EstimatedTotal += est
			}
			st.LastSuccessSum = successSum
		} else if successSum >= 0 && successSum < st.LastSuccessSum {
			st.LastSuccessSum = successSum
		}
		if failedSum > st.LastFailedSum {
			delta := failedSum - st.LastFailedSum
			st.RequestsToday += delta
			st.RequestsTotal += delta
			st.FailedEvents += delta
			st.LastFailedSum = failedSum
		} else if failedSum >= 0 && failedSum < st.LastFailedSum {
			st.LastFailedSum = failedSum
		}
		st.LastEventAtMS = at.UnixMilli()
	})
}

func (s *Store) ObserveFreeQuota(authIndex string, actual, limit int64, at time.Time) error {
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" || limit <= 0 {
		return nil
	}
	if actual < 0 {
		actual = 0
	}
	// Snapshot only. Free-usage actual/limit is a rolling window probe — do NOT add
	// deltas into calendar UsedToday/UsedTotal (those come only from usage.handle tokens).
	return s.mutateUsage(func(st *UsageStats) {
		day := DayKeyShanghai(at)
		if st.DayKey != day {
			st.DayKey = day
			st.UsedToday = 0
			st.RequestsToday = 0
			st.EstimatedToday = 0
			st.ZeroTokenSuccessToday = 0
		}
		st.QuotaByAuth[authIndex] = &AccountQuotaSnapshot{
			AuthIndex:   authIndex,
			Actual:      actual,
			Limit:       limit,
			UpdatedAtMS: at.UnixMilli(),
			Source:      "free-usage-exhausted",
		}
		st.LastEventAtMS = at.UnixMilli()
	})
}

// BuildMetricsView combines auth-file inventory + durable usage/quota snapshots.
// liveAuth: if non-nil, only snapshots for still-present credentials are counted.
// Prefer BuildMetricsViewOpts with enabledAuth for correct rolling pool.
func BuildMetricsView(xaiTotal, xaiEnabled, xaiDisabled int, st UsageStats) MetricsView {
	return BuildMetricsViewOpts(xaiTotal, xaiEnabled, xaiDisabled, st, false, nil, nil)
}

// BuildMetricsViewOpts:
//   - Daily total quota (QuotaTotalEst) = enabled xAI * DefaultFreeLimit (2M rolling 24h each).
//     Disabled credentials are NOT capacity; includeUnobservedEst=false → known enabled limits only.
//   - Used today/total = usage.handle real tokens only (no free-usage actual floor, no success×N).
//   - Rolling used/limit = free-usage snapshots for **enabled** accounts only (not disabled/deleted).
//   - actual is capped at limit when summing (Grok free-usage often reports actual>limit).
func BuildMetricsViewOpts(xaiTotal, xaiEnabled, xaiDisabled int, st UsageStats, includeUnobservedEst bool, liveAuth, enabledAuth map[string]bool) MetricsView {
	st = *EnsureUsageStats(&st)
	var usedKnown, limitKnown int64
	known := 0
	for k, q := range st.QuotaByAuth {
		if q == nil || q.Limit <= 0 {
			continue
		}
		// Prefer enabled set for rolling pool; fall back to live presence; else all snapshots.
		if enabledAuth != nil {
			if !enabledAuth[k] {
				continue
			}
		} else if liveAuth != nil {
			if _, ok := liveAuth[k]; !ok {
				continue
			}
		}
		known++
		// Cap actual at limit: exhausted bodies often show actual slightly over limit.
		act := q.Actual
		if act < 0 {
			act = 0
		}
		if act > q.Limit {
			act = q.Limit
		}
		usedKnown += act
		limitKnown += q.Limit
	}
	def := DefaultFreeLimit
	// Daily free-tier capacity: each ENABLED account ≈ 2M / rolling 24h.
	dailyCapEnabled := int64(xaiEnabled) * def
	unobservedEnabled := xaiEnabled - known
	if unobservedEnabled < 0 {
		unobservedEnabled = 0
	}
	var totalEst int64
	if includeUnobservedEst {
		totalEst = dailyCapEnabled
	} else {
		totalEst = limitKnown
	}
	if xaiEnabled == 0 {
		totalEst = 0
	}
	if totalEst > dailyCapEnabled && dailyCapEnabled > 0 {
		totalEst = dailyCapEnabled
	}

	// Real usage only — never floor with rolling actual (that mixes 24h window into "used total").
	usedTodayDisplay := st.UsedToday
	usedTotalDisplay := st.UsedTotal

	alert := st.ZeroTokenStreak >= ZeroTokenAlertThreshold
	alertMsg := ""
	if alert {
		alertMsg = "连续成功请求缺少 usage Detail token，可能 CPA 未发布用量明细；日历今日累计可能偏低。"
	}

	note := "仅xAI；日额度池=启用×2M(rolling 24h)；日历今日/累计=usage.handle 真实token(非快照)；滚动池=启用号 free-usage 快照(actual封顶limit)；禁用不计入。"
	return MetricsView{
		XAITotal:              xaiTotal,
		XAIEnabled:            xaiEnabled,
		XAIDisabled:           xaiDisabled,
		QuotaTotalEst:         totalEst,
		QuotaUsedKnown:        usedKnown,
		QuotaLimitKnown:       limitKnown,
		QuotaKnownAccounts:    known,
		QuotaTotalKnownOnly:   limitKnown,
		UnobservedAccounts:    unobservedEnabled,
		IncludeUnobservedEst:  includeUnobservedEst,
		UsedToday:             st.UsedToday,
		UsedTotal:             st.UsedTotal,
		UsedTodayDisplay:      usedTodayDisplay,
		UsedTotalDisplay:      usedTotalDisplay,
		RequestsToday:         st.RequestsToday,
		RequestsTotal:         st.RequestsTotal,
		EstimatedToday:        0,
		DefaultLimitPerAcct:   def,
		EstimatePerSuccess:    0,
		DayKey:                st.DayKey,
		RollingUsedKnown:      usedKnown,
		RollingLimitKnown:     limitKnown,
		RollingAccounts:       known,
		ZeroTokenSuccessToday: st.ZeroTokenSuccessToday,
		ZeroTokenStreak:       st.ZeroTokenStreak,
		DetailMissingAlert:    alert,
		DetailAlertMessage:    alertMsg,
		BackfillSource:        st.BackfillSource,
		BackfillAtMS:          st.BackfillAtMS,
		BackfillTokensFloor:   st.BackfillTokensFloor,
		Note:                  note,
	}
}

// PruneQuotaSnapshots removes free-usage snapshots for auth indexes no longer in liveAuth.
// Shrinks state after mass deletes; does not touch calendar UsedToday/UsedTotal.
func (s *Store) PruneQuotaSnapshots(liveAuth map[string]bool) (removed int, err error) {
	if liveAuth == nil {
		return 0, nil
	}
	err = s.mutateUsage(func(st *UsageStats) {
		for k := range st.QuotaByAuth {
			if _, ok := liveAuth[k]; !ok {
				delete(st.QuotaByAuth, k)
				removed++
			}
		}
	})
	return removed, err
}

// ApplyCalendarBackfill raises calendar-day used_today floor from an external source (e.g. CPAMP).
// Never decreases existing plugin counters. source is recorded on LastEvent only.
// ResetCalendarToday clears calendar-day counters (UsedToday/RequestsToday/EstimatedToday/zero-token)
// for the current Shanghai day. Does NOT clear UsedTotal/RequestsTotal or QuotaByAuth snapshots.
// Used when historical free-usage deltas polluted UsedToday before 0.2.18.
func (s *Store) ResetCalendarToday(at time.Time, note string) error {
	return s.mutateUsage(func(st *UsageStats) {
		day := DayKeyShanghai(at)
		st.DayKey = day
		st.UsedToday = 0
		st.RequestsToday = 0
		st.EstimatedToday = 0
		st.ZeroTokenSuccessToday = 0
		st.ZeroTokenStreak = 0
		for _, u := range st.UsageByAuth {
			if u == nil {
				continue
			}
			u.UsedToday = 0
			u.RequestsToday = 0
		}
		if note != "" {
			st.BackfillSource = "reset_today:" + note
			st.BackfillAtMS = at.UnixMilli()
			st.BackfillTokensFloor = 0
		}
		st.LastEventAtMS = at.UnixMilli()
	})
}

// SetCalendarTodayExact sets UsedToday/RequestsToday to exact values for current day (does not raise UsedTotal).
// Prefer Reset + natural accumulation; this is for operator correction after pollution.
func (s *Store) SetCalendarTodayExact(usedToday, requestsToday int64, source string, at time.Time) error {
	if usedToday < 0 {
		usedToday = 0
	}
	if requestsToday < 0 {
		requestsToday = 0
	}
	return s.mutateUsage(func(st *UsageStats) {
		day := DayKeyShanghai(at)
		if st.DayKey != day {
			st.DayKey = day
			// new day: totals unchanged
		}
		st.DayKey = day
		st.UsedToday = usedToday
		st.RequestsToday = requestsToday
		st.EstimatedToday = 0
		st.BackfillSource = source
		st.BackfillAtMS = at.UnixMilli()
		st.BackfillTokensFloor = usedToday
		st.LastEventAtMS = at.UnixMilli()
	})
}

func (s *Store) ApplyCalendarBackfill(dayKey string, usedTodayFloor, requestsTodayFloor int64, source string, at time.Time) (applied bool, err error) {
	if usedTodayFloor < 0 {
		usedTodayFloor = 0
	}
	if requestsTodayFloor < 0 {
		requestsTodayFloor = 0
	}
	var did bool
	err = s.mutateUsage(func(st *UsageStats) {
		day := DayKeyShanghai(at)
		if dayKey != "" && dayKey != day {
			// only backfill current calendar day
			return
		}
		if st.DayKey != day {
			st.DayKey = day
			st.UsedToday = 0
			st.RequestsToday = 0
			st.EstimatedToday = 0
			st.ZeroTokenSuccessToday = 0
		}
		if usedTodayFloor > st.UsedToday {
			delta := usedTodayFloor - st.UsedToday
			st.UsedToday = usedTodayFloor
			st.UsedTotal += delta
			did = true
		}
		if requestsTodayFloor > st.RequestsToday {
			delta := requestsTodayFloor - st.RequestsToday
			st.RequestsToday = requestsTodayFloor
			st.RequestsTotal += delta
			did = true
		}
		if did {
			st.LastEventAtMS = at.UnixMilli()
			if source != "" {
				st.BackfillSource = source
				st.BackfillAtMS = at.UnixMilli()
				st.BackfillTokensFloor = usedTodayFloor
			}
		}
	})
	return did, err
}
