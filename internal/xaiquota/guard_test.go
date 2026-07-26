package xaiquota

import (
	"net/http"
	"sync"
	"testing"
	"time"
)

type memAuth struct {
	mu    sync.Mutex
	files map[string]AuthFile
}

func newMemAuth(files ...AuthFile) *memAuth {
	m := &memAuth{files: map[string]AuthFile{}}
	for _, f := range files {
		m.files[f.AuthIndex] = f
	}
	return m
}

func (m *memAuth) List() ([]AuthFile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]AuthFile, 0, len(m.files))
	for _, f := range m.files {
		out = append(out, f)
	}
	return out, nil
}

func (m *memAuth) SetDisabled(authIndex string, disabled bool) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.files[authIndex]
	if !ok {
		return false, nil
	}
	prev := f.Disabled
	f.Disabled = disabled
	m.files[authIndex] = f
	return prev, nil
}

func (m *memAuth) Delete(authIndex string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.files, authIndex)
	return nil
}

type memLog struct {
	mu   sync.Mutex
	msgs []string
}

func (l *memLog) Log(level, message string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.msgs = append(l.msgs, level+":"+message)
}

func TestGuardDisableAndRecoverPluginAuto(t *testing.T) {
	auth := newMemAuth(AuthFile{AuthIndex: "x1", Name: "xai-1.json", Provider: "xai", Disabled: false})
	logger := &memLog{}
	g, err := NewGuard(Config{
		Enabled:         true,
		TickSeconds:     1,
		ManagementURL:   "http://127.0.0.1:8317",
		ManagementKey:   "test",
		StatePath:       "",
		MaxResetSeconds: 86400,
	}, auth, logger)
	if err != nil {
		t.Fatal(err)
	}
	g.HandleUsage(UsageEvent{
		AuthIndex:  "x1",
		Provider:   "xai",
		Failed:     true,
		StatusCode: 429,
		Body:       `{"error":{"code":"rate_limit_exceeded","message":"Rate limit reached for requests per minute","type":"tokens","retry_after":1}}`,
	})
	// force recover_at to past
	rec := g.storeGet("x1")
	if rec == nil || rec.State != StateAutoDisabled || rec.DisableSource != SourcePluginAuto {
		t.Fatalf("expected auto_disabled, got %#v", rec)
	}
	rec.RecoverAtMS = time.Now().Add(-time.Second).UnixMilli()
	_ = g.storeUpsert(*rec)

	g.Tick()
	files, _ := auth.List()
	if files[0].Disabled {
		t.Fatal("expected re-enabled")
	}
	after := g.storeGet("x1")
	if after != nil && after.State != StateActive {
		// MarkActive keeps record but state active; Get still returns it
		if after.DisableSource != SourceNone {
			t.Fatalf("after recover %#v", after)
		}
	}
}

func TestGuardNeverRecoversUserManual(t *testing.T) {
	auth := newMemAuth(AuthFile{AuthIndex: "x2", Name: "xai-2.json", Provider: "xai", Disabled: true})
	g, err := NewGuard(Config{
		Enabled:       true,
		ManagementURL: "http://127.0.0.1:8317",
		ManagementKey: "test",
	}, auth, &memLog{})
	if err != nil {
		t.Fatal(err)
	}
	g.HandleUsage(UsageEvent{
		AuthIndex:  "x2",
		Provider:   "xai",
		Failed:     true,
		StatusCode: 429,
		Body:       `{"error":{"code":"rate_limit_exceeded","message":"Rate limit","type":"tokens","retry_after":1}}`,
	})
	rec := g.storeGet("x2")
	if rec == nil || rec.DisableSource != SourceUserManual {
		t.Fatalf("expected user_manual, got %#v", rec)
	}
	// Force due
	rec.RecoverAtMS = time.Now().Add(-time.Second).UnixMilli()
	rec.State = StateUserManualDisabled
	_ = g.storeUpsert(*rec)
	g.Tick()
	files, _ := auth.List()
	if !files[0].Disabled {
		t.Fatal("user_manual must remain disabled")
	}
}

