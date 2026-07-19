package main

import (
	"context"
	"embed"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mortal/cpa-xai-quota-guard/internal/xaiquota"
)

//go:embed web/console.html
var consoleFS embed.FS

func renderConsole() []byte {
	b, err := consoleFS.ReadFile("web/console.html")
	if err != nil {
		return []byte("<html><body>console missing</body></html>")
	}
	return b
}

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
			{Method: "POST", Path: "/cpa-xai-quota-guard/metrics/reset-today", Description: "清零日历今日已用/请求（不改累计与滚动快照；用于 0.2.18 前污染校准）"},
			{Method: "GET", Path: "/cpa-xai-quota-guard/deletes", Description: "最近删除历史"},
			{Method: "GET", Path: "/cpa-xai-quota-guard/export", Description: "导出今日用量 JSON"},
			{Method: "POST", Path: "/cpa-xai-quota-guard/toggle", Description: "开关 enabled"},
			{Method: "POST", Path: "/cpa-xai-quota-guard/bind-key", Description: "保存 Management Key 到进程（与浏览器同一把，纯 CPA）"},
			{Method: "POST", Path: "/cpa-xai-quota-guard/run", Description: "手动触发恢复扫描"},
		{Method: "POST", Path: "/cpa-xai-quota-guard/patrol", Description: "全量巡查：仅当前启用的 xAI 凭证"},
		{Method: "POST", Path: "/cpa-xai-quota-guard/patrol/spending", Description: "仅复核：plugin_auto 冷却号(429 free-usage 与 402 spending)"},
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
	// Pure CPA: browser X-Management-Key is the process key for auth-files ops.
	adoptManagementKeyFromHeaders(req.Headers)

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
	case "metrics/reset-today":
		return metricsResetTodayResponse(req)
	case "deletes":
		return deletesResponse()
	case "export":
		return exportResponse()
	case "toggle":
		return toggleResponse(req)
	case "bind-key":
		return bindKeyResponse(req)
	case "run":
		return runResponse()
	case "patrol":
		return patrolResponse(req)
	case "patrol/spending":
		return patrolSpendingResponse(req)
	case "patrol/status":
		return patrolStatusResponse(req)
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
	inv, invErr := newMgmtAuth(cfg).List()
	invOK := invErr == nil
	if invErr != nil {
		inv = nil
	}
	byIndex := map[string]xaiquota.AuthFile{}
	inCPA := map[string]bool{}
	xaiTotal, xaiEnabled, xaiDisabled := 0, 0, 0
	tierFreeN, tierSuperN, tierHeavyN, tierUnknownN := 0, 0, 0, 0
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
	// Drop plugin state only when inventory list succeeded.
	// BUGFIX: on list failure inv/inCPA was empty and PruneMissingInventory wiped ALL
	// plugin_auto cooldowns (auto-disable markers "lost" after refresh/restart race).
	if invOK {
		// Extra guard: never treat a sudden empty inventory as "delete everything"
		// when we still track plugin_auto cooldowns (transient auth-files glitch).
		autoTracked := 0
		for _, rec := range tracked {
			if rec.State == xaiquota.StateAutoDisabled && rec.DisableSource == xaiquota.SourcePluginAuto {
				autoTracked++
			}
		}
		if len(inCPA) == 0 && autoTracked > 0 {
			// keep state; do not prune
		} else if n := g.PruneMissingInventory(inCPA); n > 0 {
			tracked = g.Snapshot()
		}
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
		tierCl := xaiquota.ClassifyAuthTier(f, nil)
		switch tierCl.Tier {
		case xaiquota.TierHeavy:
			tierHeavyN++
		case xaiquota.TierSuper:
			tierSuperN++
		case xaiquota.TierFree:
			tierFreeN++
		default:
			tierUnknownN++
		}
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
			"tier":           tierCl.Tier,
			"tier_source":    tierCl.Source,
			"tier_detail":    tierCl.Detail,
			"tier_protected": xaiquota.IsProtectedTier(tierCl.Tier, nil),
		}
		if !inCPA[k] {
			item["in_inventory"] = false
		}

		if hasRec {
			item["state"] = rec.State
			item["disable_source"] = rec.DisableSource
			effRecover := xaiquota.EffectiveRecoverAtMS(&rec)
			item["recover_at_ms"] = effRecover
			item["disabled_at_ms"] = rec.DisabledAtMS
			item["pre_disabled"] = rec.PreDisabled
			item["owner"] = rec.Owner
			item["reason"] = htmlUnescapeBasic(rec.Reason)
			item["signal"] = rec.Signal
			item["updated_at_ms"] = rec.UpdatedAtMS
			item["last_cooldown_signal"] = rec.LastCooldownSignal
			item["last_cooldown_reason"] = htmlUnescapeBasic(rec.LastCooldownReason)
			item["last_cooldown_disabled_at_ms"] = rec.LastCooldownDisabledAtMS
			item["last_cooldown_recover_at_ms"] = rec.LastCooldownRecoverAtMS
			item["spending_suspect_hits"] = rec.SpendingSuspectHits
			item["spending_suspect_first_ms"] = rec.SpendingSuspectFirstMS
			item["spending_suspect_last_ms"] = rec.SpendingSuspectLastMS
			if rec.State == xaiquota.StateAutoDisabled {
				autoN++
				if effRecover > 0 && now >= effRecover {
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
	liveAuth := map[string]bool{}
	for k := range byIndex {
		liveAuth[k] = true
	}
	metrics := g.MetricsWithInventoryLive(xaiTotal, xaiEnabled, xaiDisabled, liveAuth)
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
			"management_url":               resolveManagementBaseURL(cfg.ManagementURL),
			"management_url_raw":           cfg.ManagementURL,
			"management_url_configured":    strings.TrimSpace(cfg.ManagementURL) != "",
			"management_source":            managementURLSource(cfg.ManagementURL),
			"management_key_set":           resolveManagementKey(cfg.ManagementKey) != "",
			"management_key_from_config":   strings.TrimSpace(cfg.ManagementKey) != "",
			"management_key_source":        managementKeySource(cfg.ManagementKey),
			"management_key_from_browser":  getRuntimeManagementKey() != "" && strings.TrimSpace(cfg.ManagementKey) == "",
			"state_path":                   cfg.StatePath,
			"cpamp_url":                    cfg.CPAMPURL,
			"cpamp_admin_key_set":          cfg.CPAMPAdminKey != "",
			"cpamp_optional":               true,
			"pure_cpa_mode":               strings.TrimSpace(cfg.CPAMPURL) == "" || strings.TrimSpace(cfg.CPAMPAdminKey) == "",
			"webhook_url_set":              cfg.WebhookURL != "",
			"patrol_enabled":               cfg.PatrolEnabled,
			"patrol_interval":              cfg.PatrolInterval,
			"patrol_timeout":               cfg.PatrolTimeout,
			"patrol_auth_dir":              xaiquota.ResolvePatrolAuthDir(cfg.PatrolAuthDir),
			"patrol_auth_dir_raw":          cfg.PatrolAuthDir,
			"patrol_concurrency":           cfg.PatrolConcurrency,
			"patrol_batch_size":            cfg.PatrolBatchSize,
			"patrol_model":                cfg.PatrolModel,
			"patrol_auto_model_switch":   cfg.PatrolAutoModelSwitch,
			"patrol_initial_delay_sec":  cfg.PatrolInitialDelaySec,
			"patrol_proxy_url":           cfg.PatrolProxyURL,
			"patrol_proxy_set":           cfg.PatrolProxyURL != "",
			"spending_confirm_hits":        cfg.SpendingConfirmHits,
			"spending_confirm_window_sec":  cfg.SpendingConfirmWindowSec,
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
			"tier_free":        tierFreeN,
			"tier_super":       tierSuperN,
			"tier_heavy":       tierHeavyN,
			"tier_unknown":     tierUnknownN,
			"hot_total":        hotTotal,
			"hot_shown":        hotShown,
			"hot_hidden":       hotHidden,
			"focus_hot_cap":    focusHotCap,
		},
		"metrics":         metrics,
		"delete_history":  g.ListDeletes(50),
		"action_history":  g.ListActions(80),
		"patrol":          g.PatrolStatus(),
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
	metrics := g.MetricsWithInventoryLive(xaiTotal, xaiEnabled, xaiDisabled, nil)
	return jsonResponse(map[string]any{
		"exported_at_ms": time.Now().UnixMilli(),
		"plugin":         pluginID,
		"version":        pluginVer,
		"day_key":        metrics.DayKey,
		"metrics":        metrics,
		"accounts":       g.Snapshot(),
		"usage_by_auth":  g.UsageByAuthMap(),
		"delete_history": g.ListDeletes(100),
		"action_history": g.ListActions(100),
		"patrol":         g.PatrolStatus(),
		"cpamp_url":      cfg.CPAMPURL,
	})
}

func healthResponse() ([]byte, error) {
	return jsonResponse(guard().HealthCheck())
}

func deletesResponse() ([]byte, error) {
	return jsonResponse(map[string]any{"items": guard().ListDeletes(50)})
}


func metricsResetTodayResponse(req managementRequest) ([]byte, error) {
	if req.Method != http.MethodPost {
		return okEnvelope(managementResponse{
			StatusCode: http.StatusMethodNotAllowed,
			Headers:    http.Header{"content-type": []string{"application/json"}},
			Body:       []byte(`{"error":"POST required"}`),
		})
	}
	var body struct {
		Confirm bool   `json:"confirm"`
		Note    string `json:"note"`
	}
	_ = json.Unmarshal(req.Body, &body)
	if !body.Confirm {
		return jsonResponse(map[string]any{
			"ok":      false,
			"error":   "confirm=true required",
			"hint":    "清零日历今日 used_today/requests_today；不改 used_total 与 rolling 快照",
			"version": pluginVer,
		})
	}
	g := guard()
	before := g.MetricsWithInventory(0, 0, 0)
	note := strings.TrimSpace(body.Note)
	if note == "" {
		note = "ui"
	}
	if err := g.ResetCalendarToday(note); err != nil {
		return jsonResponse(map[string]any{"ok": false, "error": err.Error()})
	}
	after := g.MetricsWithInventory(0, 0, 0)
	return jsonResponse(map[string]any{
		"ok":      true,
		"note":    note,
		"before":  map[string]any{"used_today": before.UsedToday, "requests_today": before.RequestsToday, "day_key": before.DayKey},
		"after":   map[string]any{"used_today": after.UsedToday, "requests_today": after.RequestsToday, "day_key": after.DayKey},
		"kept":    map[string]any{"used_total": after.UsedTotal, "rolling_used": after.RollingUsedKnown},
	})
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
		"management_url":         resolveManagementBaseURL(cfg.ManagementURL),
		"management_url_configured": strings.TrimSpace(cfg.ManagementURL) != "",
		"management_source":      managementURLSource(cfg.ManagementURL),
		"management_key_set":     resolveManagementKey(cfg.ManagementKey) != "",
		"management_key_source":  managementKeySource(cfg.ManagementKey),
		"pure_cpa_mode":         strings.TrimSpace(cfg.CPAMPURL) == "" || strings.TrimSpace(cfg.CPAMPAdminKey) == "",
		"state_path":             cfg.StatePath,
		"patrol_enabled":         cfg.PatrolEnabled,
		"patrol_interval":        cfg.PatrolInterval,
		"patrol_timeout":         cfg.PatrolTimeout,
		"patrol_auth_dir":        cfg.PatrolAuthDir,
		"patrol_concurrency":     cfg.PatrolConcurrency,
		"patrol_batch_size":      cfg.PatrolBatchSize,
		"patrol_model":          cfg.PatrolModel,
		"patrol_auto_model_switch": cfg.PatrolAutoModelSwitch,
		"patrol_initial_delay_sec":  cfg.PatrolInitialDelaySec,
		"patrol_proxy_url":      cfg.PatrolProxyURL,
		"patrol_proxy_set":      cfg.PatrolProxyURL != "",
	})
}


