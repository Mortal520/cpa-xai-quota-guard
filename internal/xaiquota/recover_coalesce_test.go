package xaiquota

import (
	"testing"
	"time"
)

func TestCoalesceRecoverAtMSSoftDoesNotExtend(t *testing.T) {
	now := time.Now()
	disabled := now.Add(-10 * time.Hour)
	existing := &AccountRecord{
		State:         StateAutoDisabled,
		DisableSource: SourcePluginAuto,
		Owner:         Owner,
		DisabledAtMS:  disabled.UnixMilli(),
		RecoverAtMS:   disabled.Add(24 * time.Hour).UnixMilli(),
	}
	match := MatchResult{
		RecoverAt: now.Add(24 * time.Hour),
		Signal:    "body.error.code=subscription:free-usage-exhausted",
		Soft:      true,
	}
	got := CoalesceRecoverAtMS(existing, match)
	if got != existing.RecoverAtMS {
		t.Fatalf("soft re-hit must keep original recover: got %d want %d", got, existing.RecoverAtMS)
	}
}

func TestCoalesceRecoverAtMSClampsPastSoftCap(t *testing.T) {
	now := time.Now()
	disabled := now.Add(-10 * time.Hour)
	existing := &AccountRecord{
		DisabledAtMS: disabled.UnixMilli(),
		RecoverAtMS:  now.Add(24 * time.Hour).UnixMilli(),
	}
	match := MatchResult{RecoverAt: now.Add(24 * time.Hour), Soft: true}
	got := CoalesceRecoverAtMS(existing, match)
	capMS := disabled.UnixMilli() + SoftCooldownMS
	if got != capMS {
		t.Fatalf("expected clamp to disabled+24h %d got %d", capMS, got)
	}
}

func TestEffectiveRecoverAtMSClampsWrongExtend(t *testing.T) {
	now := time.Now()
	disabled := now.Add(-30 * time.Hour)
	rec := &AccountRecord{
		DisabledAtMS: disabled.UnixMilli(),
		RecoverAtMS:  now.Add(20 * time.Hour).UnixMilli(),
	}
	eff := EffectiveRecoverAtMS(rec)
	want := disabled.UnixMilli() + SoftCooldownMS
	if eff != want {
		t.Fatalf("eff=%d want=%d", eff, want)
	}
	if now.UnixMilli() < eff {
		t.Fatalf("should be past due: now=%d eff=%d", now.UnixMilli(), eff)
	}
}

func TestDueAutoDisabledUsesEffectiveRecover(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir + "/state.json")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	disabled := now.Add(-30 * time.Hour)
	_ = s.Upsert(AccountRecord{
		AuthIndex:     "a1",
		State:         StateAutoDisabled,
		DisableSource: SourcePluginAuto,
		Owner:         Owner,
		DisabledAtMS:  disabled.UnixMilli(),
		RecoverAtMS:   now.Add(20 * time.Hour).UnixMilli(),
	})
	due := s.DueAutoDisabled(now)
	if len(due) != 1 {
		t.Fatalf("due=%d want 1", len(due))
	}
}