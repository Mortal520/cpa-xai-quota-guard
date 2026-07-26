package xaiquota

import (
	"encoding/json"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// DecodeUsageEvent decodes host usage records while preserving raw failure
// fields emitted by older or differently-tagged CPA builds.
func DecodeUsageEvent(request []byte) (UsageEvent, bool) {
	var record pluginapi.UsageRecord
	if err := json.Unmarshal(request, &record); err == nil {
		ev := usageEventFromRecord(record)
		if rawEv, ok := usageEventFromRaw(request); ok {
			mergeUsageEvent(&ev, rawEv)
		}
		return ev, true
	}
	return usageEventFromRaw(request)
}

func mergeUsageEvent(dst *UsageEvent, src UsageEvent) {
	if dst == nil {
		return
	}
	if src.Failed {
		dst.Failed = true
		if src.StatusCode > 0 {
			dst.StatusCode = src.StatusCode
		}
		if src.Body != "" {
			dst.Body = src.Body
		}
	}
	if dst.AuthIndex == "" {
		dst.AuthIndex = src.AuthIndex
	}
	if dst.Provider == "" {
		dst.Provider = src.Provider
	}
	if dst.AuthType == "" {
		dst.AuthType = src.AuthType
	}
	if dst.InputTokens <= 0 {
		dst.InputTokens = src.InputTokens
	}
	if dst.OutputTokens <= 0 {
		dst.OutputTokens = src.OutputTokens
	}
	if dst.ReasoningTokens <= 0 {
		dst.ReasoningTokens = src.ReasoningTokens
	}
	if dst.TotalTokens <= 0 {
		dst.TotalTokens = src.TotalTokens
	}
	if len(dst.ResponseHeaders) == 0 && len(src.ResponseHeaders) > 0 {
		dst.ResponseHeaders = src.ResponseHeaders
	}
}

func usageEventFromRecord(r pluginapi.UsageRecord) UsageEvent {
	body := r.Failure.Body
	status := r.Failure.StatusCode
	failed := r.Failed || status >= 400 || body != ""
	var headers map[string][]string
	if r.ResponseHeaders != nil {
		headers = map[string][]string(r.ResponseHeaders)
	}
	total := r.Detail.TotalTokens
	if total <= 0 {
		total = r.Detail.InputTokens + r.Detail.OutputTokens + r.Detail.ReasoningTokens
	}
	return UsageEvent{
		AuthIndex:       r.AuthIndex,
		Provider:        r.Provider,
		AuthType:        r.AuthType,
		Failed:          failed,
		StatusCode:      status,
		Body:            body,
		ResponseHeaders: headers,
		InputTokens:     r.Detail.InputTokens,
		OutputTokens:    r.Detail.OutputTokens,
		ReasoningTokens: r.Detail.ReasoningTokens,
		TotalTokens:     total,
	}
}

func usageEventFromRaw(request []byte) (UsageEvent, bool) {
	var raw map[string]any
	if err := json.Unmarshal(request, &raw); err != nil || raw == nil {
		return UsageEvent{}, false
	}
	getS := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := raw[k]; ok {
				if s, ok := v.(string); ok && s != "" {
					return s
				}
			}
		}
		return ""
	}
	getB := func(keys ...string) bool {
		for _, k := range keys {
			if v, ok := raw[k]; ok {
				switch t := v.(type) {
				case bool:
					return t
				case float64:
					return t != 0
				}
			}
		}
		return false
	}
	getI := func(m map[string]any, keys ...string) int64 {
		for _, k := range keys {
			if v, ok := m[k]; ok {
				switch t := v.(type) {
				case float64:
					return int64(t)
				case int64:
					return t
				case json.Number:
					n, _ := t.Int64()
					return n
				case string:
					var n int64
					_, _ = fmt.Sscan(t, &n)
					return n
				}
			}
		}
		return 0
	}

	failed := getB("Failed", "failed")
	status := 0
	body := ""
	for _, key := range []string{"Failure", "failure"} {
		f, ok := raw[key].(map[string]any)
		if !ok {
			continue
		}
		if status == 0 {
			status = int(getI(f, "StatusCode", "status_code", "statusCode"))
		}
		if body == "" {
			if s, ok := f["Body"].(string); ok {
				body = s
			} else if s, ok := f["body"].(string); ok {
				body = s
			}
		}
	}
	detail := map[string]any{}
	if d, ok := raw["Detail"].(map[string]any); ok {
		detail = d
	} else if d, ok := raw["detail"].(map[string]any); ok {
		detail = d
	}
	inTok := getI(detail, "InputTokens", "input_tokens", "inputTokens", "PromptTokens", "prompt_tokens")
	outTok := getI(detail, "OutputTokens", "output_tokens", "outputTokens", "CompletionTokens", "completion_tokens")
	reasoningTok := getI(detail, "ReasoningTokens", "reasoning_tokens", "reasoningTokens")
	total := getI(detail, "TotalTokens", "total_tokens", "totalTokens")
	if total <= 0 {
		total = inTok + outTok + reasoningTok
	}
	if total <= 0 {
		total = getI(raw, "TotalTokens", "total_tokens", "totalTokens")
	}

	headers := map[string][]string{}
	for _, key := range []string{"ResponseHeaders", "response_headers", "responseHeaders"} {
		h, ok := raw[key].(map[string]any)
		if !ok {
			continue
		}
		for k, v := range h {
			switch t := v.(type) {
			case []any:
				arr := make([]string, 0, len(t))
				for _, x := range t {
					arr = append(arr, fmt.Sprint(x))
				}
				headers[k] = arr
			case []string:
				headers[k] = t
			case string:
				headers[k] = []string{t}
			}
		}
	}
	return UsageEvent{
		AuthIndex:       getS("AuthIndex", "auth_index", "authIndex"),
		Provider:        getS("Provider", "provider"),
		AuthType:        getS("AuthType", "auth_type", "authType"),
		Failed:          failed || status >= 400 || body != "",
		StatusCode:      status,
		Body:            body,
		ResponseHeaders: headers,
		InputTokens:     inTok,
		OutputTokens:    outTok,
		ReasoningTokens: reasoningTok,
		TotalTokens:     total,
	}, true
}
