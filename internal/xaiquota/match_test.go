package xaiquota

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestMatchXAIRateLimitWithRetryAfter(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	h := http.Header{}
	h.Set("Retry-After", "60")
	got, ok := MatchShortWindowQuota(MatchInput{
		Provider:        "xai",
		Failed:          true,
		StatusCode:      429,
		Body:            `{"error":{"code":"rate_limit_exceeded","message":"Rate limit reached for requests per minute","type":"tokens"}}`,
		ResponseHeaders: h,
		Now:             now,
		MaxResetSeconds: 86400,
	})
	if !ok {
		t.Fatal("expected match")
	}
	if !got.RecoverAt.Equal(now.Add(60 * time.Second)) {
		t.Fatalf("recover_at = %v", got.RecoverAt)
	}
}

func TestMatchXAIRateLimitWithBodyRetryAfter(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	got, ok := MatchShortWindowQuota(MatchInput{
		Provider:   "xai",
		Failed:     true,
		StatusCode: 429,
		Body:       `{"error":{"message":"Too many requests","type":"tokens","code":"rate_limit_exceeded","retry_after":120}}`,
		Now:        now,
	})
	if !ok {
		t.Fatal("expected match")
	}
	if !got.RecoverAt.Equal(now.Add(120 * time.Second)) {
		t.Fatalf("recover_at = %v", got.RecoverAt)
	}
}

func TestMatchIgnoresNonXAI(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	_, ok := MatchShortWindowQuota(MatchInput{
		Provider:   "codex",
		Failed:     true,
		StatusCode: 429,
		Body:       `{"error":{"type":"usage_limit_reached","resets_in_seconds":60}}`,
		Now:        now,
	})
	if ok {
		t.Fatal("codex must not match xAI plugin")
	}
}

func TestMatchIgnoresAuthErrors(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	_, ok := MatchShortWindowQuota(MatchInput{
		Provider:   "xai",
		Failed:     true,
		StatusCode: 401,
		Body:       `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error","code":"invalid_api_key"}}`,
		Now:        now,
	})
	if ok {
		t.Fatal("401 must be ignored")
	}
}

func TestMatchIgnoresInsufficientQuota(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	h := http.Header{}
	h.Set("Retry-After", "60")
	_, ok := MatchShortWindowQuota(MatchInput{
		Provider:        "xai",
		Failed:          true,
		StatusCode:      429,
		Body:            `{"error":{"message":"You exceeded your current quota, please check your plan and billing details","code":"insufficient_quota"}}`,
		ResponseHeaders: h,
		Now:             now,
	})
	if ok {
		t.Fatal("insufficient_quota must be ignored")
	}
}

func TestMatchRequiresResetTime(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	_, ok := MatchShortWindowQuota(MatchInput{
		Provider:   "xai",
		Failed:     true,
		StatusCode: 429,
		Body:       `{"error":{"code":"rate_limit_exceeded","message":"Rate limit reached for requests"}}`,
		Now:        now,
	})
	if ok {
		t.Fatal("missing reset time must not match")
	}
}

func TestMatchXAIHeaderReset(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	h := http.Header{}
	h.Set("x-ratelimit-remaining-requests", "0")
	h.Set("x-ratelimit-reset-requests", "90")
	got, ok := MatchShortWindowQuota(MatchInput{
		Provider:        "xai",
		Failed:          true,
		StatusCode:      429,
		Body:            `{"error":{"message":"Too Many Requests"}}`,
		ResponseHeaders: h,
		Now:             now,
	})
	if !ok {
		t.Fatal("expected header-based match")
	}
	if !got.RecoverAt.Equal(now.Add(90 * time.Second)) {
		t.Fatalf("recover_at = %v", got.RecoverAt)
	}
}

func TestMatchRejectsCodexUsageLimitOnXAIProvider(t *testing.T) {
	// Codex-style body must not be the primary xAI signal unless it also has
	// real xAI rate-limit fields/time. Pure usage_limit_reached is ignored.
	now := time.Unix(1_700_000_000, 0)
	_, ok := MatchShortWindowQuota(MatchInput{
		Provider:   "xai",
		Failed:     true,
		StatusCode: 429,
		Body:       `{"error":{"type":"usage_limit_reached","resets_in_seconds":30}}`,
		Now:        now,
	})
	if ok {
		t.Fatal("codex usage_limit_reached must not be treated as xAI short-window signal")
	}
}

