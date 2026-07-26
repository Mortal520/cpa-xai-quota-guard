package xaiquota

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMergeProbeHeaders_defaultsWhenEmpty(t *testing.T) {
	h := mergeProbeHeaders(nil)
	if h["x-grok-client-version"] != DefaultProbeCLIVersion {
		t.Fatalf("version=%q want %q", h["x-grok-client-version"], DefaultProbeCLIVersion)
	}
	if h["x-xai-token-auth"] != "xai-grok-cli" {
		t.Fatalf("token-auth=%q", h["x-xai-token-auth"])
	}
	if h["User-Agent"] == "" {
		t.Fatal("User-Agent empty")
	}
}

func TestMergeProbeHeaders_fileOverrides(t *testing.T) {
	h := mergeProbeHeaders(map[string]string{
		"x-grok-client-version": "9.9.9",
		"X-Custom":              "yes",
	})
	if h["x-grok-client-version"] != "9.9.9" {
		t.Fatalf("override version=%q", h["x-grok-client-version"])
	}
	if h["X-Custom"] != "yes" {
		t.Fatalf("custom missing: %#v", h)
	}
	// defaults still present
	if h["x-xai-token-auth"] != "xai-grok-cli" {
		t.Fatalf("default token-auth lost")
	}
}

func TestIsCLIVersionRejected(t *testing.T) {
	body := `{"error":"Your Grok CLI version (none) is outdated. Please update to version 0.1.202 or later"}`
	if !isCLIVersionRejected(426, body) {
		t.Fatal("426+outdated should reject")
	}
	if !isCLIVersionRejected(http.StatusUpgradeRequired, "x") {
		t.Fatal("426 alone should reject")
	}
	if isCLIVersionRejected(403, `{"code":"permission-denied"}`) {
		t.Fatal("403 permission should not be CLI reject")
	}
}

func TestDoProbePOSTReturnsRateLimitHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Ratelimit-Limit-Tokens", "1000000")
		w.Header().Set("X-Ratelimit-Remaining-Tokens", "100000")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	g := &Guard{}
	code, body, headers, err := g.doProbePOST(server.Client(), server.URL, "test", nil, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if code != http.StatusOK || body == "" {
		t.Fatalf("code=%d body=%q", code, body)
	}
	if headers.Get("X-Ratelimit-Limit-Tokens") != "1000000" {
		t.Fatalf("响应头未返回: %#v", headers)
	}
}

func TestProbeOneCredentialCoolsOnSuccessfulThresholdHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Ratelimit-Limit-Tokens", "1000000")
		w.Header().Set("X-Ratelimit-Remaining-Tokens", "50000")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	name := "threshold.json"
	raw := []byte(`{"access_token":"token","base_url":"` + server.URL + `"}`)
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	auth := newMemAuth(AuthFile{AuthIndex: "threshold", Name: name, Provider: "xai"})
	g, err := NewGuard(Config{
		Enabled: true, PatrolModel: "grok-4.5", QuotaUsageCooldownRatio: 0.90,
	}, auth, &memLog{})
	if err != nil {
		t.Fatal(err)
	}

	result := g.probeOneCredential(AuthFile{AuthIndex: "threshold", Name: name, Provider: "xai"}, dir, server.Client())
	if result.action != "cooldown" || result.httpCode != http.StatusOK {
		t.Fatalf("result=%+v", result)
	}
	files, _ := auth.List()
	if len(files) != 1 || !files[0].Disabled {
		t.Fatalf("凭证未按阈值停用: %+v", files)
	}
}

func TestProbeOneCredentialReenablesRecoveredPluginCooldown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	name := "recovered.json"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(`{"access_token":"token","base_url":"`+server.URL+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	auth := newMemAuth(AuthFile{AuthIndex: "recovered", Name: name, Provider: "xai", Disabled: true})
	g, err := NewGuard(Config{Enabled: true, PatrolModel: "grok-4.5", QuotaUsageCooldownRatio: 0.90}, auth, &memLog{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	_ = g.storeUpsert(AccountRecord{
		AuthIndex: "recovered", State: StateAutoDisabled, DisableSource: SourcePluginAuto,
		Owner: Owner, PreDisabled: false, Signal: "usage_ratio_threshold",
		DisabledAtMS: now.Add(-time.Hour).UnixMilli(), RecoverAtMS: now.Add(23 * time.Hour).UnixMilli(),
	})

	result := g.probeOneCredential(AuthFile{AuthIndex: "recovered", Name: name, Provider: "xai", Disabled: true}, dir, server.Client())
	if result.action != "reenabled" {
		t.Fatalf("result=%+v", result)
	}
	files, _ := auth.List()
	if len(files) != 1 || files[0].Disabled {
		t.Fatalf("恢复账号未启用: %+v", files)
	}
}

func TestProbeOneCredentialKeepsCooldownWhenStillAboveThreshold(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Ratelimit-Limit-Tokens", "1000000")
		w.Header().Set("X-Ratelimit-Remaining-Tokens", "50000")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	dir := t.TempDir()
	name := "still-hot.json"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(`{"access_token":"token","base_url":"`+server.URL+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	auth := newMemAuth(AuthFile{AuthIndex: "still-hot", Name: name, Provider: "xai", Disabled: true})
	g, err := NewGuard(Config{Enabled: true, PatrolModel: "grok-4.5", QuotaUsageCooldownRatio: 0.90}, auth, &memLog{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	originalRecoverAt := now.Add(23 * time.Hour).UnixMilli()
	_ = g.storeUpsert(AccountRecord{
		AuthIndex: "still-hot", State: StateAutoDisabled, DisableSource: SourcePluginAuto,
		Owner: Owner, PreDisabled: false, Signal: "body.error.code=subscription:free-usage-exhausted",
		DisabledAtMS: now.Add(-time.Hour).UnixMilli(), RecoverAtMS: originalRecoverAt,
	})

	result := g.probeOneCredential(AuthFile{AuthIndex: "still-hot", Name: name, Provider: "xai", Disabled: true}, dir, server.Client())
	if result.action != "cooldown" {
		t.Fatalf("result=%+v", result)
	}
	files, _ := auth.List()
	if len(files) != 1 || !files[0].Disabled {
		t.Fatalf("仍超阈值账号不应启用: %+v", files)
	}
	rec := g.storeGet("still-hot")
	if rec == nil || rec.RecoverAtMS != originalRecoverAt {
		t.Fatalf("复查不得重置恢复时间: %+v", rec)
	}
}