func TestGuardSkipsParseFailure(t *testing.T) {
	auth := newMemAuth(AuthFile{AuthIndex: "x3", Name: "xai-3.json", Provider: "xai", Disabled: false})
	g, err := NewGuard(Config{
		Enabled:       true,
		ManagementURL: "http://127.0.0.1:8317",
		ManagementKey: "test",
	}, auth, &memLog{})
	if err != nil {
		t.Fatal(err)
	}
	g.HandleUsage(UsageEvent{
		AuthIndex:  "x3",
		Provider:   "xai",
		Failed:     true,
		StatusCode: 429,
		Body:       `{"error":{"code":"rate_limit_exceeded","message":"Rate limit"}}`, // no reset time
	})
	files, _ := auth.List()
	if files[0].Disabled {
		t.Fatal("must not disable when reset parse fails")
	}
}

func TestGuardDeletesPermissionDenied(t *testing.T) {
	auth := newMemAuth(AuthFile{AuthIndex: "x403", Name: "xai-403.json", Provider: "xai", Disabled: false})
	g, err := NewGuard(Config{
		Enabled:       true,
		ManagementURL: "http://127.0.0.1:8317",
		ManagementKey: "test",
	}, auth, &memLog{})
	if err != nil {
		t.Fatal(err)
	}
	g.HandleUsage(UsageEvent{
		AuthIndex:  "x403",
		Provider:   "xai",
		Failed:     true,
		StatusCode: 403,
		Body:       `{"code":"permission-denied","error":"Access to the chat endpoint is denied."}`,
	})
	files, _ := auth.List()
	if len(files) != 0 {
		t.Fatalf("expected deleted, still have %#v", files)
	}
}
func TestGuardRecordsSuccessUsageTokens(t *testing.T) {
	auth := newMemAuth(AuthFile{AuthIndex: "xs", Name: "xai-s.json", Provider: "xai", Disabled: false})
	g, err := NewGuard(Config{
		Enabled:       true,
		ManagementURL: "http://127.0.0.1:8317",
		ManagementKey: "test",
		StatePath:     "",
	}, auth, &memLog{})
	if err != nil {
		t.Fatal(err)
	}
	g.HandleUsage(UsageEvent{
		AuthIndex:    "xs",
		Provider:     "xai",
		Failed:       false,
		StatusCode:   200,
		InputTokens:  50000,
		OutputTokens: 800,
		TotalTokens:  50800,
	})
	st := g.store.GetUsageStats()
	if st.UsedToday != 50800 || st.RequestsToday != 1 || st.SuccessEvents != 1 {
		t.Fatalf("success metrics not recorded: %+v", st)
	}
	// non-xai ignored
	g.HandleUsage(UsageEvent{Provider: "codex", Failed: false, TotalTokens: 9999})
	st = g.store.GetUsageStats()
	if st.UsedToday != 50800 {
		t.Fatalf("non-xai leaked into today: %+v", st)
	}
	// disabled plugin ignores (mutate config in-place to keep same store)
	g.mu.Lock()
	g.cfg.Enabled = false
	g.mu.Unlock()
	g.HandleUsage(UsageEvent{Provider: "xai", Failed: false, TotalTokens: 1000})
	st = g.store.GetUsageStats()
	if st.UsedToday != 50800 {
		t.Fatalf("disabled still counted: %+v", st)
	}
}

func TestGuardCoolsSuccessfulAccountAtUsageRatioThreshold(t *testing.T) {
	auth := newMemAuth(AuthFile{AuthIndex: "ratio-90", Name: "ratio-90.json", Provider: "xai"})
	g, err := NewGuard(Config{
		Enabled:                 true,
		QuotaUsageCooldownRatio: 0.90,
	}, auth, &memLog{})
	if err != nil {
		t.Fatal(err)
	}

	g.HandleUsage(UsageEvent{
		AuthIndex:  "ratio-90",
		Provider:   "xai",
		StatusCode: http.StatusOK,
		ResponseHeaders: map[string][]string{
			"X-Ratelimit-Limit-Tokens":     {"1000000"},
			"X-Ratelimit-Remaining-Tokens": {"100000"},
		},
	})

	files, _ := auth.List()
	if len(files) != 1 || !files[0].Disabled {
		t.Fatalf("达到 90%% 阈值后应停用: %+v", files)
	}
	rec := g.storeGet("ratio-90")
	if rec == nil || rec.Signal != "usage_ratio_threshold" || rec.State != StateAutoDisabled {
		t.Fatalf("阈值冷却状态错误: %+v", rec)
	}
	remain := time.Until(time.UnixMilli(rec.RecoverAtMS))
	if remain < 23*time.Hour || remain > 24*time.Hour+time.Minute {
		t.Fatalf("恢复时间应约为 24h，实际 %v", remain)
	}
}

