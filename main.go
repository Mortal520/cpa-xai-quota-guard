package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = host;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) {
		return 1;
	}
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {
		stored_host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"sync"
	"unsafe"

	"github.com/mortal/cpa-xai-quota-guard/internal/xaiquota"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	pluginID   = "cpa-xai-quota-guard"
	pluginVer  = "0.3.14"
	pluginAuth = "@mortal"
	pluginRepo = "https://github.com/mortal/cpa-xai-quota-guard"
	pluginLogo = ""
)

func main() {}

func init() {
	hostCall = cgoHostCall
}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	C.store_host_api(host)
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := handleMethod(C.GoString(method), requestBytes)
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, len C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
	_ = len
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	shutdownGuard()
}

func cgoHostCall(method string, request []byte) ([]byte, error) {
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var resp C.cliproxy_buffer
	var reqPtr *C.uint8_t
	var reqLen C.size_t
	if len(request) > 0 {
		reqPtr = (*C.uint8_t)(C.CBytes(request))
		defer C.free(unsafe.Pointer(reqPtr))
		reqLen = C.size_t(len(request))
	}
	code := C.call_host_api(cMethod, reqPtr, reqLen, &resp)
	if code != 0 {
		return nil, fmt.Errorf("host call %s code=%d", method, int(code))
	}
	if resp.ptr == nil || resp.len == 0 {
		return []byte(`{}`), nil
	}
	raw := C.GoBytes(resp.ptr, C.int(resp.len))
	C.free_host_buffer(resp.ptr, resp.len)
	return raw, nil
}

