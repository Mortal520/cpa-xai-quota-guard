package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/mortal/cpa-xai-quota-guard/internal/xaiquota"
)

// managementRequest mirrors the request the host delivers to management.handle.
// Host may use either PascalCase (Method/Path/Body) or lowercase.
type managementRequest struct {
	Method         string          `json:"Method"`
	Path           string          `json:"Path"`
	Headers        http.Header     `json:"Headers"`
	Query          url.Values      `json:"Query"`
	Body           json.RawMessage `json:"Body"`
	HostCallbackID string          `json:"host_callback_id,omitempty"`

	// lowercase aliases for hosts that lower-case JSON keys
	MethodAlt string          `json:"method"`
	PathAlt   string          `json:"path"`
	BodyAlt   json.RawMessage `json:"body"`
}

// managementResponse is the host-expected HTTP-like response, then wrapped in ok envelope.
type managementResponse struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers"`
	Body       []byte      `json:"Body"`
}

type managementRegistration struct {
	Routes    []managementRoute    `json:"routes,omitempty"`
	Resources []managementResource `json:"resources,omitempty"`
}

type managementRoute struct {
	Method      string `json:"Method"`
	Path        string `json:"Path"`
	Description string `json:"Description"`
}

type managementResource struct {
	Path        string `json:"Path"`
	Menu        string `json:"Menu"`
	Description string `json:"Description"`
}

func buildManagementRegistration() managementRegistration {
	return managementRegistration{
		Resources: []managementResource{
			{
				Path:        "/index.html",
				Menu:        "xAI Quota Guard",
				Description: "xAI 短时额度自动禁用、到期恢复与 permission-denied 删除",
			},
		},
		Routes: []managementRoute{
			{Method: "GET", Path: "/cpa-xai-quota-guard/state", Description: "账号状态 JSON"},
			{Method: "GET", Path: "/cpa-xai-quota-guard/config", Description: "当前配置（脱敏）"},
			{Method: "GET", Path: "/cpa-xai-quota-guard/accounts", Description: "账号列表"},
			{Method: "GET", Path: "/cpa-xai-quota-guard/health", Description: "自检"},
			{Method: "POST", Path: "/cpa-xai-quota-guard/backfill", Description: "从 CPAMP 回补今日用量"},
			{Method: "GET", Path: "/cpa-xai-quota-guard/deletes", Description: "最近删除历史"},
			{Method: "GET", Path: "/cpa-xai-quota-guard/export", Description: "导出今日用量 JSON"},
			{Method: "POST", Path: "/cpa-xai-quota-guard/toggle", Description: "开关 enabled"},
			{Method: "POST", Path: "/cpa-xai-quota-guard/run", Description: "手动触发恢复扫描"},
		{Method: "POST", Path: "/cpa-xai-quota-guard/patrol", Description: "启动主动巡查(全量：启用凭证+spending冷却号)"},
		{Method: "POST", Path: "/cpa-xai-quota-guard/patrol/spending", Description: "仅巡查 spending_limit 冷却号(改模型后复查)"},
		{Method: "GET", Path: "/cpa-xai-quota-guard/patrol/status", Description: "巡查状态与日志"},
		{Method: "POST", Path: "/cpa-xai-quota-guard/patrol/stop", Description: "停止当前巡查"},
		{Method: "POST", Path: "/cpa-xai-quota-guard/patrol/config", Description: "保存定时巡查配置"},
		{Method: "GET", Path: "/cpa-xai-quota-guard/patrol/models", Description: "探测可用模型列表(凭证 /models + 建议)"},
		},
	}
}

func handleManagement(raw []byte) ([]byte, error) {
	var req managementRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return errorEnvelope("decode_management", err.Error()), nil
		}
	}
	method := strings.ToUpper(strings.TrimSpace(firstNonEmpty(req.Method, req.MethodAlt)))
	if method == "" {
		method = http.MethodGet
	}
	path := strings.TrimSpace(firstNonEmpty(req.Path, req.PathAlt))
	body := req.Body
	if len(body) == 0 && len(req.BodyAlt) > 0 {
		body = req.BodyAlt
	}
	req.Method = method
	req.Path = path
	body = decodeManagementBody(body)
	req.Body = body

	const resourcePrefix = "/v0/resource/plugins/" + pluginID + "/"
	if strings.HasPrefix(path, resourcePrefix) {
		return serveResource(strings.TrimPrefix(path, resourcePrefix))
	}
	// also accept bare resource path like /index.html delivered without prefix
	if path == "/index.html" || path == "index.html" || path == "/" || path == "" {
		// only treat empty as resource when host marks resource calls with that path alone
		// empty path from API should 404; resource host usually uses full prefix above
		if path == "/index.html" || path == "index.html" {
			return serveResource("index.html")
		}
	}

	const mgmtPrefix = "/v0/management/" + pluginID + "/"
	if strings.HasPrefix(path, mgmtPrefix) {
		return dispatchAPI(req, strings.TrimPrefix(path, mgmtPrefix))
	}
	// /cpa-xai-quota-guard/<action>
	const shortPrefix = "/" + pluginID + "/"
	if strings.HasPrefix(path, shortPrefix) {
		return dispatchAPI(req, strings.TrimPrefix(path, shortPrefix))
	}
	// bare action: state / config / ...
	if path != "" && !strings.Contains(strings.Trim(path, "/"), "/") {
		return dispatchAPI(req, strings.Trim(path, "/"))
	}
	// last segment fallback
	if idx := strings.LastIndex(path, "/"); idx >= 0 && idx+1 < len(path) {
		return dispatchAPI(req, path[idx+1:])
	}
	return okEnvelope(managementResponse{
		StatusCode: http.StatusNotFound,
		Headers:    http.Header{"content-type": []string{"text/plain; charset=utf-8"}},
		Body:       []byte("not found: " + path),
	})
}

func serveResource(sub string) ([]byte, error) {
	src := strings.TrimRight(strings.TrimSpace(sub), "/")
	if src == "" || src == "view" || src == "about" {
		src = "index.html"
	}
	return okEnvelope(managementResponse{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"content-type": []string{"text/html; charset=utf-8"}},
		Body:       renderConsole(),
	})
}

func dispatchAPI(req managementRequest, action string) ([]byte, error) {
	action = strings.Trim(strings.TrimSpace(action), "/")
	// collapse nested like "logs/clear" keep as-is
	switch action {
	case "state":
		return stateResponse(req)
	case "config", "settings":
		return configResponse()
	case "accounts":
		return accountsResponse(req)
	case "health":
		return healthResponse()
	case "backfill":
		return backfillResponse()
	case "deletes":
		return deletesResponse()
	case "export":
		return exportResponse()
	case "toggle":
		return toggleResponse(req)
	case "run":
		return runResponse()
	case "patrol":
		return patrolResponse(req)
	case "patrol/spending":
		return patrolSpendingResponse(req)
	case "patrol/status":
		return patrolStatusResponse()
	case "patrol/stop":
		return patrolStopResponse()
	case "patrol/config":
		return patrolConfigResponse(req)
	case "patrol/models":
		return patrolModelsResponse()
	default:
		return okEnvelope(managementResponse{
			StatusCode: http.StatusNotFound,
			Headers:    http.Header{"content-type": []string{"text/plain; charset=utf-8"}},
			Body:       []byte("unknown route: " + action),
		})
	}
}

func jsonResponse(v any) ([]byte, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return okEnvelope(managementResponse{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"content-type": []string{"application/json; charset=utf-8"}},
		Body:       body,
	})
}

func stateViewFromRequest(req managementRequest) string {
	// default focus: avoid shipping thousands of idle inventory rows to the browser.
	view := "focus"
	q := req.Query
	if q == nil && req.Headers != nil {
		// some hosts only put raw path; parse from Path
	}
	if q != nil {
		if v := strings.TrimSpace(q.Get("view")); v != "" {
			view = strings.ToLower(v)
		}
	}
	// fallback: ?view= on Path / PathAlt
	for _, raw := range []string{req.Path, req.PathAlt} {
		if i := strings.Index(raw, "view="); i >= 0 {
			v := raw[i+5:]
			if j := strings.IndexAny(v, "&/#?"); j >= 0 {
				v = v[:j]
			}
			if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
				view = v
			}
		}
	}
	switch view {
	case "all", "full", "inventory":
		return "all"
	default:
		return "focus"
	}
}

func isFocusAccountItem(item map[string]any) bool {
	h, _ := item["health"].(string)
	switch h {
	case "due", "auto", "manual", "over", "cpa_disabled":
		return true
	}
	// hot: only calendar-today activity (not lifetime totals)
	if anyInt64(item["used_today"]) > 0 || anyInt64(item["requests_today"]) > 0 {
		return true
	}
	if b, _ := item["rolling_over"].(bool); b {
		return true
	}
	if b, _ := item["tracked"].(bool); b {
		st, _ := item["state"].(string)
		if st == string(xaiquota.StateAutoDisabled) || st == string(xaiquota.StateUserManualDisabled) {
			return true
		}
	}
	return false
}

// focusHotCap limits "today active" noise so the management iframe stays interactive.
const focusHotCap = 80

func capFocusAccountList(list []map[string]any) (out []map[string]any, hotTotal, hotShown, hotHidden int) {
	priority := make([]map[string]any, 0, 64)
	hot := make([]map[string]any, 0, 128)
	for _, item := range list {
		if !isFocusAccountItem(item) {
			continue
		}
		h, _ := item["health"].(string)
		if h == "hot" {
			hot = append(hot, item)
			continue
		}
		// due/auto/manual/over/cpa_disabled always kept
		priority = append(priority, item)
	}
	hotTotal = len(hot)
	// hot already sorted by used_today desc from parent sort
	if len(hot) > focusHotCap {
		hotShown = focusHotCap
		hotHidden = len(hot) - focusHotCap
		hot = hot[:focusHotCap]
	} else {
		hotShown = len(hot)
	}
	out = make([]map[string]any, 0, len(priority)+len(hot))
	out = append(out, priority...)
	out = append(out, hot...)
	return out, hotTotal, hotShown, hotHidden
}