func TestGuardCapacityErrorNeverUsesRatioThreshold(t *testing.T) {
	auth := newMemAuth(AuthFile{AuthIndex: "capacity", Name: "capacity.json", Provider: "xai"})
	g, err := NewGuard(Config{Enabled: true, QuotaUsageCooldownRatio: 0.80}, auth, &memLog{})
	if err != nil {
		t.Fatal(err)
	}
	g.HandleUsage(UsageEvent{
		AuthIndex:  "capacity",
		Provider:   "xai",
		Failed:     true,
		StatusCode: http.StatusTooManyRequests,
		Body:       `{"code":"resource-exhausted","error":"The model is currently at capacity due to high demand"}`,
		ResponseHeaders: map[string][]string{
			"X-Ratelimit-Limit-Tokens":     {"1000000"},
			"X-Ratelimit-Remaining-Tokens": {"0"},
		},
	})
	files, _ := auth.List()
	if len(files) != 1 || files[0].Disabled {
		t.Fatalf("平台高负载不得触发阈值冷却: %+v", files)
	}
}

func TestGuardUsageRatioThresholdDoesNotRestartCooldown(t *testing.T) {
	auth := newMemAuth(AuthFile{AuthIndex: "ratio-repeat", Name: "ratio-repeat.json", Provider: "xai"})
	g, err := NewGuard(Config{Enabled: true, QuotaUsageCooldownRatio: 0.90}, auth, &memLog{})
	if err != nil {
		t.Fatal(err)
	}
	ev := UsageEvent{
		AuthIndex: "ratio-repeat", Provider: "xai", StatusCode: http.StatusOK,
		ResponseHeaders: map[string][]string{
			"X-Ratelimit-Limit-Tokens": {"1000000"}, "X-Ratelimit-Remaining-Tokens": {"0"},
		},
	}
	g.HandleUsage(ev)
	first := g.storeGet("ratio-repeat")
	if first == nil {
		t.Fatal("首次阈值冷却未落盘")
	}
	firstRecoverAt := first.RecoverAtMS
	time.Sleep(2 * time.Millisecond)
	g.HandleUsage(ev)
	second := g.storeGet("ratio-repeat")
	if second == nil || second.RecoverAtMS != firstRecoverAt {
		t.Fatalf("重复命中不得后推 24h: first=%d second=%+v", firstRecoverAt, second)
	}
}

func TestGuardUsageRatioThresholdRespectsManualDisable(t *testing.T) {
	auth := newMemAuth(AuthFile{AuthIndex: "ratio-manual", Name: "ratio-manual.json", Provider: "xai", Disabled: true})
	g, err := NewGuard(Config{Enabled: true, QuotaUsageCooldownRatio: 0.90}, auth, &memLog{})
	if err != nil {
		t.Fatal(err)
	}
	g.HandleUsage(UsageEvent{
		AuthIndex: "ratio-manual", Provider: "xai", StatusCode: http.StatusOK,
		ResponseHeaders: map[string][]string{
			"X-Ratelimit-Limit-Tokens": {"1000000"}, "X-Ratelimit-Remaining-Tokens": {"0"},
		},
	})
	rec := g.storeGet("ratio-manual")
	if rec == nil || rec.State != StateUserManualDisabled || rec.DisableSource != SourceUserManual {
		t.Fatalf("手动停用不得被阈值冷却夺权: %+v", rec)
	}
}
func TestGuardDeletesInvalidCredentials401(t *testing.T) {
	auth := newMemAuth(AuthFile{AuthIndex: "x401", Name: "xai-401.json", Provider: "xai", Disabled: false})
	g, err := NewGuard(Config{
		Enabled:       true,
		ManagementURL: "http://127.0.0.1:8317",
		ManagementKey: "test",
	}, auth, &memLog{})
	if err != nil {
		t.Fatal(err)
	}
	g.HandleUsage(UsageEvent{
		AuthIndex:  "x401",
		Provider:   "xai",
		Failed:     true,
		StatusCode: 401,
		Body:       `{"error":"Invalid or expired credentials (auth_kind=bearer, x_xai_token_auth=xai-grok-cli, upstream=PermissionDenied, reason=no auth context)"}`,
	})
	files, _ := auth.List()
	if len(files) != 0 {
		t.Fatalf("expected deleted, still have %#v", files)
	}
}

