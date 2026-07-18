package main

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/mortal/cpa-xai-quota-guard/internal/xaiquota"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

func configDefaults() xaiquota.Config {
	// management_url/key may be empty in pure CPA yaml; resolved at call time (mgmt_resolve.go).
	return xaiquota.Defaults()
}

func configFields() []pluginapi.ConfigField {
	return []pluginapi.ConfigField{
		{Name: "enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "CPA 加载插件开关（勿用本插件 UI 关闭，否则会卸载路由）"},
		{Name: "quota_guard_enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "额度管控功能开关（UI 切换写入此字段；默认跟随 enabled）"},
		{Name: "tick_seconds", Type: pluginapi.ConfigFieldTypeNumber, Description: "到期恢复扫描周期(秒)"},
		{Name: "max_reset_seconds", Type: pluginapi.ConfigFieldTypeNumber, Description: "允许的最大重置等待(秒)，超过则不禁用"},
		{Name: "management_url", Type: pluginapi.ConfigFieldTypeString, Description: "CPA 管理 API 基址（可空：默认 http://127.0.0.1:8317 或 CPA_MANAGEMENT_BASE_URL；纯 CPA 同机即可）"},
		{Name: "management_key", Type: pluginapi.ConfigFieldTypeString, Description: "CPA X-Management-Key（敏感；可空时读 CPA_MANAGEMENT_KEY/MANAGEMENT_PASSWORD）"},
		{Name: "state_path", Type: pluginapi.ConfigFieldTypeString, Description: "状态持久化 JSON 路径"},
		{Name: "min_reset_seconds", Type: pluginapi.ConfigFieldTypeNumber, Description: "最小冷却等待(秒)，0=不限制"},
		{Name: "include_unobserved_quota_est", Type: pluginapi.ConfigFieldTypeBoolean, Description: "总额度是否计入未观测账号×默认2M（默认开；关则仅已知 rolling limit）"},
		{Name: "cpamp_url", Type: pluginapi.ConfigFieldTypeString, Description: "CPAMP 基址(可选，用于回补/深链)"},
		{Name: "cpamp_admin_key", Type: pluginapi.ConfigFieldTypeString, Description: "CPAMP Panel Admin Key(敏感)"},
		{Name: "webhook_url", Type: pluginapi.ConfigFieldTypeString, Description: "冷却/删除事件 Webhook URL(可选)"},
		{Name: "patrol_enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "启用定时巡查(默认关闭)"},
		{Name: "patrol_interval", Type: pluginapi.ConfigFieldTypeNumber, Description: "巡查周期(秒,默认3600=60分钟；UI 以分钟编辑)"},
		{Name: "patrol_timeout", Type: pluginapi.ConfigFieldTypeNumber, Description: "单个凭证探测超时(秒,默认15)"},
		{Name: "patrol_batch_size", Type: pluginapi.ConfigFieldTypeNumber, Description: "每轮巡查上限(0=不限)"},
		{Name: "patrol_auth_dir", Type: pluginapi.ConfigFieldTypeString, Description: "auth 目录(可空：自动探测 ~/.cli-proxy-api 等；空保存不会清空已有值)"},
		{Name: "patrol_proxy_url", Type: pluginapi.ConfigFieldTypeString, Description: "巡查探测使用的代理(可选,如 socks5://host:port)"},
		{Name: "patrol_concurrency", Type: pluginapi.ConfigFieldTypeNumber, Description: "巡查并发硬上限(默认24；实际线程在上限内按负载/探测健康弹性伸缩)"},
		{Name: "patrol_model", Type: pluginapi.ConfigFieldTypeString, Description: "巡查主探测模型(默认 grok-4.5)"},
		{Name: "patrol_auto_model_switch", Type: pluginapi.ConfigFieldTypeBoolean, Description: "402 时自动拉取凭证 /models 并切换备用模型再测(默认关；关则仅用 patrol_model，仍 402 则冷却禁用)"},
		{Name: "patrol_initial_delay_sec", Type: pluginapi.ConfigFieldTypeNumber, Description: "定时巡查启动后首轮延迟(秒,默认60；0=随首次 tick 立即可能触发)"},
	}
}

func parseConfigFromReconfigure(request []byte) xaiquota.Config {
	cfg := configDefaults()
	if len(request) == 0 {
		return cfg
	}
	var raw map[string]any
	if err := json.Unmarshal(request, &raw); err != nil {
		return cfg
	}
	if yamlBytes, ok := extractYAMLBytes(raw); ok {
		applyYAMLConfig(&cfg, yamlBytes)
		normalizeRuntimeConfig(&cfg)
		return cfg
	}
	configMap := raw
	if nested, ok := raw["config"].(map[string]any); ok {
		configMap = nested
	}
	applyConfigMap(&cfg, configMap)
	return cfg
}

func extractYAMLBytes(raw map[string]any) ([]byte, bool) {
	v, ok := raw["config_yaml"]
	if !ok || v == nil {
		return nil, false
	}
	switch t := v.(type) {
	case string:
		if decoded, err := base64.StdEncoding.DecodeString(t); err == nil {
			return decoded, true
		}
		return []byte(t), true
	case []byte:
		return t, true
	default:
		return nil, false
	}
}

func applyYAMLConfig(cfg *xaiquota.Config, yamlBytes []byte) {
	var m map[string]any
	if err := yaml.Unmarshal(yamlBytes, &m); err != nil {
		return
	}
	applyConfigMap(cfg, m)
}

func applyConfigMap(cfg *xaiquota.Config, m map[string]any) {
	if m == nil {
		return
	}
	// CPA host "enabled" controls plugin load; functional switch is quota_guard_enabled.
	// If quota_guard_enabled is absent, fall back to enabled for backward compatibility.
	if v, ok := asBool(m["enabled"]); ok {
		cfg.Enabled = v
	}
	if v, ok := asBool(m["quota_guard_enabled"]); ok {
		cfg.Enabled = v
	}
	if v, ok := asFloat(m["tick_seconds"]); ok && v > 0 {
		cfg.TickSeconds = v
	}
	if v, ok := asFloat(m["max_reset_seconds"]); ok && v > 0 {
		cfg.MaxResetSeconds = v
	}
	if v, ok := asString(m["management_url"]); ok {
		cfg.ManagementURL = strings.TrimSpace(v)
	}
	if v, ok := asString(m["management_key"]); ok {
		cfg.ManagementKey = strings.TrimSpace(v)
	}
	if v, ok := asString(m["state_path"]); ok && strings.TrimSpace(v) != "" {
		cfg.StatePath = strings.TrimSpace(v)
	}
	if v, ok := asFloat(m["min_reset_seconds"]); ok && v >= 0 {
		cfg.MinResetSeconds = v
	}
	if v, ok := asBool(m["include_unobserved_quota_est"]); ok {
		cfg.IncludeUnobservedQuotaEst = v
	}
	if v, ok := asString(m["cpamp_url"]); ok {
		cfg.CPAMPURL = strings.TrimSpace(v)
	}
	if v, ok := asString(m["cpamp_admin_key"]); ok {
		cfg.CPAMPAdminKey = strings.TrimSpace(v)
	}
	if v, ok := asString(m["webhook_url"]); ok {
		cfg.WebhookURL = strings.TrimSpace(v)
	}
	if v, ok := asBool(m["patrol_enabled"]); ok {
		cfg.PatrolEnabled = v
	}
	if v, ok := asFloat(m["patrol_interval"]); ok && v > 0 {
		cfg.PatrolInterval = v
	}
	if v, ok := asFloat(m["patrol_timeout"]); ok && v > 0 {
		cfg.PatrolTimeout = v
	}
	if v, ok := asFloat(m["patrol_batch_size"]); ok && v >= 0 {
		cfg.PatrolBatchSize = int(v)
	}
	if v, ok := asString(m["patrol_auth_dir"]); ok {
		cfg.PatrolAuthDir = strings.TrimSpace(v)
	}
	if v, ok := asString(m["patrol_proxy_url"]); ok {
		cfg.PatrolProxyURL = strings.TrimSpace(v)
	}
	if v, ok := asFloat(m["patrol_concurrency"]); ok && v > 0 {
		cfg.PatrolConcurrency = int(v)
	}
	if v, ok := asString(m["patrol_model"]); ok {
		v = strings.TrimSpace(v)
		if v != "" {
			cfg.PatrolModel = v
		}
	}
	if v, ok := asBool(m["patrol_auto_model_switch"]); ok {
		cfg.PatrolAutoModelSwitch = v
	}
	if v, ok := asFloat(m["patrol_initial_delay_sec"]); ok && v >= 0 {
		cfg.PatrolInitialDelaySec = v
	}

}

func asBool(v any) (bool, bool) {
	switch t := v.(type) {
	case bool:
		return t, true
	case string:
		s := strings.ToLower(strings.TrimSpace(t))
		if s == "true" || s == "1" || s == "yes" {
			return true, true
		}
		if s == "false" || s == "0" || s == "no" {
			return false, true
		}
	case float64:
		return t != 0, true
	}
	return false, false
}

func asFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case json.Number:
		n, err := t.Float64()
		return n, err == nil
	case string:
		n, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return n, err == nil
	}
	return 0, false
}

func asString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	default:
		return "", false
	}
}