func stateResponse(req managementRequest) ([]byte, error) {
	g := guard()
	cfg := g.Config()
	now := time.Now().UnixMilli()
	view := stateViewFromRequest(req)
	tracked := g.Snapshot()
	usageByAuth, quotaByAuth := g.UsageAndQuotaMaps()

	// Refresh free-usage snapshots from tracked reasons first.
	for _, a := range tracked {
		reason := htmlUnescapeBasic(a.Reason)
		if actual, limit, ok := xaiquota.ParseFreeUsageTokens(reason); ok {
			g.ObserveQuota(a.AuthIndex, actual, limit)
		}
	}

	// Single auth-files list (used for both inventory metrics and account merge).
	inv, err := newMgmtAuth(cfg).List()
	if err != nil {
		inv = nil
	}
	byIndex := map[string]xaiquota.AuthFile{}
	inCPA := map[string]bool{}
	xaiTotal, xaiEnabled, xaiDisabled := 0, 0, 0
	var successSum, failedSum int64
	for _, f := range inv {
		if !xaiquota.IsXAIProvider(f.Provider, "") {
			continue
		}
		xaiTotal++
		successSum += f.Success
		failedSum += f.Failed
		if f.Disabled {
			xaiDisabled++
		} else {
			xaiEnabled++
		}
		if f.AuthIndex == "" {
			continue
		}
		byIndex[f.AuthIndex] = f
		inCPA[f.AuthIndex] = true
	}
	// Drop plugin state for credentials no longer present in CPA inventory.
	// Previously we re-injected missing tracked keys as ghosts, so deleted/re-enabled
	// accounts stayed wrong in the account table until manual cleanup.
	if n := g.PruneMissingInventory(inCPA); n > 0 {
		tracked = g.Snapshot()
	}

	// Focus view: only materialize tracked/hot accounts into the table payload.
	// Full inventory (thousands of idle files) is counted for metrics but not serialized.
	keys := make([]string, 0, len(byIndex))
	if view == "all" {
		for k := range byIndex {
			keys = append(keys, k)
		}
	} else {
		seen := map[string]bool{}
		for k := range tracked {
			if _, ok := byIndex[k]; ok && !seen[k] {
				keys = append(keys, k)
				seen[k] = true
			}
		}
		// Focus: actionable only — disabled / today activity. Never pull pure historical UsedTotal.
		// Hot-today is hard-capped later so a busy day cannot ship 900+ rows to the browser.
		for k, f := range byIndex {
			if seen[k] {
				continue
			}
			u := usageByAuth[k]
			if f.Disabled || u.UsedToday > 0 || u.RequestsToday > 0 {
				keys = append(keys, k)
				seen[k] = true
			}
		}
	}

	autoN, manualN, dueN, overN, invOnlyN, cpaDisabledN := 0, 0, 0, 0, 0, 0
	// For summary.inventory_only / cpa_disabled under focus, count from full inventory without heavy item build.
	if view != "all" {
		for k, f := range byIndex {
			if _, hasRec := tracked[k]; hasRec {
				continue
			}
			invOnlyN++
			if f.Disabled {
				cpaDisabledN++
			}
		}
	}
	list := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		f := byIndex[k]
		rec, hasRec := tracked[k]
		u := usageByAuth[k]
		item := map[string]any{
			"auth_index":     f.AuthIndex,
			"file_name":      firstNonEmpty(f.Name, rec.FileName),
			"provider":       firstNonEmpty(f.Provider, rec.Provider, "xai"),
			"account":        firstNonEmpty(f.Account, rec.Account),
			"cpa_disabled":   f.Disabled,
			"cpa_success":    f.Success,
			"cpa_failed":     f.Failed,
			"in_inventory":   true,
			"tracked":        hasRec,
			"used_today":     u.UsedToday,
			"used_total":     u.UsedTotal,
			"requests_today": u.RequestsToday,
			"requests_total": u.RequestsTotal,
			"last_tokens":    u.LastTokens,
			"last_at_ms":     u.LastAtMS,
			"last_failed":    u.LastFailed,
		}
		if !inCPA[k] {
			item["in_inventory"] = false
		}

		if hasRec {
			item["state"] = rec.State
			item["disable_source"] = rec.DisableSource
			item["recover_at_ms"] = rec.RecoverAtMS
			item["disabled_at_ms"] = rec.DisabledAtMS
			item["pre_disabled"] = rec.PreDisabled
			item["owner"] = rec.Owner
			item["reason"] = htmlUnescapeBasic(rec.Reason)
			item["signal"] = rec.Signal
			item["updated_at_ms"] = rec.UpdatedAtMS
			if rec.State == xaiquota.StateAutoDisabled {
				autoN++
				if rec.RecoverAtMS > 0 && now >= rec.RecoverAtMS {
					dueN++
					item["health"] = "due"
				} else {
					item["health"] = "auto"
				}
			} else if rec.State == xaiquota.StateUserManualDisabled || rec.DisableSource == xaiquota.SourceUserManual {
				manualN++
				item["health"] = "manual"
			} else if u.UsedToday > 0 || u.RequestsToday > 0 {
				item["health"] = "hot"
			} else {
				item["health"] = "active"
			}
		} else {
			// Not tracked by plugin state machine.
			if view == "all" {
				invOnlyN++
			}
			item["tracked"] = false
			if f.Disabled {
				if view == "all" {
					cpaDisabledN++
				}
				item["state"] = "cpa_disabled"
				item["disable_source"] = "external"
				item["health"] = "cpa_disabled"
				item["reason"] = "CPA 侧已禁用（非本插件状态机）"
			} else if u.UsedToday > 0 || u.RequestsToday > 0 {
				item["state"] = "inventory"
				item["disable_source"] = "none"
				item["health"] = "hot"
				item["reason"] = ""
			} else {
				item["state"] = "inventory"
				item["disable_source"] = "none"
				item["health"] = "inventory"
				item["reason"] = ""
			}
			item["recover_at_ms"] = int64(0)
			item["disabled_at_ms"] = int64(0)
			item["pre_disabled"] = false
			item["owner"] = ""
			item["signal"] = ""
			item["updated_at_ms"] = int64(0)
		}

		if q := quotaByAuth[k]; q != nil {
			item["rolling_actual"] = q.Actual
			item["rolling_limit"] = q.Limit
			item["rolling_updated_at_ms"] = q.UpdatedAtMS
			item["rolling_over"] = q.Limit > 0 && q.Actual > q.Limit
			if q.Limit > 0 && q.Actual > q.Limit {
				overN++
				if h, _ := item["health"].(string); h == "inventory" || h == "hot" || h == "active" {
					item["health"] = "over"
				}
			}
		}
		list = append(list, item)
	}

	// Sort: due/auto/manual/over/hot/cpa_disabled/inventory/active
	rank := func(m map[string]any) int {
		h, _ := m["health"].(string)
		switch h {
		case "due":
			return 0
		case "auto":
			return 1
		case "manual":
			return 2
		case "cpa_disabled":
			return 3
		case "over":
			return 4
		case "hot":
			return 5
		case "inventory":
			return 6
		default:
			return 7
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		ri, rj := rank(list[i]), rank(list[j])
		if ri != rj {
			return ri < rj
		}
		ui := anyInt64(list[i]["used_today"])
		uj := anyInt64(list[j]["used_today"])
		if ui != uj {
			return ui > uj
		}
		ai, _ := list[i]["account"].(string)
		aj, _ := list[j]["account"].(string)
		if ai != aj {
			return ai < aj
		}
		fi, _ := list[i]["file_name"].(string)
		fj, _ := list[j]["file_name"].(string)
		if fi != fj {
			return fi < fj
		}
		ki, _ := list[i]["auth_index"].(string)
		kj, _ := list[j]["auth_index"].(string)
		return ki < kj
	})

	accountsTotal := len(list)
	outList := list
	hotTotal, hotShown, hotHidden := 0, 0, 0
	if view != "all" {
		outList, hotTotal, hotShown, hotHidden = capFocusAccountList(list)
	}

	g.SyncInventoryUsage(successSum, failedSum, 0)
	metrics := g.MetricsWithInventory(xaiTotal, xaiEnabled, xaiDisabled)
	metrics.EstimatePerSuccess = 0

	return jsonResponse(map[string]any{
		"plugin_id": pluginID,
		"version":   pluginVer,
		"enabled":   cfg.Enabled,
		"now_ms":    now,
		"view":      view,
		"config": map[string]any{
			"enabled":                      cfg.Enabled,
			"tick_seconds":                 cfg.TickSeconds,
			"max_reset_seconds":            cfg.MaxResetSeconds,
			"min_reset_seconds":            cfg.MinResetSeconds,
			"include_unobserved_quota_est": cfg.IncludeUnobservedQuotaEst,
			"management_url":               cfg.ManagementURL,
			"management_key_set":           cfg.ManagementKey != "",
			"state_path":                   cfg.StatePath,
			"cpamp_url":                    cfg.CPAMPURL,
			"cpamp_admin_key_set":          cfg.CPAMPAdminKey != "",
			"webhook_url_set":              cfg.WebhookURL != "",
			"patrol_enabled":               cfg.PatrolEnabled,
			"patrol_interval":              cfg.PatrolInterval,
			"patrol_timeout":               cfg.PatrolTimeout,
			"patrol_auth_dir":              cfg.PatrolAuthDir,
			"patrol_concurrency":           cfg.PatrolConcurrency,
			"patrol_batch_size":            cfg.PatrolBatchSize,
			"patrol_model":                cfg.PatrolModel,
			"patrol_auto_model_switch":   cfg.PatrolAutoModelSwitch,
		},
		"accounts": outList,
		"summary": map[string]any{
			"total":            accountsTotal,
			"returned":         len(outList),
			"view":             view,
			"tracked":          len(tracked),
			"inventory_only":   invOnlyN,
			"auto_disabled":    autoN,
			"user_manual":      manualN,
			"recover_due":      dueN,
			"rolling_over":     overN,
			"cpa_disabled":     cpaDisabledN,
			"hot_total":        hotTotal,
			"hot_shown":        hotShown,
			"hot_hidden":       hotHidden,
			"focus_hot_cap":    focusHotCap,
		},
		"metrics":        metrics,
		"delete_history": g.ListDeletes(20),
	})
}

func countXAIInventory() (total, enabled, disabled int, successSum, failedSum int64) {
	cfg := guard().Config()
	list, err := newMgmtAuth(cfg).List()
	if err != nil || list == nil {
		return 0, 0, 0, 0, 0
	}
	for _, f := range list {
		// Strict: only provider == xai. Unknown/empty provider is excluded.
		if !xaiquota.IsXAIProvider(f.Provider, "") {
			continue
		}
		total++
		successSum += f.Success
		failedSum += f.Failed
		if f.Disabled {
			disabled++
		} else {
			enabled++
		}
	}
	return total, enabled, disabled, successSum, failedSum
}

func accountsResponse(req managementRequest) ([]byte, error) {
	// same payload as state; honor view=all/focus
	return stateResponse(req)
}

func exportResponse() ([]byte, error) {
	g := guard()
	cfg := g.Config()
	xaiTotal, xaiEnabled, xaiDisabled, _, _ := countXAIInventory()
	metrics := g.MetricsWithInventory(xaiTotal, xaiEnabled, xaiDisabled)
	return jsonResponse(map[string]any{
		"exported_at_ms": time.Now().UnixMilli(),
		"plugin":         pluginID,
		"version":        pluginVer,
		"day_key":        metrics.DayKey,
		"metrics":        metrics,
		"accounts":       g.Snapshot(),
		"usage_by_auth":  g.UsageByAuthMap(),
		"delete_history": g.ListDeletes(100),
		"cpamp_url":      cfg.CPAMPURL,
	})
}

func healthResponse() ([]byte, error) {
	return jsonResponse(guard().HealthCheck())
}

func deletesResponse() ([]byte, error) {
	return jsonResponse(map[string]any{"items": guard().ListDeletes(50)})
}

func backfillResponse() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	res, err := guard().BackfillFromCPAMP(ctx)
	if err != nil {
		return jsonResponse(map[string]any{"ok": false, "error": err.Error()})
	}
	res["ok"] = true
	return jsonResponse(res)
}

func configResponse() ([]byte, error) {
	cfg := guard().Config()
	return jsonResponse(map[string]any{
		"enabled":                cfg.Enabled,
		"tick_seconds":           cfg.TickSeconds,
		"max_reset_seconds":      cfg.MaxResetSeconds,
		"management_url":         cfg.ManagementURL,
		"management_key_set":     cfg.ManagementKey != "",
		"state_path":             cfg.StatePath,
		"patrol_enabled":         cfg.PatrolEnabled,
		"patrol_interval":        cfg.PatrolInterval,
		"patrol_timeout":         cfg.PatrolTimeout,
		"patrol_auth_dir":        cfg.PatrolAuthDir,
		"patrol_concurrency":     cfg.PatrolConcurrency,
		"patrol_batch_size":      cfg.PatrolBatchSize,
		"patrol_model":          cfg.PatrolModel,
		"patrol_auto_model_switch": cfg.PatrolAutoModelSwitch,
	})
}