func TestIsXAIProvider(t *testing.T) {
	if !IsXAIProvider("xai", "") {
		t.Fatal("xai provider")
	}
	if !IsXAIProvider("", "xai") {
		t.Fatal("auth_type fallback")
	}
	if IsXAIProvider("openai", "") {
		t.Fatal("openai must fail")
	}
	if IsXAIProvider("codex", "") {
		t.Fatal("codex must fail")
	}
}

func TestMatchGrokFreeUsageExhausted24h(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	body := `{"code":"subscription:free-usage-exhausted","error":"You've used all the included free usage for model grok-4.5-build-free for now. Usage resets over a rolling 24-hour window — tokens (actual/limit): 1091108/1000000. Upgrade to a Grok subscription for higher limits: https://grok.com/supergrok"}`
	h := http.Header{}
	h.Set("X-Should-Retry", "true")
	got, ok := MatchShortWindowQuota(MatchInput{
		Provider:        "xai",
		Failed:          true,
		StatusCode:      429,
		Body:            body,
		ResponseHeaders: h,
		Now:             now,
		MaxResetSeconds: 86400,
	})
	if !ok {
		t.Fatal("expected free-usage-exhausted match")
	}
	if !got.RecoverAt.Equal(now.Add(24 * time.Hour)) {
		t.Fatalf("recover_at = %v want +24h", got.RecoverAt)
	}
}

func TestMatchIgnoresPermissionDenied403(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	body := `{"code":"permission-denied","error":"Access to the chat endpoint is denied. Please ensure you're using the correct credentials."}`
	_, ok := MatchShortWindowQuota(MatchInput{
		Provider:   "xai",
		Failed:     true,
		StatusCode: 403,
		Body:       body,
		Now:        now,
	})
	if ok {
		t.Fatal("403 permission-denied must be ignored")
	}
	// even if status were 429 with permission body
	_, ok = MatchShortWindowQuota(MatchInput{
		Provider:   "xai",
		Failed:     true,
		StatusCode: 429,
		Body:       body,
		Now:        now,
	})
	if ok {
		t.Fatal("permission-denied body must be ignored")
	}
}
func TestIsInvalidCredentials(t *testing.T) {
	body := `{"error":"Invalid or expired credentials (auth_kind=bearer, x_xai_token_auth=xai-grok-cli, upstream=PermissionDenied, reason=no auth context)"}`
	if !IsInvalidCredentials(401, body) {
		t.Fatal("expected 401 invalid credentials match")
	}
	if IsInvalidCredentials(429, body) {
		t.Fatal("should not match non-401 without grant revoke")
	}
	if !IsInvalidCredentials(400, `{"error":"invalid_grant","error_description":"Refresh token has been revoked"}`) {
		t.Fatal("expected invalid_grant match")
	}
	if IsInvalidCredentials(401, `{"error":"something else"}`) {
		t.Fatal("should not match generic 401")
	}
}


func TestIsSpendingLimitBlocked(t *testing.T) {
	body := `{"code":"personal-team-blocked:spending-limit","error":"You have run out of credits or need a Grok subscription. Add credits at https://grok.com/?_s=usage or upgrade at https://grok.com/supergrok."}`
	if !IsSpendingLimitBlocked(402, body) {
		t.Fatal("expected 402 spending-limit match")
	}
	// 429 free-usage must not be classified as spending-limit
	if IsSpendingLimitBlocked(429, body) {
		// body is spending; status 429 alone is not accepted
	}
	if IsSpendingLimitBlocked(429, `{"code":"subscription:free-usage-exhausted"}`) {
		t.Fatal("must not match free-usage as spending-limit")
	}
	if IsSpendingLimitBlocked(402, `{"error":"something else"}`) {
		t.Fatal("generic 402 should not match without body signal")
	}
	// 403 should not be spending-limit (permission/region)
	if IsSpendingLimitBlocked(403, body) {
		t.Fatal("403 must not match spending-limit")
	}
}