func TestGuardCooldownsSpendingLimit402(t *testing.T) {
	auth := newMemAuth(AuthFile{AuthIndex: "x402", Name: "xai-402.json", Provider: "xai", Disabled: false})
	g, err := NewGuard(Config{
		Enabled:                  true,
		ManagementURL:            "http://127.0.0.1:8317",
		ManagementKey:            "test",
		SpendingConfirmHits:      3,
		SpendingConfirmWindowSec: 900,
	}, auth, &memLog{})
	if err != nil {
		t.Fatal(err)
	}
	ev := UsageEvent{
		AuthIndex:  "x402",
		Provider:   "xai",
		Failed:     true,
		StatusCode: 402,
		Body:       `{"code":"personal-team-blocked:spending-limit","error":"You have run out of credits or need a Grok subscription."}`,
	}
	// First two hits: confirm only, not yet disabled.
	for i := 0; i < 2; i++ {
		g.HandleUsage(ev)
		files, _ := auth.List()
		if files[0].Disabled {
			t.Fatalf("hit %d: should not disable yet", i+1)
		}
		rec := g.storeGet("x402")
		if rec == nil || rec.SpendingSuspectHits != i+1 {
			t.Fatalf("hit %d suspect hits=%v", i+1, rec)
		}
	}
	// Third hit: design path — soft cooldown.
	g.HandleUsage(ev)
	files, _ := auth.List()
	if len(files) != 1 {
		t.Fatalf("expected keep file (cooldown), got %#v", files)
	}
	if !files[0].Disabled {
		t.Fatal("expected plugin_auto disabled for 402 spending-limit after confirm")
	}
	rec := g.storeGet("x402")
	if rec == nil || rec.State != StateAutoDisabled || rec.DisableSource != SourcePluginAuto {
		t.Fatalf("expected auto_disabled plugin_auto, got %#v", rec)
	}
	if rec.Signal != "spending_limit" {
		t.Fatalf("signal=%q want spending_limit", rec.Signal)
	}
	if rec.SpendingSuspectHits != 0 {
		t.Fatalf("suspect should clear after cooldown, hits=%d", rec.SpendingSuspectHits)
	}
}

func TestGuardSpending402ClearedBySuccess(t *testing.T) {
	auth := newMemAuth(AuthFile{AuthIndex: "x402b", Name: "xai-402b.json", Provider: "xai", Disabled: false})
	g, err := NewGuard(Config{
		Enabled: true, ManagementURL: "http://127.0.0.1:8317", ManagementKey: "test",
		SpendingConfirmHits: 3, SpendingConfirmWindowSec: 900,
	}, auth, &memLog{})
	if err != nil {
		t.Fatal(err)
	}
	ev := UsageEvent{AuthIndex: "x402b", Provider: "xai", Failed: true, StatusCode: 402,
		Body: `{"code":"personal-team-blocked:spending-limit","error":"run out of credits"}`}
	g.HandleUsage(ev)
	g.HandleUsage(UsageEvent{AuthIndex: "x402b", Provider: "xai", Failed: false, StatusCode: 200})
	rec := g.storeGet("x402b")
	if rec != nil && rec.SpendingSuspectHits != 0 {
		t.Fatalf("success should clear suspect, got %#v", rec)
	}
	// new 402 after success starts at 1
	g.HandleUsage(ev)
	rec = g.storeGet("x402b")
	if rec == nil || rec.SpendingSuspectHits != 1 {
		t.Fatalf("want hits=1 after clear, got %#v", rec)
	}
	files, _ := auth.List()
	if files[0].Disabled {
		t.Fatal("must not disable on first post-success 402")
	}
}

// staleListAuth returns wrong Disabled=true→false on List, but real state in mem for SetDisabled.
// Simulates TTL/sticky cache that caused Tick to MarkActive without enabling.
type staleListAuth struct {
	real          *memAuth
	listDisabled  map[string]bool // override Disabled for List only
	setCalls      int
	lastSetEnable bool
}