func toggleResponse(req managementRequest) ([]byte, error) {
	if req.Method != http.MethodPost {
		return okEnvelope(managementResponse{
			StatusCode: http.StatusMethodNotAllowed,
			Headers:    http.Header{"content-type": []string{"application/json; charset=utf-8"}},
			Body:       []byte(`{"error":"POST required"}`),
		})
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	_ = json.Unmarshal(req.Body, &body)
	cfg := guard().Config()
	if body.Enabled != nil {
		cfg.Enabled = *body.Enabled
	} else {
		cfg.Enabled = !cfg.Enabled
	}
	// Write functional switch to quota_guard_enabled (NOT CPA host "enabled",
	// which unloads the plugin and 404s all management routes).
	// Keep host enabled=true so the plugin stays loaded.
	if err := writePluginConfig(cfg, map[string]any{
		"enabled":             true,
		"quota_guard_enabled": cfg.Enabled,
	}); err != nil {
		return jsonResponse(map[string]any{"ok": false, "error": err.Error(), "enabled": cfg.Enabled})
	}
	guard().ApplyConfig(cfg)
	return jsonResponse(map[string]any{"ok": true, "enabled": cfg.Enabled, "persisted": true})
}

func runResponse() ([]byte, error) {
	guard().Tick()
	return jsonResponse(map[string]any{"ok": true, "ran": true})
}


func patrolSpendingResponse(req managementRequest) ([]byte, error) {
	if req.Method != http.MethodPost {
		return okEnvelope(managementResponse{
			StatusCode: http.StatusMethodNotAllowed,
			Headers:    http.Header{"content-type": []string{"application/json; charset=utf-8"}},
			Body:       []byte(`{"error":"POST required"}`),
		})
	}
	g := guard()
	status := g.PatrolRunSpendingOnly()
	return jsonResponse(map[string]any{
		"ok":     true,
		"scope":  "spending_only",
		"patrol": status,
	})
}

func patrolResponse(req managementRequest) ([]byte, error) {
	if req.Method != http.MethodPost {
		return okEnvelope(managementResponse{
			StatusCode: http.StatusMethodNotAllowed,
			Headers:    http.Header{"content-type": []string{"application/json; charset=utf-8"}},
			Body:       []byte(`{"error":"POST required"}`),
		})
	}
	g := guard()
	status := g.PatrolRunOnce()
	return jsonResponse(map[string]any{
		"ok":         true,
		"patrol":     status,
	})
}

func patrolStatusResponse() ([]byte, error) {
	g := guard()
	status := g.PatrolStatus()
	return jsonResponse(map[string]any{
		"ok":             true,
		"patrol":         status,
		"delete_history": g.ListDeletes(20),
	})
}

func patrolStopResponse() ([]byte, error) {
	g := guard()
	g.PatrolStop()
	return jsonResponse(map[string]any{
		"ok":     true,
		"message": "stop requested",
	})
}

func patrolConfigResponse(req managementRequest) ([]byte, error) {
	if req.Method != http.MethodPost {
		return okEnvelope(managementResponse{
			StatusCode: http.StatusMethodNotAllowed,
			Headers:    http.Header{"content-type": []string{"application/json"}},
			Body:       []byte(`{"error":"POST required"}`),
		})
	}
	var body struct {
		PatrolEnabled   *bool   `json:"patrol_enabled"`
		PatrolInterval  *float64 `json:"patrol_interval"`
		PatrolTimeout   *float64 `json:"patrol_timeout"`
		PatrolAuthDir   *string `json:"patrol_auth_dir"`
		PatrolProxyURL  *string `json:"patrol_proxy_url"`
		PatrolConcurrency *float64 `json:"patrol_concurrency"`
		PatrolBatchSize *float64 `json:"patrol_batch_size"`
		PatrolModel     *string  `json:"patrol_model"`
	}
	_ = json.Unmarshal(req.Body, &body)
	cfg := guard().Config()
	if body.PatrolEnabled != nil {
		cfg.PatrolEnabled = *body.PatrolEnabled
	}
	if body.PatrolInterval != nil && *body.PatrolInterval > 0 {
		cfg.PatrolInterval = *body.PatrolInterval
	}
	if body.PatrolTimeout != nil && *body.PatrolTimeout > 0 {
		cfg.PatrolTimeout = *body.PatrolTimeout
	}
	if body.PatrolAuthDir != nil {
		cfg.PatrolAuthDir = strings.TrimSpace(*body.PatrolAuthDir)
	}
	if body.PatrolProxyURL != nil {
		cfg.PatrolProxyURL = strings.TrimSpace(*body.PatrolProxyURL)
	}
	if body.PatrolConcurrency != nil && *body.PatrolConcurrency > 0 {
		cfg.PatrolConcurrency = int(*body.PatrolConcurrency)
	}
	if body.PatrolBatchSize != nil {
		cfg.PatrolBatchSize = int(*body.PatrolBatchSize)
	}
	if body.PatrolModel != nil {
		m := strings.TrimSpace(*body.PatrolModel)
		if m == "" {
			m = xaiquota.DefaultPatrolModel
		}
		cfg.PatrolModel = m
	}
	// Persist full patrol settings into CPA plugin config (GET+merge+PUT).
	patch := map[string]any{
		"patrol_enabled":     cfg.PatrolEnabled,
		"patrol_interval":    cfg.PatrolInterval,
		"patrol_timeout":     cfg.PatrolTimeout,
		"patrol_auth_dir":    cfg.PatrolAuthDir,
		"patrol_concurrency": cfg.PatrolConcurrency,
		"patrol_batch_size":  cfg.PatrolBatchSize,
		"patrol_model":       cfg.PatrolModel,
	}
	if cfg.PatrolProxyURL != "" {
		patch["patrol_proxy_url"] = cfg.PatrolProxyURL
	}
	if err := writePluginConfig(cfg, patch); err != nil {
		return jsonResponse(map[string]any{"ok": false, "error": err.Error()})
	}
	guard().ApplyConfig(cfg)
	return jsonResponse(map[string]any{
		"ok":                 true,
		"persisted":          true,
		"patrol_enabled":     cfg.PatrolEnabled,
		"patrol_interval":    cfg.PatrolInterval,
		"patrol_timeout":     cfg.PatrolTimeout,
		"patrol_auth_dir":    cfg.PatrolAuthDir,
		"patrol_concurrency": cfg.PatrolConcurrency,
		"patrol_batch_size":  cfg.PatrolBatchSize,
		"patrol_model":       cfg.PatrolModel,
	})
}


func patrolModelsResponse() ([]byte, error) {
	g := guard()
	cfg := g.Config()
	models, source, errMsg := g.ListPatrolModels()
	return jsonResponse(map[string]any{
		"ok":           true,
		"models":       models,
		"source":       source,
		"error":        errMsg,
		"patrol_model": cfg.PatrolModel,
		"default":      xaiquota.DefaultPatrolModel,
	})
}

func decodeManagementBody(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return nil
	}
	// Prefer []byte unmarshal: host commonly encodes HTTP body as base64 JSON string.
	var asBytes []byte
	if err := json.Unmarshal(raw, &asBytes); err == nil {
		return asBytes
	}
	// Double-encoded JSON object as a JSON string.
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return []byte(asString)
	}
	// Already raw object/array JSON.
	return []byte(raw)
}
func anyInt64(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	case float64:
		return int64(t)
	default:
		return 0
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func renderConsole() []byte {
	const tpl = `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>xAI Quota Guard</title>
<style>
:root{--bg:#f4f7fb;--card:#fff;--text:#0f172a;--muted:#64748b;--accent:#0d9488;--warn:#b45309;--err:#b91c1c;--ok:#047857;--border:#e2e8f0}
*{box-sizing:border-box}body{margin:0;background:radial-gradient(1200px 600px at 10% -10%,#ccfbf1 0%,transparent 55%),linear-gradient(160deg,#f8fafc,#eef2ff 55%,#f1f5f9);color:var(--text);font-family:"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif;min-height:100vh;padding:1.25rem}
.wrap{max-width:1080px;margin:0 auto}
h1{font-size:1.3rem;margin:0 0 .35rem;display:flex;align-items:center;gap:.5rem;flex-wrap:wrap}
.sub{color:var(--muted);font-size:.9rem;margin-bottom:1rem}
.badge{font-size:.72rem;padding:.18rem .55rem;border-radius:9999px;background:#ccfbf1;color:#0f766e;border:1px solid #99f6e4;font-weight:600}
.badge.off{background:#fee2e2;color:#991b1b;border-color:#fecaca}
.badge.on{background:#d1fae5;color:#065f46;border-color:#6ee7b7}
button.on{background:#047857;border-color:#065f46;color:#fff}
button.off{background:#fff;border-color:#fca5a5;color:#991b1b}
button.on:hover{filter:brightness(1.05)}
button.off:hover{background:#fef2f2}
.status-dot{width:.65rem;height:.65rem;border-radius:50%;display:inline-block;margin-right:.35rem;vertical-align:middle}
.status-dot.on{background:#10b981;box-shadow:0 0 0 3px rgba(16,185,129,.25)}
.status-dot.off{background:#ef4444;box-shadow:0 0 0 3px rgba(239,68,68,.2)}
.badge.muted{background:#f1f5f9;color:var(--muted);border-color:var(--border)}
.grid{display:grid;gap:1rem}
.card{background:var(--card);border:1px solid var(--border);border-radius:14px;padding:1rem 1.1rem;box-shadow:0 1px 3px rgba(15,23,42,.06)}
.row{display:flex;gap:.6rem;flex-wrap:wrap;align-items:center}
.stats-wrap{display:grid;gap:.85rem;margin-top:.9rem}
.stats-group{background:linear-gradient(180deg,#f8fafc 0%,#fff 100%);border:1px solid var(--border);border-radius:12px;padding:.7rem .75rem .85rem}
.stats-group h3{margin:0 0 .55rem;font-size:.78rem;font-weight:700;color:#0f766e;letter-spacing:.04em;text-transform:none;display:flex;align-items:center;gap:.4rem}
.stats-group h3::before{content:"";width:.35rem;height:.35rem;border-radius:50%;background:var(--accent);display:inline-block}
.stats{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:.65rem}
@media (max-width:960px){.stats{grid-template-columns:repeat(2,minmax(0,1fr))}}
@media (max-width:560px){.stats{grid-template-columns:1fr}}
.stat{background:#fff;border:1px solid var(--border);border-radius:10px;padding:.7rem .75rem;min-height:84px;display:flex;flex-direction:column;justify-content:space-between;box-shadow:0 1px 2px rgba(15,23,42,.03)}
.stat.accent{border-color:#99f6e4;background:linear-gradient(160deg,#f0fdfa,#fff)}
.stat.warn{border-color:#fcd34d;background:linear-gradient(160deg,#fffbeb,#fff)}
.stat b{display:block;font-size:1.28rem;margin:.2rem 0 .15rem;line-height:1.15;font-variant-numeric:tabular-nums;letter-spacing:-.02em;word-break:break-all}
.stat .unit{font-size:.72rem;font-weight:600;color:#0f766e;margin-left:.2rem}
.stat span.lbl{color:var(--muted);font-size:.78rem;font-weight:600}
.stat .sub{color:var(--muted);font-size:.72rem;line-height:1.35;margin-top:auto}
.stat .bar{height:4px;background:#e2e8f0;border-radius:999px;overflow:hidden;margin-top:.45rem}
.stat .bar>i{display:block;height:100%;background:linear-gradient(90deg,#14b8a6,#0d9488);border-radius:999px;width:0%}
button,.btn{appearance:none;border:1px solid var(--border);background:#fff;color:var(--text);border-radius:10px;padding:.45rem .8rem;font-size:.88rem;cursor:pointer}
button.primary{background:var(--accent);border-color:#0f766e;color:#fff}
button.warn{background:#fff7ed;border-color:#fdba74;color:#9a3412}
button:disabled{opacity:.5;cursor:not-allowed}
input,select{border:1px solid var(--border);border-radius:10px;padding:.45rem .65rem;font-size:.88rem;min-width:0}
table{width:100%;border-collapse:collapse;font-size:.86rem}
th,td{text-align:left;padding:.55rem .4rem;border-bottom:1px solid var(--border);vertical-align:top}
th{color:var(--muted);font-weight:600}
.tag{display:inline-block;padding:.1rem .45rem;border-radius:999px;font-size:.72rem;border:1px solid var(--border)}
.tag.auto{background:#ecfeff;color:#0e7490;border-color:#a5f3fc}
.tag.manual{background:#fef3c7;color:#92400e;border-color:#fcd34d}
.tag.active{background:#ecfdf5;color:#047857;border-color:#a7f3d0}
.muted{color:var(--muted)}
.err{color:var(--err)}
.acc-toolbar{display:flex;gap:.45rem;flex-wrap:wrap;align-items:center;margin:.35rem 0 .7rem;padding:.55rem .65rem;background:linear-gradient(180deg,#f8fafc,#fff);border:1px solid var(--border);border-radius:12px}
.acc-table-wrap{overflow:auto;border:1px solid var(--border);border-radius:12px;background:#fff}
table.acc{width:100%;border-collapse:separate;border-spacing:0;font-size:.86rem}
table.acc thead th{position:sticky;top:0;z-index:1;background:#f8fafc;color:var(--muted);font-weight:700;font-size:.75rem;letter-spacing:.02em;text-align:left;padding:.65rem .55rem;border-bottom:1px solid var(--border);white-space:nowrap}
table.acc tbody td{padding:.7rem .55rem;border-bottom:1px solid #eef2f7;vertical-align:top}
table.acc tbody tr:last-child td{border-bottom:none}
table.acc tbody tr:hover{background:#f8fafc}
table.acc tbody tr.row-over{background:#fff7ed}
table.acc tbody tr.row-due{background:#fef2f2}
table.acc tbody tr.row-auto{background:#f0fdfa}
.acc-name{font-weight:600;line-height:1.25}
.acc-file{color:var(--muted);font-size:.72rem;margin-top:.15rem;word-break:break-all}
.acc-meta{display:flex;flex-wrap:wrap;gap:.3rem;margin-top:.25rem}
.chip{display:inline-flex;align-items:center;gap:.2rem;padding:.08rem .4rem;border-radius:999px;font-size:.7rem;border:1px solid var(--border);background:#f8fafc;color:#475569;font-variant-numeric:tabular-nums}
.chip.hot{background:#ecfeff;border-color:#a5f3fc;color:#0e7490}
.chip.over{background:#fff7ed;border-color:#fdba74;color:#9a3412}
.chip.due{background:#fef2f2;border-color:#fecaca;color:#b91c1c}
.reason-main{font-weight:600;line-height:1.35}
.reason-sub{color:var(--muted);font-size:.72rem;margin-top:.2rem;line-height:1.35}
.tag.due{background:#fef2f2;color:#b91c1c;border-color:#fecaca}
.tag.over{background:#fff7ed;color:#9a3412;border-color:#fdba74}
.tag.cpa{background:#f1f5f9;color:#475569;border-color:#cbd5e1}
.tag.hot{background:#ecfeff;color:#0e7490;border-color:#a5f3fc}
.stack{display:flex;flex-direction:column;gap:.2rem}
.mono{font-variant-numeric:tabular-nums}
.guide{display:none;background:#fffbeb;border:1px solid #fcd34d;color:#78350f;border-radius:10px;padding:.65rem .8rem;margin:.5rem 0;font-size:.85rem}
code{background:#f1f5f9;padding:.1rem .3rem;border-radius:4px;font-size:.82rem}
</style></head><body><div class="wrap">
<h1>xAI Quota Guard <span class="badge" id="enBadge">…</span> <span class="badge muted" id="verBadge">v0</span></h1>
<div class="sub">仅处理 xAI：429 免费额度用尽后冷却约 24h（滚动窗口）到期才自动恢复 · 未到点不会启用 · 403 permission-denied 直接删除 · 用户手动禁用永不自动启用</div>
<div class="grid">
  <div class="card">
    <div class="row" style="justify-content:space-between">
      <div class="row">
        <button class="primary on" id="btnToggle" onclick="togglePlugin()" title="切换插件总开关"><span class="status-dot on" id="enDot"></span><span id="enBtnText">加载中…</span></button>
        <button onclick="runTick()">立即扫描恢复</button>
        <button onclick="loadState()">刷新</button>
        <button onclick="runBackfill()" id="btnBackfill" title="用 CPAMP analytics 回补日历今日真实 token">CPAMP 回补</button>
        <button onclick="runHealth()" id="btnHealth">自检</button>
        <button onclick="exportUsage()">导出JSON</button>
      </div>
      <div class="muted" id="nowLabel">—</div>
    </div>
    <div class="stats-wrap">
      <div class="stats-group">
        <h3>插件状态</h3>
        <div class="stats">
          <div class="stat accent"><span class="lbl">xAI 凭证</span><b id="sXaiTotal">0</b><div class="sub" id="sXaiSplit">启用 0 / 停用 0</div></div>
          <div class="stat"><span class="lbl">自动停用中</span><b id="sAuto">0</b><div class="sub" id="sTotalSub">plugin_auto 冷却</div></div>
          <div class="stat warn"><span class="lbl">已到点待启用</span><b id="sDue">0</b><div class="sub">等待 tick 恢复</div></div>
          <div class="stat"><span class="lbl">用户手动停用</span><b id="sManual">0</b><div class="sub">永不自动启用</div></div>
        </div>
      </div>
      <div class="stats-group">
        <h3>xAI 额度（仅 provider=xai）</h3>
        <div class="stats">
          <div class="stat accent"><span class="lbl">总额度(估)</span><b id="sQuotaTotal">0</b><div class="sub" id="sQuotaKnown">xAI 免费池</div></div>
          <div class="stat accent"><span class="lbl">已用 · 日历今日</span><b id="sUsedToday">0</b><div class="sub" id="sDayKey">—</div></div>
          <div class="stat accent"><span class="lbl">滚动池 used/limit</span><b id="sRolling">0</b><div class="sub" id="sRollingSub">free-usage 已知账号</div></div>
          <div class="stat accent"><span class="lbl">已用 · 总计</span><b id="sUsedTotal">0</b><div class="sub" id="sUsedNote">事件累计</div>
            <div class="bar" title="已用/总额度"><i id="sUsedBar"></i></div>
          </div>
        </div>
      </div>
    </div>
    <div id="recoverTip" class="guide" style="display:none;margin-top:.75rem"></div>
  </div>
  <div class="card">
    <div style="font-weight:600;margin-bottom:.5rem">管理 API 配置（浏览器鉴权）</div>
    <div class="guide" id="cfgGuide">请填写 X-Management-Key 并保存；该 key 仅保存在本机浏览器 localStorage，不会写回插件配置面板以外的明文日志。</div>
    <div class="row">
      <input id="cfgKey" type="password" placeholder="X-Management-Key" style="flex:1;min-width:220px">
      <button class="primary" onclick="saveKey()">保存 Key</button>
    </div>
    <div class="muted" style="margin-top:.45rem;font-size:.82rem" id="cfgMeta">management_url / key 状态加载中…</div>
  </div>
  <div class="card" id="patrolCard">
    <div class="row" style="justify-content:space-between;gap:.5rem;margin-bottom:.35rem">
      <div style="font-weight:700">主动巡查</div>
      <div class="muted" style="font-size:.78rem" id="patrolHint">402=积分/订阅耗尽(spending-limit)→冷却不删；可自动换模型再测；403/401 删除；可仅复查冷却号</div>
    </div>
    <div class="row" style="gap:.6rem;flex-wrap:wrap;align-items:center">
      <label style="display:flex;align-items:center;gap:.3rem;font-size:.85rem">
        <input type="checkbox" id="cfgPatrolEn" style="width:auto"> 启用定时巡查
      </label>
      <label style="font-size:.85rem">周期(秒)
        <input id="cfgPatrolInt" type="number" min="60" step="60" style="width:80px" placeholder="3600">
      </label>
      <label style="font-size:.85rem">超时(秒)
        <input id="cfgPatrolTO" type="number" min="1" step="1" style="width:60px" placeholder="15">
      </label>
      <label style="font-size:.85rem">并发
        <input id="cfgPatrolCon" type="number" min="1" step="1" style="width:60px" placeholder="8">
      </label>
      <label style="font-size:.85rem">每轮上限(0=不限)
        <input id="cfgPatrolBatch" type="number" min="0" step="1" style="width:60px" placeholder="0">
      </label>
      <button class="primary" onclick="savePatrolConfig()">保存配置</button>
    </div>
    <div class="row" style="gap:.6rem;flex-wrap:wrap;margin-top:.4rem">
      <label style="font-size:.85rem;flex:1;min-width:200px">auth 目录
        <input id="cfgPatrolDir" type="text" placeholder="/root/.cli-proxy-api" style="width:100%">
      </label>
      <label style="font-size:.85rem;flex:1;min-width:180px">代理(可选)
        <input id="cfgPatrolProxy" type="text" placeholder="socks5://host:port" style="width:100%">
      </label>
    </div>
    <div class="row" style="gap:.6rem;flex-wrap:wrap;margin-top:.4rem;align-items:flex-end">
      <label style="font-size:.85rem;flex:1;min-width:240px">探测模型
        <select id="cfgPatrolModel" style="width:100%;min-height:2rem">
          <option value="grok-4.5-build-free">grok-4.5-build-free（推荐·免费档）</option>
        </select>
      </label>
      <button type="button" onclick="refreshPatrolModels()" style="white-space:nowrap">刷新模型列表</button>
      <label style="display:flex;align-items:center;gap:.3rem;font-size:.85rem;white-space:nowrap">
        <input type="checkbox" id="cfgPatrolAutoModel" style="width:auto"> 402 自动换模型再测
      </label>
      <span class="muted" style="font-size:.78rem;max-width:320px" id="patrolModelHint">主模型探测；开启自动换模型时，402 会拉凭证模型列表轮询备用，仍 402 才冷却</span>
    </div>
    <div class="muted" style="margin-top:.4rem;font-size:.8rem" id="patrolCfgHint">配置加载中…</div>
    <hr style="border:none;border-top:1px solid var(--border);margin:.7rem 0">
    <div class="row" style="gap:.6rem;flex-wrap:wrap;align-items:center">
      <button id="patrolBtn" class="warn" onclick="patrolStart()">全量巡查</button>
      <button id="patrolSpendBtn" class="primary" onclick="patrolSpendingStart()" title="仅复查 plugin_auto spending_limit 冷却号">仅复查冷却号</button>
      <button id="patrolStopBtn" class="off" onclick="patrolStop()" style="display:none">停止巡查</button>
      <span id="patrolStatus" class="muted" style="font-size:.82rem">空闲</span>
    </div>
    <div id="patrolProgress" style="margin-top:.6rem;display:none">
      <div style="background:#e2e8f0;border-radius:6px;height:.5rem;overflow:hidden">
        <div id="patrolBar" style="background:var(--accent);height:100%;width:0%;transition:width .3s"></div>
      </div>
      <div class="row" style="margin-top:.4rem;gap:1rem;font-size:.8rem">
        <span>已探测: <b id="patrolProbed">0</b></span>
        <span style="color:var(--ok)">存活: <b id="patrolAlive">0</b></span>
        <span style="color:var(--err)">已删除: <b id="patrolDeleted">0</b></span>
        <span style="color:var(--warn)">错误: <b id="patrolErrors">0</b></span>
      </div>
    </div>
    <div id="patrolDelHist" style="margin-top:.7rem">
      <div style="font-weight:600;margin-bottom:.3rem;font-size:.82rem;color:var(--muted)">删除历史（最近 20 条）</div>
      <div id="patrolDelBox" style="height:180px;overflow-y:auto;font-size:.78rem;border:1px solid var(--border);border-radius:8px;background:var(--card,#fff)">
        <table style="width:100%;border-collapse:collapse;table-layout:fixed">
          <thead><tr style="text-align:left;color:var(--muted);border-bottom:1px solid var(--border);position:sticky;top:0;background:var(--card,#fff)">
            <th style="padding:.25rem;width:28%">时间</th><th style="width:22%">账号</th><th style="width:10%">来源</th><th>原因</th>
          </tr></thead>
          <tbody id="patrolDelBody"><tr><td colspan="4" class="muted" style="padding:.5rem">加载中…</td></tr></tbody>
        </table>
      </div>
    </div>
    <div id="patrolLog" style="margin-top:.7rem;display:none">
      <div style="font-weight:600;margin-bottom:.3rem;font-size:.82rem;color:var(--muted)">巡查探测日志</div>
      <div style="height:220px;overflow-y:auto;font-size:.78rem;border:1px solid var(--border);border-radius:8px;background:var(--card,#fff)">
        <table style="width:100%;border-collapse:collapse;table-layout:fixed">
          <thead><tr style="text-align:left;color:var(--muted);border-bottom:1px solid var(--border);position:sticky;top:0;background:var(--card,#fff)">
            <th style="padding:.25rem;width:24%">时间</th><th style="width:22%">账号</th><th style="width:12%">动作</th><th style="width:10%">HTTP</th><th>原因</th>
          </tr></thead>
          <tbody id="patrolLogBody"></tbody>
        </table>
      </div>
    </div>
  </div>
  <div class="card">
    <div class="row" style="justify-content:space-between;gap:.5rem;margin-bottom:.35rem">
      <div style="font-weight:700">账号状态</div>
      <div class="muted" style="font-size:.78rem">与上方「xAI 凭证」同一 inventory · 默认关注异常/有用量</div>
    </div>
    <div class="acc-toolbar">
      <select id="accFilter" onchange="ACC_PAGE=1; onAccFilterChange()" style="min-width:150px">
        <option value="focus" selected>关注(异常+用量)</option>
        <option value="all">全部账号</option>
        <option value="auto">自动停用</option>
        <option value="due">已到点</option>
        <option value="manual">用户手动</option>
        <option value="hot">今日有用量</option>
        <option value="over">滚动超限</option>
        <option value="cpa_disabled">CPA已禁用</option>
        <option value="inventory">仅库存未跟踪</option>
        <option value="active">活跃/其他</option>
      </select>
      <input id="accSearch" placeholder="搜账号 / 文件名 / auth_index" oninput="onAccSearch()" style="flex:1;min-width:180px">
      <select id="accPageSize" onchange="ACC_PAGE=1; renderAccountTable()" style="width:110px" title="每页条数">
        <option value="30">30/页</option>
        <option value="50" selected>50/页</option>
        <option value="100">100/页</option>
        <option value="200">200/页</option>
      </select>
      <button type="button" onclick="ACC_PAGE=Math.max(1,ACC_PAGE-1); renderAccountTable()">上一页</button>
      <button type="button" onclick="ACC_PAGE=ACC_PAGE+1; renderAccountTable()">下一页</button>
      <span class="muted" id="accPageInfo" style="font-size:.82rem">—</span>
    </div>
    <div class="acc-table-wrap">
      <table class="acc">
        <thead><tr><th style="min-width:160px">账号</th><th style="min-width:110px">状态</th><th style="min-width:130px">恢复</th><th style="min-width:150px">今日用量</th><th style="min-width:180px">原因 / 说明</th></tr></thead>
        <tbody id="accBody"><tr><td colspan="5" class="muted">加载中…</td></tr></tbody>
      </table>
    </div>
  </div>
  <div class="card muted" style="font-size:.82rem">插件 ID: <code>cpa-xai-quota-guard</code> · 状态标签: <code>plugin_auto</code> / <code>user_manual</code> · 不兼容非 xAI · 时间解析失败静默跳过</div>
</div>
</div>
<script>
const API = "/v0/management/cpa-xai-quota-guard";
const KEY_LS = "cpaXaiQgKey";
function mgmtKey(){ return localStorage.getItem(KEY_LS) || ""; }
function setMgmtKey(v){ localStorage.setItem(KEY_LS, v || ""); }
function showKeyNeeded(){
  const b=document.getElementById("enBadge");
  if(b){ b.textContent="请配置 Management Key"; b.className="badge muted"; }
  const g=document.getElementById("cfgGuide"); if(g) g.style.display="block";
}
function decodeMaybeBody(res){
  if(res == null) return res;
  if(typeof res === "string"){
    try { return JSON.parse(res); } catch(e){ return res; }
  }
  if(typeof res !== "object") return res;
  // ManagementResponse: {StatusCode, Headers, Body}
  if(res.Body != null || res.body != null){
    let raw = res.Body != null ? res.Body : res.body;
    try{
      if(typeof raw === "string"){
        // plain json string or base64?
        try { return JSON.parse(raw); } catch(e1){
          try {
            const bin = atob(raw);
            const bytes = new Uint8Array(bin.length);
            for(let i=0;i<bin.length;i++) bytes[i]=bin.charCodeAt(i);
            return JSON.parse(new TextDecoder().decode(bytes));
          } catch(e2){ return raw; }
        }
      }
      if(Array.isArray(raw)){
        return JSON.parse(new TextDecoder().decode(new Uint8Array(raw)));
      }
      // already object
      if(typeof raw === "object") return raw;
    }catch(e){}
  }
  // nested envelope
  if("ok" in res && "result" in res){
    return decodeMaybeBody(res.result);
  }
  return res;
}
function normalizeStatePayload(d, nowOverride){
  d = decodeMaybeBody(d) || {};
  // if still wrapped once more
  if(d && d.accounts == null && d.result) d = decodeMaybeBody(d.result) || d;
  if(!Array.isArray(d.accounts)) d.accounts = [];
  const prev = d.summary || {};
  // Live due count from wall clock for displayed auto rows; keep server global fields.
  let dueLive = 0;
  const now = (typeof nowOverride === "number" && nowOverride > 0) ? nowOverride : Date.now();
  d.accounts.forEach(function(a){
    if(!a) return;
    if(a.state === "auto_disabled" && a.recover_at_ms > 0 && now >= a.recover_at_ms) dueLive++;
  });
  d.summary = Object.assign({}, prev, {
    // prefer server auto/manual totals (global); only refresh due live
    recover_due: (prev.auto_disabled != null) ? dueLive : (prev.recover_due || dueLive),
    returned: (prev.returned != null ? prev.returned : d.accounts.length)
  });
  // never clobber inventory/tracked/view from server
  if(prev.tracked != null) d.summary.tracked = prev.tracked;
  if(prev.inventory_only != null) d.summary.inventory_only = prev.inventory_only;
  if(prev.auto_disabled != null) d.summary.auto_disabled = prev.auto_disabled;
  if(prev.user_manual != null) d.summary.user_manual = prev.user_manual;
  if(prev.cpa_disabled != null) d.summary.cpa_disabled = prev.cpa_disabled;
  if(prev.rolling_over != null) d.summary.rolling_over = prev.rolling_over;
  if(prev.view != null) d.summary.view = prev.view;
  if(prev.total != null) d.summary.total = prev.total;
  if(prev.hot_hidden != null) d.summary.hot_hidden = prev.hot_hidden;
  if(prev.hot_total != null) d.summary.hot_total = prev.hot_total;
  if(prev.hot_shown != null) d.summary.hot_shown = prev.hot_shown;
  if(prev.focus_hot_cap != null) d.summary.focus_hot_cap = prev.focus_hot_cap;
  return d;
}
async function api(path, opts){
  opts = opts || {};
  const hdrs = {"content-type":"application/json"};
  const k = mgmtKey();
  if(!k){ showKeyNeeded(); return {ok:false,error:"no key"}; }
  hdrs["X-Management-Key"]=k;
  let r;
  const ctrl = (typeof AbortController !== "undefined") ? new AbortController() : null;
  const timeoutMs = opts.timeout_ms || 20000;
  let timer = null;
  if(ctrl){ timer = setTimeout(function(){ try{ ctrl.abort(); }catch(e){} }, timeoutMs); }
  try {
    r = await fetch(API + "/" + path, {
      method: opts.method || "GET",
      headers: hdrs,
      body: opts.body ? JSON.stringify(opts.body) : undefined,
      cache: "no-store",
      signal: ctrl ? ctrl.signal : undefined
    });
  } catch(e){
    if(timer) clearTimeout(timer);
    const msg = (e && e.name === "AbortError") ? ("timeout "+timeoutMs+"ms") : (e && e.message ? e.message : e);
    return {ok:false, error:"network: "+msg};
  }
  if(timer) clearTimeout(timer);
  if(r.status===401){ showKeyNeeded(); return {ok:false,error:"invalid management key"}; }
  if(!r.ok){
    let t=""; try{ t=await r.text(); }catch(e){}
    return {ok:false, error:"http "+r.status+" "+t.slice(0,120)};
  }
  let j; try{ j=await r.json(); }catch(e){ return {ok:false,error:"bad json status="+r.status}; }
  let res = j;
  if(j && typeof j==="object" && "ok" in j && "result" in j){
    res = decodeMaybeBody(j.result);
    return {ok: !!j.ok, result: res, error: j.error};
  }
  return {ok:true, result: decodeMaybeBody(j)};
}
let LAST_STATE = null;
let LAST_FETCH_AT = 0;
let LAST_TABLE_FP = "";
function fmtToken(n, withUnit){
  n = Number(n||0);
  if(!isFinite(n)) n = 0;
  const sign = n < 0 ? "-" : "";
  n = Math.abs(n);
  let val, unit;
  if(n >= 1e12){ val = n/1e12; unit = "T"; }
  else if(n >= 1e9){ val = n/1e9; unit = "B"; }
  else if(n >= 1e6){ val = n/1e6; unit = "M"; }
  else if(n >= 1e3){ val = n/1e3; unit = "K"; }
  else { val = n; unit = ""; }
  let s;
  if(unit){
    // auto precision by magnitude
    if(val >= 100) s = val.toFixed(0);
    else if(val >= 10) s = val.toFixed(1);
    else s = val.toFixed(2);
    s = s.replace(/\.0+$/, "").replace(/(\.[0-9]*?)0+$/, "$1");
  } else {
    s = String(Math.round(val));
  }
  const num = sign + s + unit;
  if(withUnit === false) return num;
  // tokens unit ladder for display subtitle when needed
  return num;
}
function fmtTokenHTML(n){
  n = Number(n||0);
  if(!isFinite(n)) n = 0;
  const sign = n < 0 ? "-" : "";
  const abs = Math.abs(n);
  let val, unit;
  if(abs >= 1e12){ val = abs/1e12; unit = "T"; }
  else if(abs >= 1e9){ val = abs/1e9; unit = "B"; }
  else if(abs >= 1e6){ val = abs/1e6; unit = "M"; }
  else if(abs >= 1e3){ val = abs/1e3; unit = "K"; }
  else { val = abs; unit = ""; }
  let s;
  if(unit){
    if(val >= 100) s = val.toFixed(0);
    else if(val >= 10) s = val.toFixed(1);
    else s = val.toFixed(2);
    s = s.replace(/\.0+$/, "").replace(/(\.[0-9]*?)0+$/, "$1");
  } else {
    s = String(Math.round(val));
  }
  if(unit) return sign + s + '<span class="unit">' + unit + '</span>';
  return sign + s;
}
function setToken(id, n){
  const el = document.getElementById(id);
  if(!el) return;
  el.innerHTML = fmtTokenHTML(n);
}

function fmtTime(ms){
  if(!ms) return "—";
  try{ return new Date(ms).toLocaleString(); }catch(e){ return String(ms); }
}
function fmtCountdown(ms, nowMs){
  if(!ms) return {text:"—", due:false};
  const left = ms - (nowMs || Date.now());
  if(left <= 0) return {text:"已到点，等待扫描恢复", due:true};
  const s = Math.floor(left/1000);
  const h = Math.floor(s/3600);
  const m = Math.floor((s%3600)/60);
  const sec = s%60;
  let text;
  if(h >= 24) text = Math.floor(h/24) + "天" + (h%24) + "小时";
  else if(h > 0) text = h + "小时" + m + "分";
  else if(m > 0) text = m + "分" + sec + "秒";
  else text = sec + "秒";
  return {text: "还剩 " + text, due:false};
}
function decodeEntities(s){
  return String(s||"").replace(/&#(\d+);/g,function(_,n){return String.fromCharCode(+n);})
    .replace(/&#x([0-9a-fA-F]+);/g,function(_,n){return String.fromCharCode(parseInt(n,16));})
    .replace(/&quot;/g,'"').replace(/&#34;/g,'"').replace(/&apos;/g,"'").replace(/&#39;/g,"'")
    .replace(/&lt;/g,"<").replace(/&gt;/g,">").replace(/&amp;/g,"&");
}
function humanReason(a){
  let raw = decodeEntities(a.reason || "");
  const signal = a.signal || "";
  // already human-friendly
  if(raw && !raw.trim().startsWith("{") && raw.indexOf("免费额度")>=0) return raw;
  if(raw && !raw.trim().startsWith("{") && raw.indexOf("后可恢复")>=0) return raw;
  let code = "";
  let errText = "";
  try {
    if(raw.trim().startsWith("{")){
      const o = JSON.parse(raw);
      code = o.code || (o.error && o.error.code) || "";
      errText = (typeof o.error === "string" ? o.error : (o.error && o.error.message)) || o.message || "";
    }
  } catch(e) {}
  if(!code && signal.indexOf("subscription:free-usage-exhausted")>=0) code = "subscription:free-usage-exhausted";
  if(!code && /free-usage-exhausted|free usage/i.test(raw+signal)) code = "subscription:free-usage-exhausted";
  if(code === "subscription:free-usage-exhausted" || /free usage|free-usage/i.test(raw+errText+signal)){
    return "免费额度用尽（滚动 24 小时窗口）";
  }
  if(/permission-denied|permission_denied/i.test(raw+signal)) return "权限拒绝（将删除账号）";
  if(/invalid or expired credentials|no auth context|invalid_grant|refresh token has been revoked/i.test(raw+signal)) return "凭证失效/已吊销（将删除账号）";
  if(/spending-limit|run out of credits|personal-team-blocked|spending_limit/i.test(raw+signal)) return "积分/订阅耗尽（程序冷却，巡查恢复后启用）";
  if(code) return "xAI 限制: " + code;
  if(signal) return signal.replace(/^body\.error\.code=/, "错误码: ");
  if(errText) return errText.slice(0,100);
  if(raw) return raw.slice(0,100);
  return "—";
}
function patrolActionLabel(a){
  if(a==="deleted") return "已删除";
  if(a==="alive") return "存活";
  if(a==="error") return "错误";
  if(a==="cooldown") return "冷却禁用";
  if(a==="cooldown_skip") return "跳过";
  if(a==="reenabled") return "已恢复启用";
  return a||"—";
}
function stateTag(st, src, health){
  if(health==="due") return '<span class="tag due">已到点待恢复</span>';
  if(st==="auto_disabled" || health==="auto") return '<span class="tag auto">程序自动停用</span>';
  if(st==="user_manual_disabled" || src==="user_manual" || health==="manual") return '<span class="tag manual">用户手动停用</span>';
  if(st==="cpa_disabled" || health==="cpa_disabled") return '<span class="tag cpa">CPA 已禁用</span>';
  if(health==="over") return '<span class="tag over">滚动超限</span>';
  if(health==="hot") return '<span class="tag hot">今日有用量</span>';
  if(st==="inventory" || health==="inventory") return '<span class="tag active">库存未跟踪</span>';
  if(st==="active" || health==="active") return '<span class="tag active">正常</span>';
  return '<span class="tag active">'+esc(st||"—")+'</span>';
}
function sourceLabel(src){
  if(src==="plugin_auto") return "程序自动";
  if(src==="user_manual") return "用户手动";
  if(src==="external") return "CPA外部";
  return src || "—";
}
function paintStatusBar(d){
  d = normalizeStatePayload(d || LAST_STATE || {}, Date.now());
  const s = d.summary || {};
  const set = function(id, v){ const el=document.getElementById(id); if(el) el.textContent = String(v); };
  set("sAuto", s.auto_disabled||0);
  set("sDue", s.recover_due||0);
  set("sManual", s.user_manual||0);
  const m = d.metrics || {};
  // 凭证数 = xAI inventory（与账号列表同源），不再单独展示“账号列表”卡片
  const xaiN = (m.xai_total != null ? m.xai_total : (s.total||0));
  set("sXaiTotal", xaiN);
  const split = document.getElementById("sXaiSplit");
  if(split){
    split.textContent = "启用 " + (m.xai_enabled||0) + " / 停用 " + (m.xai_disabled||0)
      + " · 跟踪 " + (s.tracked||0) + " · 超限 " + (s.rolling_over||0);
  }
  const ts=document.getElementById("sTotalSub");
  if(ts){
    const ret = (s.returned!=null?s.returned:null);
    const tot = (s.total!=null?s.total:null);
    let extra = "plugin_auto · 库存未跟踪 " + (s.inventory_only||0);
    if(ret!=null && tot!=null && ret!==tot){ extra = "列表 "+ret+"/"+tot+" · " + extra; }
    if(s.view){ extra += " · view=" + s.view; }
    if(s.hot_hidden>0){ extra += " · 今日热账号截断 +" + s.hot_hidden; }
    ts.textContent = extra;
  }
  setToken("sQuotaTotal", m.quota_total_est||0);
  const qk = document.getElementById("sQuotaKnown");
  if(qk){
    const known=m.quota_known_accounts||0, total=xaiN, rest=m.unobserved_accounts!=null?m.unobserved_accounts:Math.max(0,total-known);
    const defL = m.default_limit_per_acct || 1000000;
    const mode = m.include_unobserved_est ? ("全量≈凭证×"+fmtToken(defL)) : "仅已知 rolling";
    qk.textContent = mode + " · 已知 " + known + " · 未观测 " + rest;
  }
  setToken("sUsedToday", (m.used_today_display != null ? m.used_today_display : (m.used_today||0)));
  const roll = document.getElementById("sRolling");
  if(roll){
    const ru = m.rolling_used_known||m.quota_used_known||0;
    const rl = m.rolling_limit_known||m.quota_limit_known||0;
    const over = rl>0 && ru>rl; roll.innerHTML = fmtTokenHTML(ru) + '<span class="muted" style="font-size:.75rem"> / </span>' + fmtTokenHTML(rl) + (over?' <span class="err" style="font-size:.75rem">超限</span>':'');
  }
  const rs = document.getElementById("sRollingSub");
  if(rs){ rs.textContent = "已知 " + (m.rolling_accounts||m.quota_known_accounts||0) + " 账号 · free-usage 滚动窗"; }
  let alertEl = document.getElementById("detailAlert");
  if(!alertEl){
    const tip = document.getElementById("recoverTip");
    if(tip && tip.parentNode){
      alertEl = document.createElement("div");
      alertEl.id = "detailAlert";
      alertEl.className = "guide";
      alertEl.style.display = "none";
      tip.parentNode.insertBefore(alertEl, tip);
    }
  }
  if(alertEl){
    if(m.detail_missing_alert){
      alertEl.style.display = "block";
      alertEl.textContent = m.detail_alert_message || ("连续 " + (m.zero_token_streak||0) + " 次成功请求缺少 token Detail");
    } else {
      alertEl.style.display = "none";
      alertEl.textContent = "";
    }
  }

  const dk = document.getElementById("sDayKey");
  if(dk){
    const req = m.requests_today||0;
    const est = m.estimated_today||0;
    const real = m.used_today||0;
    let extra = "请求 " + req;
    if(est>0) extra += " · 估 " + fmtToken(est);
    if(real>0) extra += " · 实 " + fmtToken(real);
    let bf = ""; if(m.backfill_source){ bf = " · 回补 " + m.backfill_source + (m.backfill_tokens_floor?(" "+fmtToken(m.backfill_tokens_floor)):""); }
    dk.textContent = extra + " · " + (m.day_key||"—") + bf;
  }
  const usedTotal = (m.used_total_display != null ? m.used_total_display : (m.used_total||0));
  setToken("sUsedTotal", usedTotal);
  const un = document.getElementById("sUsedNote");
  if(un) un.textContent = "真实token " + fmtToken(m.used_total||0) + " · actual " + fmtToken(m.quota_used_known||0) + " · 今日请求 " + (m.requests_today||0);
  const bar = document.getElementById("sUsedBar");
  if(bar){
    const total = Number(m.quota_total_est||0);
    const used = Number(usedTotal||0);
    let pct = total > 0 ? (used / total * 100) : 0;
    if(pct < 0) pct = 0;
    if(pct > 100) pct = 100;
    bar.style.width = pct.toFixed(1) + "%";
    bar.parentElement.title = "已用/总额度 " + pct.toFixed(1) + "%";
  }
  const nowLabel = document.getElementById("nowLabel");
  if(nowLabel){
    const age = LAST_FETCH_AT ? Math.max(0, Math.floor((Date.now()-LAST_FETCH_AT)/1000)) : 0;
    nowLabel.textContent = "服务器 " + fmtTime(d.now_ms) + " · 刷新于 " + age + "s 前";
  }
  const tip = document.getElementById("recoverTip");
  if(tip){
    const autoN = s.auto_disabled||0;
    const dueN = s.recover_due||0;
    if(autoN>0 && dueN===0){
      tip.style.display = "block";
      tip.innerHTML = "说明：当前 <b>"+autoN+"</b> 个账号程序自动停用，恢复时间未到点（rolling 24h）。倒计时每秒刷新；状态栏随账号表重算。";
    } else if(dueN>0){
      tip.style.display = "block";
      tip.innerHTML = "有 <b>"+dueN+"</b> 个账号已到恢复时间，等待 tick 启用（可点“立即扫描恢复”）。";
    } else {
      tip.style.display = "none";
      tip.innerHTML = "";
    }
  }
  // delete history is NOT refreshed here (avoids 1s blink with patrol poll)
  // live countdown cells without full re-fetch
  const nowMs = Date.now();

  document.querySelectorAll("[data-recover-ms]").forEach(function(el){
    const ms = Number(el.getAttribute("data-recover-ms")||0);
    const cd = fmtCountdown(ms, nowMs);
    el.textContent = cd.text;
    el.className = cd.due ? "err" : "muted";
    el.style.fontSize = ".78rem";
  });
}

let ACC_PAGE = 1;
let ACC_SEARCH_TIMER = null;
function onAccSearch(){
  if(ACC_SEARCH_TIMER) clearTimeout(ACC_SEARCH_TIMER);
  ACC_SEARCH_TIMER = setTimeout(function(){ ACC_PAGE = 1; renderAccountTable(); }, 200);
}
function accountHealth(a, nowMs){
  if(a.health) return a.health;
  if(a.state === "auto_disabled"){
    if(a.recover_at_ms > 0 && nowMs >= a.recover_at_ms) return "due";
    return "auto";
  }
  if(a.state === "user_manual_disabled" || a.disable_source === "user_manual") return "manual";
  if(a.state === "cpa_disabled") return "cpa_disabled";
  if(a.rolling_over || (a.rolling_limit>0 && a.rolling_actual>a.rolling_limit)) return "over";
  if((a.used_today||0) > 0 || (a.requests_today||0) > 0) return "hot";
  if(a.state === "inventory") return "inventory";
  return "active";
}
function isFocusHealth(h){
  return h==="due" || h==="auto" || h==="manual" || h==="over" || h==="hot" || h==="cpa_disabled";
}
function renderAccountTable(){
  const d = LAST_STATE;
  if(!d) return;
  const list = sortAccounts(d.accounts || []);
  const filter = (document.getElementById("accFilter")||{}).value || "focus";
  const q = ((document.getElementById("accSearch")||{}).value || "").toLowerCase().trim();
  const pageSize = Math.max(10, parseInt(((document.getElementById("accPageSize")||{}).value || "50"), 10) || 50);
  const nowMs = Date.now();
  const filtered = list.filter(function(a){
    const h = accountHealth(a, nowMs);
    if(filter === "focus" && !isFocusHealth(h)) return false;
    if(filter === "auto" && h !== "auto" && h !== "due") return false;
    if(filter === "due" && h !== "due") return false;
    if(filter === "manual" && h !== "manual") return false;
    if(filter === "active" && h !== "active") return false;
    if(filter === "hot" && h !== "hot" && !((a.used_today||0)>0 || (a.requests_today||0)>0)) return false;
    if(filter === "inventory" && h !== "inventory") return false;
    if(filter === "cpa_disabled" && h !== "cpa_disabled") return false;
    if(filter === "over" && !(a.rolling_over || (a.rolling_limit>0 && a.rolling_actual>a.rolling_limit) || h==="over")) return false;
    if(q){
      const hay = ((a.account||"")+" "+(a.file_name||"")+" "+(a.auth_index||"")).toLowerCase();
      if(hay.indexOf(q) < 0) return false;
    }
    return true;
  });
  const totalFiltered = filtered.length;
  const totalPages = Math.max(1, Math.ceil(totalFiltered / pageSize));
  if(ACC_PAGE > totalPages) ACC_PAGE = totalPages;
  if(ACC_PAGE < 1) ACC_PAGE = 1;
  const start = (ACC_PAGE - 1) * pageSize;
  const pageItems = filtered.slice(start, start + pageSize);
  const info = document.getElementById("accPageInfo");
  if(info){
    const allN = (d.accounts||[]).length;
    const end = totalFiltered ? Math.min(totalFiltered, start + pageItems.length) : 0;
    info.textContent = (totalFiltered ? ((start+1)+"-"+end) : "0") + " / 筛选 "+totalFiltered + " · 全量 "+allN + " · 第 "+ACC_PAGE+"/"+totalPages+" 页";
  }
  const body = document.getElementById("accBody");
  if(!body) return;
  if(!pageItems.length){
    body.innerHTML = '<tr><td colspan="5" class="muted">无匹配账号（可切换「全部账号」或清空搜索）</td></tr>';
    return;
  }
  body.innerHTML = pageItems.map(function(a){
    const health = accountHealth(a, nowMs);
    const cd = fmtCountdown(a.recover_at_ms, nowMs);
    const reason = humanReason(a);
    const srcLabel = sourceLabel(a.disable_source);
    const over = !!(a.rolling_over || (a.rolling_limit>0 && a.rolling_actual>a.rolling_limit));
    const usedMain = fmtToken(a.used_today||0);
    let chips = '<span class="chip">请求 '+(a.requests_today||0)+'</span>';
    if(a.rolling_actual!=null){
      chips += '<span class="chip'+(over?' over':'')+'">滚 '+fmtToken(a.rolling_actual)+'/'+(a.rolling_limit?fmtToken(a.rolling_limit):'?')+(over?' 超限':'')+'</span>';
    }
    if(a.last_tokens){ chips += '<span class="chip hot">近 '+fmtToken(a.last_tokens)+'</span>'; }
    if(a.cpa_success!=null){ chips += '<span class="chip">CPA成功 '+a.cpa_success+'</span>'; }
    let rowClass = "";
    if(over) rowClass = "row-over";
    else if(health==="due") rowClass = "row-due";
    else if(health==="auto") rowClass = "row-auto";
    const name = a.account || a.file_name || a.auth_index || "—";
    const file = a.file_name || "";
    const recoverMain = a.recover_at_ms ? fmtTime(a.recover_at_ms) : "—";
    const sig = String(a.signal||"").replace(/^body\.error\.code=/,"");
    return '<tr data-auth="'+esc(a.auth_index||"")+'" class="'+rowClass+'">'+
      '<td><div class="acc-name">'+esc(name)+'</div>'+(file?'<div class="acc-file">'+esc(file)+'</div>':'')+'</td>'+
      '<td><div class="stack">'+stateTag(a.state,a.disable_source,health)+
        (srcLabel!=="—"?'<div class="muted" style="font-size:.72rem">来源 '+esc(srcLabel)+'</div>':'')+
      '</div></td>'+
      '<td><div class="stack mono">'+esc(recoverMain)+
        '<div data-recover-ms="'+(a.recover_at_ms||0)+'" class="'+(cd.due?'err':'muted')+'" style="font-size:.78rem">'+esc(cd.text)+'</div>'+
      '</div></td>'+
      '<td><div class="stack"><div class="mono" style="font-weight:700">'+esc(usedMain)+'</div><div class="acc-meta">'+chips+'</div></div></td>'+
      '<td><div class="reason-main">'+esc(reason)+'</div>'+(sig?'<div class="reason-sub">'+esc(sig)+'</div>':'')+'</td></tr>';
  }).join("");
}


async function exportUsage(){
  const r = await api("export");
  if(!r||!r.ok){ alert("导出失败"); return; }
  const blob = new Blob([JSON.stringify(r.result,null,2)], {type:"application/json"});
  const a = document.createElement("a");
  a.href = URL.createObjectURL(blob);
  a.download = "xai-quota-export-"+((r.result&&r.result.day_key)||"day")+".json";
  a.click();
  URL.revokeObjectURL(a.href);
}
async function runBackfill(){
  const r = await api("backfill", {method:"POST", body:{}});
  if(!r || !r.ok){ alert("回补失败: "+(r&&r.error?JSON.stringify(r.error):"无响应")); return; }
  const d = r.result || {};
  alert((d.ok?"回补完成":"回补返回")+"\nCPAMP tokens="+fmtToken(d.cpamp_tokens||0)+"\ncalls="+ (d.cpamp_calls||0)+"\napplied="+d.applied);
  loadState();
}
async function runHealth(){
  const r = await api("health");
  if(!r || !r.ok){ alert("自检失败"); return; }
  const h = r.result || {};
  alert("自检 "+(h.ok?"OK":"异常")+"\nenabled="+h.enabled+"\nmanagement="+h.management_configured+"\nauth_list="+h.auth_list_ok+" xai="+h.xai_auth_files+"\ndetail_ok="+h.detail_tokens_healthy+" streak="+h.zero_token_streak+"\ncpamp="+h.cpamp_configured+" webhook="+h.webhook_configured+"\nused_today="+fmtToken(h.used_today||0));
}

function onAccFilterChange(){
  // 切换到全量/库存时重新请求后端；关注类筛选可在本地完成
  const needAll = currentStateView() === "all";
  const haveAll = !!(LAST_STATE && LAST_STATE.view === "all");
  if(needAll && !haveAll){ loadState(); return; }
  if(!needAll && haveAll){ loadState(); return; }
  renderAccountTable();
}
function currentStateView(){
  const f = ((document.getElementById("accFilter")||{}).value || "focus");
  // 全部账号才拉全量；其余筛选先拉 focus 子集，避免 5k+ 卡死
  return (f === "all" || f === "inventory") ? "all" : "focus";
}
let LOAD_STATE_INFLIGHT = false;
function clearLoadingUI(errMsg){
  const txt = document.getElementById("enBtnText");
  if(txt && /加载中/.test(txt.textContent||"")){
    txt.textContent = errMsg ? "加载失败" : "—";
  }
  const meta = document.getElementById("cfgMeta");
  if(meta && /加载中/.test(meta.textContent||"")){
    meta.textContent = errMsg ? ("加载失败: " + errMsg) : "—";
  }
}
async function loadState(){
  if(LOAD_STATE_INFLIGHT) return;
  LOAD_STATE_INFLIGHT = true;
  const view = currentStateView();
  const body = document.getElementById("accBody");
  try {
    if(body && !LAST_STATE){
      body.innerHTML = '<tr><td colspan="5" class="muted">加载中…（view='+view+'）</td></tr>';
    }
    const r = await api("state?view="+encodeURIComponent(view), {timeout_ms: 20000});
    if(!r || !r.ok){
      const err = (r&&r.error? (typeof r.error==="string"?r.error:(r.error.message||"error")) : "无响应");
      clearLoadingUI(err);
      if(body){
        body.innerHTML = '<tr><td colspan="5" class="err">加载失败: '+esc(String(err))+'。请确认 Management Key；选「全部账号」更慢。</td></tr>';
      }
      return;
    }
    const d = normalizeStatePayload(r.result || {});
    LAST_STATE = d;
    LAST_FETCH_AT = Date.now();
    const en = !!d.enabled;
    applyEnabledUI(en);
    const vb = document.getElementById("verBadge");
    if(vb) vb.textContent = "v" + (d.version || "?");
    paintStatusBar(d);
    renderDelHistFromState(d.delete_history);
    const s = d.summary || {};
    const tip = document.getElementById("recoverTip");
    if(tip && s.hot_hidden > 0){
      // non-blocking note appended via status sub line
    }
    const cfg = d.config || {};
    const meta = document.getElementById("cfgMeta");
    if(meta){
      meta.textContent =
        "management_url=" + (cfg.management_url||"(empty)") +
        " · key_set=" + !!cfg.management_key_set +
        " · tick=" + (cfg.tick_seconds||"-") + "s" +
        " · max_reset=" + (cfg.max_reset_seconds||"-") + "s" +
        " · state_path=" + (cfg.state_path||"-") +
        " · cpamp=" + (cfg.cpamp_url||"(未配)") +
        " · min_reset=" + (cfg.min_reset_seconds||0) +
        " · unobs_est=" + !!cfg.include_unobserved_quota_est +
        (s.hot_hidden>0 ? (" · focus热账号截断 "+s.hot_shown+"/"+s.hot_total) : "");
    }
    // patrol config display
    var ph = document.getElementById("patrolCfgHint");
    if(ph){
      var pen = cfg.patrol_enabled;
      document.getElementById("cfgPatrolEn").checked = !!pen;
      document.getElementById("cfgPatrolInt").value = cfg.patrol_interval || 3600;
      document.getElementById("cfgPatrolTO").value = cfg.patrol_timeout || 15;
      document.getElementById("cfgPatrolDir").value = cfg.patrol_auth_dir || "";
      document.getElementById("cfgPatrolCon").value = cfg.patrol_concurrency || 8;
      document.getElementById("cfgPatrolBatch").value = cfg.patrol_batch_size || 0;
      var pm = cfg.patrol_model || "grok-4.5-build-free";
      ensurePatrolModelOption(pm);
      document.getElementById("cfgPatrolModel").value = pm;
      document.getElementById("cfgPatrolAutoModel").checked = !!cfg.patrol_auto_model_switch;
      try{ refreshPatrolModels(true); }catch(e){}
      document.getElementById("cfgPatrolProxy").value = ""; // sensitive, not echoed
      ph.textContent = pen
        ?("已启用 · 周期"+(cfg.patrol_interval||"?")+"s · 并发"+(cfg.patrol_concurrency||"?")+" · 模型"+(cfg.patrol_model||"?")+" · 自动换模"+(cfg.patrol_auto_model_switch?"开":"关")+" · 目录"+(cfg.patrol_auth_dir||"?"))
        :("未启用 · 模型"+(cfg.patrol_model||"grok-4.5-build-free")+" · 可勾选后点保存配置");
    }
    const list = sortAccounts(d.accounts || []);
    d.accounts = list;
    LAST_TABLE_FP = accountFingerprint(list);
    renderAccountTable();
  } finally {
    LOAD_STATE_INFLIGHT = false;
  }
}
function sortAccounts(list){
  const nowMs = Date.now();
  function rank(x){
    const h = accountHealth(x, nowMs);
    if(h === "due") return 0;
    if(h === "auto") return 1;
    if(h === "manual") return 2;
    if(h === "cpa_disabled") return 3;
    if(h === "over") return 4;
    if(h === "hot") return 5;
    if(h === "inventory") return 6;
    return 7;
  }
  return (list || []).slice().sort(function(a,b){
    const ra = rank(a), rb = rank(b);
    if(ra !== rb) return ra - rb;
    const ua = Number(a.used_today||0), ub = Number(b.used_today||0);
    if(ua !== ub) return ub - ua;
    const ma = a.recover_at_ms||0, mb = b.recover_at_ms||0;
    if(ma !== mb){
      if(ma === 0) return 1;
      if(mb === 0) return -1;
      return ma - mb;
    }
    const aa = a.account||a.file_name||"";
    const bb = b.account||b.file_name||"";
    if(aa !== bb) return aa < bb ? -1 : 1;
    return (a.auth_index||"") < (b.auth_index||"") ? -1 : 1;
  });
}
function accountFingerprint(list){
  return (list||[]).map(function(a){
    return [a.auth_index,a.state,a.disable_source,a.recover_at_ms,a.disabled_at_ms,a.reason,a.signal,a.account,a.file_name].join("|");
  }).join("\n");
}
function esc(s){ return String(s==null?"":s).replace(/[&<>"']/g,function(c){return ({"&":"&amp;","<":"&lt;",">":"&gt;","\"":"&quot;","'":"&#39;"}[c]);}); }
function saveKey(){
  const v = document.getElementById("cfgKey").value.trim();
  setMgmtKey(v);
  loadState();
}

function ensurePatrolModelOption(id){
  if(!id) return;
  var sel = document.getElementById("cfgPatrolModel");
  if(!sel) return;
  for(var i=0;i<sel.options.length;i++){ if(sel.options[i].value===id) return; }
  var opt = document.createElement("option");
  opt.value = id; opt.textContent = id;
  sel.appendChild(opt);
}
async function refreshPatrolModels(silent){
  var hint = document.getElementById("patrolModelHint");
  try{
    var r = await api("patrol/models");
    var models = (r && r.models) || [];
    var sel = document.getElementById("cfgPatrolModel");
    var cur = sel ? sel.value : "";
    if(r && r.patrol_model) cur = r.patrol_model;
    if(sel){
      var keep = cur || (r && r.default) || "grok-4.5-build-free";
      sel.innerHTML = "";
      (models||[]).forEach(function(m){
        var opt = document.createElement("option");
        opt.value = m;
        opt.textContent = m + (String(m).indexOf("free")>=0 ? " · 免费档推荐" : "");
        sel.appendChild(opt);
      });
      ensurePatrolModelOption(keep);
      sel.value = keep;
    }
    if(hint){
      var src = (r && r.source) || "?";
      var err = (r && r.error) ? (" · "+r.error) : "";
      hint.textContent = "列表来源: "+src+err+" · 当前将用于巡查探测";
    }
  }catch(e){
    if(hint) hint.textContent = "模型列表加载失败: "+(e&&e.message?e.message:e);
  }
}
async function savePatrolConfig(){
  var body = {
    patrol_enabled: document.getElementById("cfgPatrolEn").checked,
    patrol_interval: Number(document.getElementById("cfgPatrolInt").value)||3600,
    patrol_timeout: Number(document.getElementById("cfgPatrolTO").value)||15,
    patrol_auth_dir: document.getElementById("cfgPatrolDir").value.trim(),
    patrol_proxy_url: document.getElementById("cfgPatrolProxy").value.trim(),
    patrol_concurrency: Number(document.getElementById("cfgPatrolCon").value)||8,
    patrol_batch_size: Number(document.getElementById("cfgPatrolBatch").value)||0,
    patrol_model: (document.getElementById("cfgPatrolModel").value||"").trim()||"grok-4.5-build-free",
    patrol_auto_model_switch: document.getElementById("cfgPatrolAutoModel").checked
  };
  var ph = document.getElementById("patrolCfgHint");
  if(ph) ph.textContent = "保存中…";
  try {
    var r = await api("patrol/config", {method:"POST", body: body});
    if(!r || !r.ok){
      if(ph) ph.textContent = "保存失败: " + JSON.stringify(r&&r.error||r);
      alert("巡查配置保存失败: " + JSON.stringify(r&&r.error||r));
      return;
    }
    if(ph) ph.textContent = "已保存 · " + (body.patrol_enabled?"已启用":"未启用") + " · 周期"+body.patrol_interval+"s" + " · 并发"+body.patrol_concurrency + " · 模型"+(body.patrol_model||"");
    loadState();
  } catch(e){
    if(ph) ph.textContent = "保存异常: " + (e&&e.message?e.message:e);
    alert("巡查配置保存异常: " + (e&&e.message?e.message:e));
  }
}
function applyEnabledUI(en){
  const badge = document.getElementById("enBadge");
  const btn = document.getElementById("btnToggle");
  const txt = document.getElementById("enBtnText");
  const dot = document.getElementById("enDot");
  if(badge){
    badge.textContent = en ? "运行中" : "已停用";
    badge.className = en ? "badge on" : "badge off";
  }
  if(btn){
    btn.className = en ? "on" : "off";
    btn.setAttribute("aria-pressed", en ? "true" : "false");
  }
  if(txt){ txt.textContent = en ? "已启用 · 点击关闭" : "已停用 · 点击开启"; }
  if(dot){ dot.className = en ? "status-dot on" : "status-dot off"; }
}
async function togglePlugin(){
  const btn = document.getElementById("btnToggle");
  if(btn){ btn.disabled = true; }
  try{
    let cur = !!(LAST_STATE && LAST_STATE.enabled);
    if(!LAST_STATE){
      const s = await api("state?view=focus", {timeout_ms: 15000});
      if(!s || !s.ok){ clearLoadingUI("toggle"); alert("读取状态失败，请检查 Management Key"); return; }
      cur = !!(s.result && s.result.enabled);
    }
    const want = !cur;
    // optimistic UI
    applyEnabledUI(want);
    const r = await api("toggle", {method:"POST", body:{enabled: want}});
    if(!r || !r.ok){
      applyEnabledUI(cur);
      alert("开关失败: " + (r && r.error ? (typeof r.error==="string"?r.error:(r.error.message||"未知错误")) : "无响应"));
      return;
    }
    const en = (r.result && typeof r.result.enabled === "boolean") ? r.result.enabled : want;
    applyEnabledUI(en);
    setTimeout(loadState, 200);
  } finally {
    if(btn){ btn.disabled = false; }
  }
}
async function runTick(){
  const r = await api("run", {method:"POST"});
  if(!r || !r.ok){ alert("扫描失败"); return; }
  setTimeout(loadState, 300);
}
(function init(){
  const k = mgmtKey();
  if(k) document.getElementById("cfgKey").value = k;
  loadState();
  // full sync with server
  setInterval(loadState, 15000);
  // local status bar + countdown every second
  setInterval(function(){ if(LAST_STATE) paintStatusBar(LAST_STATE); }, 1000);
  // visibility resume
  document.addEventListener("visibilitychange", function(){ if(!document.hidden) loadState(); });
})();
// ===== Delete History: ONLY updated by loadState (not paintPatrol / 1s paintStatusBar) =====
var DEL_HIST_FP = "";
function renderDelHistFromState(items){
  var dhBody = document.getElementById("patrolDelBody");
  if(!dhBody) return;
  if(!Array.isArray(items)) items = [];
  var list = items.slice(0,20);
  var fp = list.map(function(x){
    return String(x.auth_index||x.file_name||"") + ":" + String(x.deleted_at_ms||0);
  }).join("|");
  if(fp === DEL_HIST_FP) return;
  DEL_HIST_FP = fp;
  if(!list.length){
    dhBody.innerHTML = '<tr><td colspan="4" class="muted" style="padding:.5rem">暂无删除记录</td></tr>';
    return;
  }
  dhBody.innerHTML = list.map(function(x){
    var src = String(x.reason||"").indexOf("patrol:")>=0 ? "巡查" : "额度";
    return '<tr style="border-bottom:1px solid #f1f5f9">' +
      '<td style="padding:.2rem;white-space:nowrap;overflow:hidden;text-overflow:ellipsis">'+fmtTime(x.deleted_at_ms)+'</td>' +
      '<td style="overflow:hidden;text-overflow:ellipsis;white-space:nowrap">'+esc(x.account||x.file_name||x.auth_index||"?")+'</td>' +
      '<td style="font-weight:600;white-space:nowrap">'+src+'</td>' +
      '<td style="color:var(--muted);overflow:hidden;text-overflow:ellipsis;white-space:nowrap">'+esc(x.reason||"")+'</td>' +
    '</tr>';
  }).join("");
}
// ===== Patrol =====
var PATROL_POLL = null;
function extractPatrol(r){
  if(!r) return null;
  // api() returns {ok, result, error}; result may be body object or nested.
  var body = (r.result != null) ? r.result : r;
  if(body && body.patrol) return body.patrol;
  if(body && (body.running != null || body.total_probed != null)) return body;
  if(r.patrol) return r.patrol;
  return null;
}
function paintPatrol(p, r){
  if(!p) return;
  document.getElementById("patrolProgress").style.display = "";
  document.getElementById("patrolLog").style.display = "";
  var probed = Number(p.total_probed||0);
  var alive = Number(p.total_alive||0);
  var deleted = Number(p.total_deleted||0);
  var errors = Number(p.total_errors||0);
  var skipped = Number(p.total_skipped||0);
  var candidates = Number(p.total_candidates||0);
  var workers = Number(p.workers||0);
  document.getElementById("patrolProbed").textContent = probed;
  document.getElementById("patrolAlive").textContent = alive;
  document.getElementById("patrolDeleted").textContent = deleted;
  document.getElementById("patrolErrors").textContent = errors;
  var denom = candidates > 0 ? candidates : (probed || 1);
  var pct = Math.min(100, Math.round(probed / denom * 100));
  document.getElementById("patrolBar").style.width = pct + "%";
  var st = document.getElementById("patrolStatus");
  var extra = "";
  if(workers > 0) extra += " · " + workers + "线程";
  if(candidates > 0) extra += " · 候选 " + candidates;
  if(skipped > 0) extra += " · 跳过 " + skipped;
  if(p.running){
    st.textContent = "巡查中... 已探测 " + probed + "/" + (candidates||"?") + extra;
    st.style.color = "var(--accent)";
    document.getElementById("patrolBtn").textContent = "巡查中...";
    document.getElementById("patrolBtn").disabled = true;
    document.getElementById("patrolStopBtn").style.display = "";
  } else {
    var err = p.last_error ? (" · 错误: " + p.last_error) : "";
    st.textContent = "已完成 · 探测 " + probed + " · 删除 " + deleted + " · 存活 " + alive + extra + err;
    st.style.color = "var(--muted)";
    document.getElementById("patrolBtn").textContent = "启动巡查";
    document.getElementById("patrolBtn").disabled = false;
    document.getElementById("patrolStopBtn").style.display = "none";
  }
  var log = p.recent_log || [];
  var tbody = document.getElementById("patrolLogBody");
  var logFp = log.map(function(e){ return (e.auth_index||"")+":"+(e.time_ms||0)+":"+(e.action||"")+":"+(e.http_code||""); }).join("|");
  if(logFp !== (window._PATROL_LOG_FP||"")){
    window._PATROL_LOG_FP = logFp;
    tbody.innerHTML = log.map(function(e){
      var color = e.action === "deleted" ? "var(--err)" : e.action === "error" ? "var(--warn)" : (e.action === "cooldown_skip" || e.action === "cooldown") ? "var(--muted)" : e.action === "reenabled" ? "var(--accent)" : "var(--ok)";
      return '<tr style="border-bottom:1px solid #f1f5f9">' +
        '<td style="padding:.2rem;white-space:nowrap">'+fmtTime(e.time_ms)+'</td>' +
        '<td style="max-width:140px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">'+esc(e.account||e.file_name||e.auth_index||"?")+'</td>' +
        '<td style="color:'+color+';font-weight:600;white-space:nowrap">'+esc(patrolActionLabel(e.action))+'</td>' +
        '<td>'+(e.http_code||"-")+'</td>' +
        '<td style="color:var(--muted);max-width:260px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">'+esc(e.reason||"")+'</td>' +
      '</tr>';
    }).join("");
  }
  // delete history intentionally not updated from patrol poll (stable UI)
}
async function patrolSpendingStart(){
  var btn = document.getElementById("patrolSpendBtn");
  var main = document.getElementById("patrolBtn");
  if(btn){ btn.disabled = true; btn.textContent = "复查中..."; }
  if(main) main.disabled = true;
  document.getElementById("patrolStopBtn").style.display = "";
  document.getElementById("patrolProgress").style.display = "";
  document.getElementById("patrolLog").style.display = "";
  document.getElementById("patrolStatus").textContent = "启动 spending 复查...";
  try {
    var r = await api("patrol/spending", {method:"POST"});
    if(!r || !r.ok){ alert("冷却复查启动失败: "+JSON.stringify(r&&r.error||r)); if(btn){btn.disabled=false;btn.textContent="仅复查冷却号";} if(main) main.disabled=false; return; }
    paintPatrol(extractPatrol(r), r);
    if(PATROL_POLL) clearInterval(PATROL_POLL);
    PATROL_POLL = setInterval(patrolPoll, 1500);
    patrolPoll();
  } catch(e){
    alert("冷却复查异常: "+(e&&e.message?e.message:e));
    if(btn){btn.disabled=false;btn.textContent="仅复查冷却号";}
    if(main) main.disabled=false;
  }
}
async function patrolStart(){
  var btn = document.getElementById("patrolBtn");
  btn.disabled = true;
  btn.textContent = "巡查中...";
  document.getElementById("patrolStopBtn").style.display = "";
  document.getElementById("patrolProgress").style.display = "";
  document.getElementById("patrolLog").style.display = "";
  document.getElementById("patrolStatus").textContent = "启动中...";
  try {
    var r = await api("patrol", {method:"POST"});
    if(!r || !r.ok){ alert("巡查启动失败: "+JSON.stringify(r&&r.error||r)); btn.disabled=false; btn.textContent="启动巡查"; return; }
    paintPatrol(extractPatrol(r), r);
    if(PATROL_POLL) clearInterval(PATROL_POLL);
    PATROL_POLL = setInterval(patrolPoll, 1500);
    patrolPoll();
  } catch(e){
    alert("巡查启动异常: "+(e&&e.message?e.message:e));
    btn.disabled = false;
    btn.textContent = "启动巡查";
  }
}
async function patrolStop(){
  await api("patrol/stop", {method:"POST"});
  setTimeout(patrolPoll, 300);
}
async function patrolPoll(){
  var r = await api("patrol/status", {method:"GET"});
  if(!r || !r.ok) return;
  var p = extractPatrol(r);
  if(!p) return;
  paintPatrol(p, r);
  if(!p.running && PATROL_POLL){
    clearInterval(PATROL_POLL);
    PATROL_POLL = null;
    // one refresh after sweep ends so delete history picks up new deletes (not during poll)
    try{ loadState(); }catch(e){}
  }
}
// page open: sync last patrol status once
setTimeout(function(){ try{ patrolPoll(); }catch(e){} }, 800);
</script></body></html>`
	return []byte(tpl)
}


func htmlUnescapeBasic(s string) string {
	replacer := strings.NewReplacer(
		"&#34;", "\"",
		"&quot;", "\"",
		"&#39;", "'",
		"&apos;", "'",
		"&lt;", "<",
		"&gt;", ">",
		"&amp;", "&",
	)
	return replacer.Replace(s)
}
