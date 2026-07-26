package xaiquota

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestUsageEventFromRecordUsesFailureDetailsWhenFailedFlagIsFalse(t *testing.T) {
	record := pluginapi.UsageRecord{
		Provider: "xai", AuthIndex: "auth-429", Failed: false,
		Failure: pluginapi.UsageFailure{StatusCode: 429, Body: `{"code":"subscription:free-usage-exhausted"}`},
		Detail:  pluginapi.UsageDetail{TotalTokens: 42},
	}
	ev := usageEventFromRecord(record)
	if !ev.Failed || ev.StatusCode != 429 || ev.Body == "" {
		t.Fatalf("Failure 未被识别: %+v", ev)
	}
}

func TestDecodeUsageEventMergesRawFailureWhenTokensExist(t *testing.T) {
	payload := map[string]any{
		"Provider": "xai", "AuthIndex": "auth-raw-429", "Failed": false,
		"Detail":           map[string]any{"TotalTokens": 42},
		"failure":          map[string]any{"status_code": 429, "body": `{"code":"subscription:free-usage-exhausted"}`},
		"response_headers": map[string]any{"X-Ratelimit-Limit-Tokens": "1000000"},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	ev, ok := DecodeUsageEvent(raw)
	if !ok || !ev.Failed || ev.StatusCode != 429 || ev.Body == "" {
		t.Fatalf("raw Failure 未合并: %+v", ev)
	}
	if ev.TotalTokens != 42 || len(ev.ResponseHeaders) == 0 {
		t.Fatalf("typed token/raw headers 未保留: %+v", ev)
	}
}