func (s *staleListAuth) List() ([]AuthFile, error) {
	files, err := s.real.List()
	if err != nil {
		return nil, err
	}
	out := make([]AuthFile, len(files))
	copy(out, files)
	for i := range out {
		if v, ok := s.listDisabled[out[i].AuthIndex]; ok {
			out[i].Disabled = v
		}
	}
	return out, nil
}

func (s *staleListAuth) SetDisabled(authIndex string, disabled bool) (bool, error) {
	s.setCalls++
	s.lastSetEnable = !disabled
	return s.real.SetDisabled(authIndex, disabled)
}

func (s *staleListAuth) Delete(authIndex string) error { return s.real.Delete(authIndex) }

func TestTickAlwaysCallsSetDisabledEvenIfListSaysEnabled(t *testing.T) {
	real := newMemAuth(AuthFile{AuthIndex: "stale1", Name: "xai-stale.json", Provider: "xai", Disabled: true})
	stale := &staleListAuth{real: real, listDisabled: map[string]bool{"stale1": false}} // List lies: enabled
	g, err := NewGuard(Config{Enabled: true, ManagementURL: "http://127.0.0.1:8317", ManagementKey: "test"}, stale, &memLog{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	_ = g.storeUpsert(AccountRecord{
		AuthIndex: "stale1", FileName: "xai-stale.json", Provider: "xai",
		DisableSource: SourcePluginAuto, State: StateAutoDisabled, Owner: Owner,
		DisabledAtMS: now.Add(-2 * time.Hour).UnixMilli(),
		RecoverAtMS:  now.Add(-time.Minute).UnixMilli(),
		Signal:       "free_usage",
	})
	g.Tick()
	if stale.setCalls < 1 {
		t.Fatal("Tick must call SetDisabled even when List reports enabled")
	}
	files, _ := real.List()
	if files[0].Disabled {
		t.Fatal("expected real re-enable via SetDisabled")
	}
	rec := g.storeGet("stale1")
	if rec != nil && rec.State == StateAutoDisabled {
		t.Fatalf("expected cleared cooldown, got %#v", rec)
	}
}

func TestTickHealsOrphanActiveWhileCPADisabled(t *testing.T) {
	auth := newMemAuth(AuthFile{AuthIndex: "orph1", Name: "xai-orph.json", Provider: "xai", Disabled: true})
	g, err := NewGuard(Config{Enabled: true, ManagementURL: "http://127.0.0.1:8317", ManagementKey: "test"}, auth, &memLog{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	// Ownership cleared (Active) but last_cooldown fingerprint remains; CPA still disabled.
	_ = g.storeUpsert(AccountRecord{
		AuthIndex: "orph1", FileName: "xai-orph.json", Provider: "xai",
		DisableSource: SourceNone, State: StateActive,
		LastCooldownSignal: "free_usage", LastCooldownReason: "quota",
		LastCooldownDisabledAtMS: now.Add(-25 * time.Hour).UnixMilli(),
		LastCooldownRecoverAtMS:  now.Add(-time.Hour).UnixMilli(),
	})
	g.Tick()
	files, _ := auth.List()
	if files[0].Disabled {
		t.Fatal("orphan due cooldown should be re-enabled")
	}
}

func TestTickRestoresLostPluginAutoBeforeDue(t *testing.T) {
	auth := newMemAuth(AuthFile{AuthIndex: "lost1", Name: "xai-lost.json", Provider: "xai", Disabled: true})
	g, err := NewGuard(Config{Enabled: true, ManagementURL: "http://127.0.0.1:8317", ManagementKey: "test"}, auth, &memLog{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	_ = g.storeUpsert(AccountRecord{
		AuthIndex: "lost1", FileName: "xai-lost.json", Provider: "xai",
		DisableSource: SourceNone, State: StateActive,
		LastCooldownSignal:       "spending_limit",
		LastCooldownDisabledAtMS: now.Add(-1 * time.Hour).UnixMilli(),
		LastCooldownRecoverAtMS:  now.Add(2 * time.Hour).UnixMilli(),
	})
	g.Tick()
	files, _ := auth.List()
	if !files[0].Disabled {
		t.Fatal("must not enable before recover_at")
	}
	rec := g.storeGet("lost1")
	if rec == nil || rec.State != StateAutoDisabled || rec.DisableSource != SourcePluginAuto || rec.Owner != Owner {
		t.Fatalf("expected restored plugin_auto ownership, got %#v", rec)
	}
}
