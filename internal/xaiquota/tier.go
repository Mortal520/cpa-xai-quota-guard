package xaiquota

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode"
)

// Account tier labels (aligned with grok-panel Free/Super/Heavy semantics).
const (
	TierFree    = "free"
	TierSuper   = "super"
	TierHeavy   = "heavy"
	TierUnknown = "unknown"
)

// TierClassification is the result of classifying one xAI auth file / JSON.
type TierClassification struct {
	Tier       string   `json:"tier"`
	Source     string   `json:"source,omitempty"`
	Detail     string   `json:"detail,omitempty"`
	SourceKeys []string `json:"source_keys,omitempty"`
}

type tierSignal struct {
	Path string
	Tier string
	Raw  string
}

// ClassifyAuthTier classifies Free / Super / Heavy from list metadata + optional auth JSON.
// Heuristics adapted from TizenryA/cpa-plugin-grok-panel (MIT) for local panel use.
func ClassifyAuthTier(file AuthFile, rawJSON []byte) TierClassification {
	signals := make([]tierSignal, 0, 8)
	listSignals := map[string]string{
		"list.account":  file.Account,
		"list.name":     file.Name,
		"list.provider": file.Provider,
		"list.note":     file.Note,
		"list.label":    file.Label,
		"list.prefix":   file.Prefix,
		"list.tag":      file.Tag,
		"list.type":     file.AccountType,
	}
	for path, value := range listSignals {
		addTierSignal(&signals, path, value)
	}
	if len(rawJSON) > 0 && string(bytesTrimSpace(rawJSON)) != "null" {
		var value any
		if err := json.Unmarshal(rawJSON, &value); err == nil {
			collectTierSignals(&signals, "", value, 0)
		}
	}
	tier := resolveTier(signals)
	if tier == TierUnknown {
		// Default mass pool accounts to free (same product assumption as grok-panel).
		tier = TierFree
	}
	keys := make([]string, 0, len(signals))
	var detail string
	src := "metadata"
	for _, s := range signals {
		if s.Tier == "" {
			continue
		}
		keys = append(keys, s.Path)
		if s.Tier == tier && detail == "" {
			detail = s.Raw
			src = s.Path
		}
	}
	return TierClassification{Tier: tier, Source: src, Detail: truncate(detail, 80), SourceKeys: keys}
}

func bytesTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}

func resolveTier(signals []tierSignal) string {
	best := TierUnknown
	for _, signal := range signals {
		switch signal.Tier {
		case TierHeavy:
			return TierHeavy
		case TierSuper:
			best = TierSuper
		case TierFree:
			if best == TierUnknown {
				best = TierFree
			}
		}
	}
	return best
}

func addTierSignal(signals *[]tierSignal, path, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	tier := tierFromText(value)
	if tier == "" {
		return
	}
	*signals = append(*signals, tierSignal{Path: path, Tier: tier, Raw: value})
}

func collectTierSignals(signals *[]tierSignal, path string, value any, depth int) {
	if depth > 6 || value == nil {
		return
	}
	switch v := value.(type) {
	case map[string]any:
		for k, val := range v {
			p := k
			if path != "" {
				p = path + "." + k
			}
			if isKnownTierKey(k) {
				switch tv := val.(type) {
				case string:
					addTierSignal(signals, p, tv)
				case bool:
					if tv && isBooleanTierKey(k) {
						addTierSignal(signals, p, k)
					}
				case float64:
					if tv != 0 && isBooleanTierKey(k) {
						addTierSignal(signals, p, k)
					}
				default:
					collectTierSignals(signals, p, val, depth+1)
				}
				continue
			}
			collectTierSignals(signals, p, val, depth+1)
		}
	case []any:
		for i, item := range v {
			collectTierSignals(signals, path+"["+strconv.Itoa(i)+"]", item, depth+1)
		}
	case string:
		addTierSignal(signals, path, v)
	}
}


func tierFromText(text string) string {
	norm := normalizeLoose(text)
	if norm == "" {
		return ""
	}
	if strings.Contains(norm, "heavy") || strings.Contains(norm, "grokheavy") ||
		strings.Contains(norm, "supergrokheavy") || strings.Contains(norm, "supergrokpro") ||
		strings.Contains(norm, "subscriptiontiersupergrokheavy") || strings.Contains(norm, "subscriptiontiersupergrokpro") {
		return TierHeavy
	}
	if strings.Contains(norm, "supergrok") || strings.Contains(norm, "subscriptiontiersupergrok") ||
		strings.Contains(norm, "grokpro") || strings.Contains(norm, "premiumplus") ||
		strings.Contains(norm, "premium") || strings.Contains(norm, "super") ||
		(strings.Contains(norm, "pro") && !strings.Contains(norm, "proxy") && !strings.Contains(norm, "provider")) ||
		strings.Contains(norm, "paid") || strings.Contains(norm, "plus") {
		return TierSuper
	}
	if strings.Contains(norm, "free") || strings.Contains(norm, "basic") || strings.Contains(norm, "trial") {
		return TierFree
	}
	return ""
}

func normalizeLoose(value string) string {
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isKnownTierKey(key string) bool {
	n := normalizeLoose(key)
	switch n {
	case "tier", "plantier", "accounttier", "subscription", "subscriptiontier", "plan", "plantype",
		"note", "label", "prefix", "tag", "tags", "accounttype", "type", "product", "package", "sku":
		return true
	default:
		return strings.Contains(n, "tier") || strings.Contains(n, "plan") || strings.Contains(n, "sub")
	}
}

func isBooleanTierKey(key string) bool {
	n := normalizeLoose(key)
	return strings.Contains(n, "super") || strings.Contains(n, "heavy") || strings.Contains(n, "premium") || strings.Contains(n, "paid")
}

// IsProtectedTier reports whether tier is in the protected list (default: super/heavy/unknown).
func IsProtectedTier(tier string, protected []string) bool {
	tier = strings.ToLower(strings.TrimSpace(tier))
	if tier == "" {
		tier = TierUnknown
	}
	if len(protected) == 0 {
		protected = []string{TierSuper, TierHeavy, TierUnknown}
	}
	for _, p := range protected {
		if strings.EqualFold(strings.TrimSpace(p), tier) {
			return true
		}
	}
	return false
}