func TestMatchSpendingLimitQuotaDistinctFrom429(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	body402 := `{"code":"personal-team-blocked:spending-limit","error":"You have run out of credits or need a Grok subscription."}`
	got, ok := MatchSpendingLimitQuota(MatchInput{
		Provider: "xai", Failed: true, StatusCode: 402, Body: body402, Now: now, MaxResetSeconds: 86400,
	})
	if !ok {
		t.Fatal("expected spending match")
	}
	if got.Signal != "spending_limit" {
		t.Fatalf("signal=%s", got.Signal)
	}
	if !strings.Contains(got.Reason, "spending-limit") && !strings.Contains(got.Reason, "积分") {
		t.Fatalf("reason=%s", got.Reason)
	}
	// 429 free-usage must use short-window path, not spending
	body429 := `{"code":"subscription:free-usage-exhausted","error":"You've used all the included free usage for model grok for now. Usage resets over a rolling 24-hour window — tokens (actual/limit): 100/1000000."}`
	if _, ok := MatchSpendingLimitQuota(MatchInput{Provider: "xai", Failed: true, StatusCode: 429, Body: body429, Now: now}); ok {
		t.Fatal("429 free-usage must not MatchSpendingLimitQuota")
	}
	sw, ok := MatchShortWindowQuota(MatchInput{Provider: "xai", Failed: true, StatusCode: 429, Body: body429, Now: now, MaxResetSeconds: 86400})
	if !ok {
		t.Fatal("429 free-usage should MatchShortWindowQuota")
	}
	if sw.Signal == "spending_limit" {
		t.Fatal("429 signal must not be spending_limit")
	}
}



func TestRegionPermissionDeniedNotDead(t *testing.T) {
	body := `{"code":"permission-denied","error":"The model grok-4.5 is not available in your region."}`
	if !IsModelRegionUnavailable(403, body) {
		t.Fatal("expected region unavailable")
	}
	if IsPermissionDenied(403, body) {
		t.Fatal("region model error must NOT be dead credential")
	}
	dead := `{"code":"permission-denied","error":"Access to the chat endpoint is denied. Please ensure you're using the correct credentials."}`
	if IsModelRegionUnavailable(403, dead) {
		t.Fatal("endpoint denied is not region")
	}
	if !IsPermissionDenied(403, dead) {
		t.Fatal("endpoint permission-denied must remain dead")
	}
}


func TestPlatformCapacityNotShortWindow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	body := `{"code":"resource-exhausted","error":"The model is currently at capacity due to high demand. Please try again later."}`
	// Even with Retry-After, capacity must never cool down accounts.
	h := http.Header{}
	h.Set("Retry-After", "30")
	if !IsPlatformCapacityExhausted(429, body) {
		t.Fatal("expected capacity match")
	}
	_, ok := MatchShortWindowQuota(MatchInput{
		Provider: "xai", Failed: true, StatusCode: 429, Body: body, ResponseHeaders: h, Now: now, MaxResetSeconds: 86400,
	})
	if ok {
		t.Fatal("platform capacity must not MatchShortWindowQuota")
	}
	// free-usage still matches
	bodyFree := `{"code":"subscription:free-usage-exhausted","error":"You've used all the included free usage for model grok for now. Usage resets over a rolling 24-hour window — tokens (actual/limit): 100/1000000."}`
	if IsPlatformCapacityExhausted(429, bodyFree) {
		t.Fatal("free-usage must not be classified as capacity")
	}
	got, ok := MatchShortWindowQuota(MatchInput{
		Provider: "xai", Failed: true, StatusCode: 429, Body: bodyFree, Now: now, MaxResetSeconds: 86400,
	})
	if !ok {
		t.Fatal("free-usage must still match short-window")
	}
	if got.RecoverAt.IsZero() {
		t.Fatal("free-usage recover_at required")
	}
}

func TestIsPlatformCapacityExhaustedVariants(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{`{"code":"resource-exhausted","error":"high demand"}`, true},
		{`{"error":"The model is currently at capacity"}`, true},
		{`{"code":"subscription:free-usage-exhausted","error":"free usage"}`, false},
		{`{"code":"rate_limit_exceeded","error":"Rate limit reached"}`, false},
		{``, false},
	}
	for _, c := range cases {
		if got := IsPlatformCapacityExhausted(429, c.body); got != c.want {
			t.Fatalf("body=%q got=%v want=%v", c.body, got, c.want)
		}
	}
}