func bindKeyResponse(req managementRequest) ([]byte, error) {
	if req.Method != http.MethodPost {
		return okEnvelope(managementResponse{
			StatusCode: http.StatusMethodNotAllowed,
			Headers:    http.Header{"content-type": []string{"application/json; charset=utf-8"}},
			Body:       []byte(`{"error":"POST required"}`),
		})
	}
	var body struct {
		Key            string `json:"key"`
		ManagementKey  string `json:"management_key"`
		Persist        *bool  `json:"persist"`
	}
	_ = json.Unmarshal(req.Body, &body)
	key := strings.TrimSpace(firstNonEmpty(body.Key, body.ManagementKey))
	if key == "" {
		// also accept header-only bind
		adoptManagementKeyFromHeaders(req.Headers)
		key = getRuntimeManagementKey()
	}
	if key == "" {
		return jsonResponse(map[string]any{"ok": false, "error": "empty management key"})
	}
	setRuntimeManagementKey(key)

	cfg := guard().Config()
	cfg.ManagementKey = key
	if strings.TrimSpace(cfg.ManagementURL) == "" {
		cfg.ManagementURL = resolveManagementBaseURL("")
	}
	persist := true
	if body.Persist != nil {
		persist = *body.Persist
	}
	persisted := false
	if persist {
		// Write into CPA plugin yaml so restarts keep pure-CPA working.
		if err := writePluginConfig(cfg, map[string]any{
			"management_key": key,
			"management_url": resolveManagementBaseURL(cfg.ManagementURL),
		}); err != nil {
			// Still keep runtime key even if persist fails (e.g. self-call race).
			guard().ApplyConfig(cfg)
			return jsonResponse(map[string]any{
				"ok":          true,
				"bound":       true,
				"persisted":   false,
				"error":       err.Error(),
				"key_set":     true,
				"key_source":  managementKeySource(cfg.ManagementKey),
				"management_url": resolveManagementBaseURL(cfg.ManagementURL),
			})
		}
		persisted = true
	}
	guard().ApplyConfig(cfg)
	return jsonResponse(map[string]any{
		"ok":             true,
		"bound":          true,
		"persisted":      persisted,
		"key_set":        true,
		"key_source":     managementKeySource(cfg.ManagementKey),
		"management_url": resolveManagementBaseURL(cfg.ManagementURL),
		"note":           "browser key == process key (pure CPA)",
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

func patrolStatusResponse(req managementRequest) ([]byte, error) {
	g := guard()
	opts := parsePatrolStatusOpts(req)
	status := g.PatrolStatusWith(opts)
	out := map[string]any{
		"ok":     true,
		"patrol": status,
	}
	// delete_history is heavy and unused by patrolPoll; only include with full log requests.
	if !opts.Lite && opts.LogLimit < 0 {
		out["delete_history"] = g.ListDeletes(20)
	}
	return jsonResponse(out)
}

func parsePatrolStatusOpts(req managementRequest) xaiquota.PatrolStatusOpts {
	opts := xaiquota.PatrolStatusOpts{}
	// default for polling: lite-ish truncation via LogLimit=50 in PatrolStatusWith
	q := req.Query
	raws := []string{req.Path, req.PathAlt}
	get := func(key string) string {
		if q != nil {
			if v := strings.TrimSpace(q.Get(key)); v != "" {
				return v
			}
		}
		for _, raw := range raws {
			if raw == "" {
				continue
			}
			needle := key + "="
			if i := strings.Index(raw, needle); i >= 0 {
				v := raw[i+len(needle):]
				if j := strings.IndexAny(v, "&/#?"); j >= 0 {
					v = v[:j]
				}
				return strings.TrimSpace(v)
			}
		}
		return ""
	}
	lite := strings.ToLower(get("lite"))
	if lite == "1" || lite == "true" || lite == "yes" {
		opts.Lite = true
	}
	logMode := strings.ToLower(get("log"))
	switch logMode {
	case "full", "all", "500":
		opts.LogLimit = -1
	case "0", "none", "off":
		opts.Lite = true
	case "":
		// keep default 50
	default:
		if n, err := strconv.Atoi(logMode); err == nil && n > 0 {
			opts.LogLimit = n
		}
	}
	return opts
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
		PatrolEnabled          *bool    `json:"patrol_enabled"`
		PatrolInterval         *float64 `json:"patrol_interval"`
		PatrolTimeout          *float64 `json:"patrol_timeout"`
		PatrolAuthDir          *string  `json:"patrol_auth_dir"`
		PatrolProxyURL         *string  `json:"patrol_proxy_url"`
		PatrolConcurrency      *float64 `json:"patrol_concurrency"`
		PatrolBatchSize        *float64 `json:"patrol_batch_size"`
		PatrolModel            *string  `json:"patrol_model"`
		PatrolAutoModelSwitch  *bool    `json:"patrol_auto_model_switch"`
		PatrolInitialDelaySec  *float64 `json:"patrol_initial_delay_sec"`
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
		d := strings.TrimSpace(*body.PatrolAuthDir)
		if d != "" {
			cfg.PatrolAuthDir = d
		}
		// empty string from UI must NOT wipe existing configured auth dir
	}
	if strings.TrimSpace(cfg.PatrolAuthDir) == "" {
		cfg.PatrolAuthDir = xaiquota.ResolvePatrolAuthDir("")
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
	if body.PatrolAutoModelSwitch != nil {
		cfg.PatrolAutoModelSwitch = *body.PatrolAutoModelSwitch
	}
	if body.PatrolInitialDelaySec != nil && *body.PatrolInitialDelaySec >= 0 {
		cfg.PatrolInitialDelaySec = *body.PatrolInitialDelaySec
	}
	// Persist full patrol settings into CPA plugin config (GET+merge+PUT).
	// Always write patrol_proxy_url (including empty) so clear/save is real.
	patch := map[string]any{
		"patrol_enabled":           cfg.PatrolEnabled,
		"patrol_interval":          cfg.PatrolInterval,
		"patrol_timeout":           cfg.PatrolTimeout,
		"patrol_auth_dir":          cfg.PatrolAuthDir,
		"patrol_concurrency":       cfg.PatrolConcurrency,
		"patrol_batch_size":        cfg.PatrolBatchSize,
		"patrol_model":             cfg.PatrolModel,
		"patrol_auto_model_switch": cfg.PatrolAutoModelSwitch,
		"patrol_initial_delay_sec":  cfg.PatrolInitialDelaySec,
		"patrol_proxy_url":         cfg.PatrolProxyURL,
	}
	if err := writePluginConfig(cfg, patch); err != nil {
		return jsonResponse(map[string]any{"ok": false, "error": err.Error()})
	}
	guard().ApplyConfig(cfg)
	return jsonResponse(map[string]any{
		"ok":                       true,
		"persisted":                true,
		"patrol_enabled":           cfg.PatrolEnabled,
		"patrol_interval":          cfg.PatrolInterval,
		"patrol_timeout":           cfg.PatrolTimeout,
		"patrol_auth_dir":          cfg.PatrolAuthDir,
		"patrol_concurrency":       cfg.PatrolConcurrency,
		"patrol_batch_size":        cfg.PatrolBatchSize,
		"patrol_model":             cfg.PatrolModel,
		"patrol_auto_model_switch": cfg.PatrolAutoModelSwitch,
		"patrol_initial_delay_sec":  cfg.PatrolInitialDelaySec,
		"patrol_proxy_url":         cfg.PatrolProxyURL,
		"patrol_proxy_set":         cfg.PatrolProxyURL != "",
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
