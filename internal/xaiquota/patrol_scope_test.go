package xaiquota

import "testing"

func TestSelectPatrolCandidatesScheduledIncludesEnabledAndPluginCooldownOnly(t *testing.T) {
	files := []AuthFile{
		{AuthIndex: "enabled", Provider: "xai", Disabled: false},
		{AuthIndex: "cooldown", Provider: "xai", Disabled: true},
		{AuthIndex: "manual", Provider: "xai", Disabled: true},
		{AuthIndex: "other", Provider: "codex", Disabled: false},
	}
	records := map[string]AccountRecord{
		"cooldown": {
			AuthIndex: "cooldown", State: StateAutoDisabled, DisableSource: SourcePluginAuto,
			Owner: Owner, PreDisabled: false, Signal: "usage_ratio_threshold",
		},
		"manual": {
			AuthIndex: "manual", State: StateUserManualDisabled, DisableSource: SourceUserManual,
			PreDisabled: true,
		},
	}

	got := selectPatrolCandidates(files, records, "scheduled")
	if len(got) != 2 || got[0].AuthIndex != "cooldown" || got[1].AuthIndex != "enabled" {
		t.Fatalf("scheduled candidates=%+v", got)
	}
	all := selectPatrolCandidates(files, records, "all")
	if len(all) != 1 || all[0].AuthIndex != "enabled" {
		t.Fatalf("all 必须只含启用号: %+v", all)
	}
	cool := selectPatrolCandidates(files, records, "spending_only")
	if len(cool) != 1 || cool[0].AuthIndex != "cooldown" {
		t.Fatalf("spending_only 必须只含插件冷却号: %+v", cool)
	}
}
