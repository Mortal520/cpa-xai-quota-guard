package xaiquota

import "testing"

func TestClassifyAuthTier(t *testing.T) {
	cases := []struct {
		name string
		file AuthFile
		raw  string
		want string
	}{
		{"default_free", AuthFile{Account: "a@b.com"}, `{}`, TierFree},
		{"super_json", AuthFile{}, `{"subscription":{"plan":"SuperGrok"}}`, TierSuper},
		{"heavy_json", AuthFile{}, `{"account_tier":"heavy"}`, TierHeavy},
		{"note_super", AuthFile{Note: "supergrok"}, ``, TierSuper},
		{"prefix_heavy", AuthFile{Prefix: "heavy-pool"}, ``, TierHeavy},
		{"label_super", AuthFile{Label: "Super Grok Account"}, ``, TierSuper},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var raw []byte
			if tc.raw != "" {
				raw = []byte(tc.raw)
			}
			got := ClassifyAuthTier(tc.file, raw)
			if got.Tier != tc.want {
				t.Fatalf("tier=%q want %q detail=%q keys=%v", got.Tier, tc.want, got.Detail, got.SourceKeys)
			}
		})
	}
}

func TestIsProtectedTier(t *testing.T) {
	if !IsProtectedTier(TierSuper, nil) || !IsProtectedTier(TierHeavy, nil) {
		t.Fatal("default protect super/heavy")
	}
	if IsProtectedTier(TierFree, nil) {
		t.Fatal("free not protected by default")
	}
	if !IsProtectedTier(TierFree, []string{"free"}) {
		t.Fatal("explicit protect free")
	}
}