type envelope struct {
	OK     bool             `json:"ok"`
	Result json.RawMessage  `json:"result,omitempty"`
	Error  *envelopeError   `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func okEnvelope(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func okEnvelopeJSON(result string) ([]byte, error) {
	return json.Marshal(envelope{OK: true, Result: json.RawMessage(result)})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}

// ── global guard ─────────────────────────────────────────

var (
	guardOnce sync.Once
	guardInst *xaiquota.Guard
)

func guard() *xaiquota.Guard {
	guardOnce.Do(func() {
		cfg := configDefaults()
		g, err := xaiquota.NewGuard(cfg, dynamicAuth{}, hostLogger{})
		if err != nil {
			hostLog("error", "init guard failed: "+err.Error())
			g, _ = xaiquota.NewGuard(cfg, nil, hostLogger{})
		}
		guardInst = g
		g.StartTicker()
	})
	return guardInst
}

func shutdownGuard() {
	if guardInst != nil {
		guardInst.StopTicker()
	}
}

// dynamicAuth always uses the latest guard config for management calls.
type dynamicAuth struct{}

func (dynamicAuth) List() ([]xaiquota.AuthFile, error) {
	cfg := guard().Config()
	return newMgmtAuth(cfg).List()
}

func (dynamicAuth) SetDisabled(authIndex string, disabled bool) (bool, error) {
	cfg := guard().Config()
	return newMgmtAuth(cfg).SetDisabled(authIndex, disabled)
}

func (dynamicAuth) Delete(authIndex string) error {
	cfg := guard().Config()
	return newMgmtAuth(cfg).Delete(authIndex)
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		return okEnvelope(pluginRegistration(request))
	case pluginabi.MethodPluginShutdown:
		shutdownGuard()
		return okEnvelopeJSON("{}")
	case pluginabi.MethodUsageHandle:
		return handleUsageEvent(request)
	case pluginabi.MethodManagementRegister:
		return okEnvelope(buildManagementRegistration())
	case pluginabi.MethodManagementHandle:
		return handleManagement(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

type registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      pluginapi.Metadata       `json:"metadata"`
	Capabilities  registrationCapabilities `json:"capabilities"`
}

type registrationCapabilities struct {
	UsagePlugin   bool `json:"usage_plugin"`
	ManagementAPI bool `json:"management_api"`
}

func pluginRegistration(request []byte) registration {
	cfg := parseConfigFromReconfigure(request)
	g := guard()
	g.ApplyConfig(cfg)
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginID,
			Version:          pluginVer,
			Author:           pluginAuth,
			GitHubRepository: pluginRepo,
			Logo:             pluginLogo,
			ConfigFields:     configFields(),
		},
		Capabilities: registrationCapabilities{
			UsagePlugin:   true,
			ManagementAPI: true,
		},
	}
}


func handleUsageEvent(request []byte) ([]byte, error) {
	if len(request) == 0 {
		return okEnvelopeJSON("{}")
	}
	// Prefer typed decode first (exported field names from host json.Marshal).
	var record pluginapi.UsageRecord
	if err := json.Unmarshal(request, &record); err == nil {
		ev := usageEventFromRecord(record)
		// Fallback fill from raw map if typed detail is empty but raw has tokens.
		if ev.TotalTokens <= 0 && (ev.InputTokens+ev.OutputTokens+ev.ReasoningTokens) <= 0 {
			if rawEv, ok := usageEventFromRaw(request); ok {
				if rawEv.TotalTokens > 0 || rawEv.InputTokens > 0 || rawEv.OutputTokens > 0 {
					ev.InputTokens = rawEv.InputTokens
					ev.OutputTokens = rawEv.OutputTokens
					ev.ReasoningTokens = rawEv.ReasoningTokens
					ev.TotalTokens = rawEv.TotalTokens
				}
				if ev.Body == "" && rawEv.Body != "" {
					ev.Body = rawEv.Body
					ev.StatusCode = rawEv.StatusCode
					ev.Failed = rawEv.Failed
				}
				if ev.AuthIndex == "" {
					ev.AuthIndex = rawEv.AuthIndex
				}
				if ev.Provider == "" {
					ev.Provider = rawEv.Provider
				}
				if ev.AuthType == "" {
					ev.AuthType = rawEv.AuthType
				}
			}
		}
		guard().HandleUsage(ev)
		return okEnvelopeJSON("{}")
	}
	// Typed decode failed: raw path.
	if ev, ok := usageEventFromRaw(request); ok {
		guard().HandleUsage(ev)
		return okEnvelopeJSON("{}")
	}
	return errorEnvelope("decode_usage", "invalid usage payload"), nil
}

func usageEventFromRecord(r pluginapi.UsageRecord) xaiquota.UsageEvent {
	body := ""
	status := 0
	if r.Failed {
		body = r.Failure.Body
		status = r.Failure.StatusCode
	}
	var headers map[string][]string
	if r.ResponseHeaders != nil {
		headers = map[string][]string(r.ResponseHeaders)
	}
	total := r.Detail.TotalTokens
	if total <= 0 {
		total = r.Detail.InputTokens + r.Detail.OutputTokens + r.Detail.ReasoningTokens
	}
	return xaiquota.UsageEvent{
		AuthIndex:       r.AuthIndex,
		Provider:        r.Provider,
		AuthType:        r.AuthType,
		Account:         "",
		Failed:          r.Failed,
		StatusCode:      status,
		Body:            body,
		ResponseHeaders: headers,
		InputTokens:     r.Detail.InputTokens,
		OutputTokens:    r.Detail.OutputTokens,
		ReasoningTokens: r.Detail.ReasoningTokens,
		TotalTokens:     total,
	}
}

func usageEventFromRaw(request []byte) (xaiquota.UsageEvent, bool) {
	var raw map[string]any
	if err := json.Unmarshal(request, &raw); err != nil || raw == nil {
		return xaiquota.UsageEvent{}, false
	}
	getS := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := raw[k]; ok {
				switch t := v.(type) {
				case string:
					if t != "" {
						return t
					}
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
		if m == nil {
			return 0
		}
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
					fmt.Sscan(t, &n)
					return n
				}
			}
		}
		return 0
	}
	failed := getB("Failed", "failed")
	status := 0
	body := ""
	// Failure may be nested object
	if f, ok := raw["Failure"].(map[string]any); ok {
		status = int(getI(f, "StatusCode", "status_code", "statusCode"))
		if s, ok := f["Body"].(string); ok {
			body = s
		} else if s, ok := f["body"].(string); ok {
			body = s
		}
	}
	if f, ok := raw["failure"].(map[string]any); ok {
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
	reaTok := getI(detail, "ReasoningTokens", "reasoning_tokens", "reasoningTokens")
	total := getI(detail, "TotalTokens", "total_tokens", "totalTokens")
	if total <= 0 {
		total = inTok + outTok + reaTok
	}
	// top-level token fallbacks
	if total <= 0 {
		total = getI(raw, "TotalTokens", "total_tokens", "totalTokens")
	}
	headers := map[string][]string{}
	if h, ok := raw["ResponseHeaders"].(map[string]any); ok {
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
	return xaiquota.UsageEvent{
		AuthIndex:       getS("AuthIndex", "auth_index", "authIndex"),
		Provider:        getS("Provider", "provider"),
		AuthType:        getS("AuthType", "auth_type", "authType"),
		Failed:          failed || status >= 400 || body != "",
		StatusCode:      status,
		Body:            body,
		ResponseHeaders: headers,
		InputTokens:     inTok,
		OutputTokens:    outTok,
		ReasoningTokens: reaTok,
		TotalTokens:     total,
	}, true
}
