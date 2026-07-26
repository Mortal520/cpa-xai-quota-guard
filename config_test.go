package main

import "testing"

func TestParseConfigAllowsOverLimitCooldownRatio(t *testing.T) {
	cfg := parseConfigFromReconfigure([]byte(`{
		"config": {
			"quota_usage_cooldown_ratio": 1.2,
			"patrol_recheck_cooldowns": false
		}
	}`))
	if cfg.QuotaUsageCooldownRatio != 1.2 {
		t.Fatalf("ratio=%v，期望保留 1.2", cfg.QuotaUsageCooldownRatio)
	}
	if cfg.PatrolRecheckCooldowns {
		t.Fatal("显式关闭定时冷却复查后不应自动开启")
	}
}

func TestParseConfigDefaultsToRecheckCooldowns(t *testing.T) {
	cfg := parseConfigFromReconfigure([]byte(`{"config":{}}`))
	if !cfg.PatrolRecheckCooldowns {
		t.Fatal("旧配置缺少字段时应默认定时复查冷却号")
	}
	if cfg.QuotaUsageCooldownRatio != 0 {
		t.Fatalf("默认阈值=%v，期望 0（关闭）", cfg.QuotaUsageCooldownRatio)
	}
}
