package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mortal/cpa-xai-quota-guard/internal/xaiquota"
)

// hostLogger adapts host.log to xaiquota.Logger.
type hostLogger struct{}

func (hostLogger) Log(level, message string) {
	hostLog(level, "[cpa-xai-quota-guard] "+message)
}

// mgmtAuth implements xaiquota.AuthFileLookup via CPA management API.
type mgmtAuth struct {
	url string
	key string
}

func newMgmtAuth(cfg xaiquota.Config) *mgmtAuth {
	// Pure CPA: empty yaml management_url/key still resolve via env + 127.0.0.1:8317 (see mgmt_resolve.go).
	return &mgmtAuth{
		url: resolveManagementBaseURL(cfg.ManagementURL),
		key: resolveManagementKey(cfg.ManagementKey),
	}
}

type mgmtAuthEntry struct {
	AuthIndex   string `json:"auth_index"`
	Name        string `json:"name"`
	Provider    string `json:"provider"`
	Account     string `json:"account"`
	Email       string `json:"email"`
	Disabled    bool   `json:"disabled"`
	Success     int64  `json:"success"`
	Failed      int64  `json:"failed"`
	Note        string `json:"note"`
	Label       string `json:"label"`
	Prefix      string `json:"prefix"`
	Tag         string `json:"tag"`
	AccountType string `json:"account_type"`
	Type        string `json:"type"`
}

// Short-lived cache so state/health/tick do not re-pull 5k+ auth-files every request.
// On fetch failure, return last good inventory (sticky) so UI never flashes xai_total=0.
var (
	authListCacheMu    sync.Mutex
	authListCacheAt    time.Time
	authListCacheKey   string
	authListCacheData  []xaiquota.AuthFile
	authListLastErr    string
	authListLastStale  bool
	authListLastXAI    int
	authListLastEn     int
	authListLastDis    int
)

const authListCacheTTL = 12 * time.Second
const authListStaleMax = 10 * time.Minute

// Auth-fail circuit breaker: wrong CPAMP key must not hammer CPA management and trip IP ban.
var (
	mgmtAuthCBMu       sync.Mutex
	mgmtAuthCBUntil    time.Time
	mgmtAuthCBLast     string
	mgmtAuthCBHits     int
	mgmtAuthCBOpenHits int
)

const mgmtAuthCBCooldown = 5 * time.Minute
const mgmtAuthCBTripAfter = 3

func mgmtAuthCircuitOpen() (open bool, remain time.Duration, last string) {
	mgmtAuthCBMu.Lock()
	defer mgmtAuthCBMu.Unlock()
	if mgmtAuthCBUntil.IsZero() {
		return false, 0, ""
	}
	left := time.Until(mgmtAuthCBUntil)
	if left <= 0 {
		mgmtAuthCBUntil = time.Time{}
		mgmtAuthCBHits = 0
		return false, 0, ""
	}
	return true, left, mgmtAuthCBLast
}

func noteMgmtAuthFailure(msg string) {
	mgmtAuthCBMu.Lock()
	defer mgmtAuthCBMu.Unlock()
	mgmtAuthCBHits++
	mgmtAuthCBLast = truncate(msg, 160)
	if mgmtAuthCBHits >= mgmtAuthCBTripAfter {
		mgmtAuthCBUntil = time.Now().Add(mgmtAuthCBCooldown)
		mgmtAuthCBOpenHits++
		hostLog("warn", fmt.Sprintf("[cpa-xai-quota-guard] CPA management 鉴权失败熔断 %s (hits=%d) — 停止对 8317 重试以免封 IP。请确认 management_key 是 CPA 密钥而非 CPAMP 密钥。", mgmtAuthCBCooldown, mgmtAuthCBHits))
	}
}

func clearMgmtAuthCircuit() {
	mgmtAuthCBMu.Lock()
	defer mgmtAuthCBMu.Unlock()
	mgmtAuthCBUntil = time.Time{}
	mgmtAuthCBHits = 0
	mgmtAuthCBLast = ""
}

func isMgmtAuthHTTPError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "status 401") || strings.Contains(s, "status 403") ||
		strings.Contains(s, "unauthorized") || strings.Contains(s, "invalid management") ||
		strings.Contains(s, "forbidden")
}

// probeManagementKey validates key against CPA before bind/persist.
func probeManagementKey(base, key string) error {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	key = strings.TrimSpace(key)
	if base == "" || key == "" {
		return fmt.Errorf("empty management base or key")
	}
	// Explicit bind always probes (bypass open circuit) so a corrected CPA key can recover.
	_, err := mgmtHTTP(http.MethodGet, base+"/v0/management/auth-files", nil, key)
	if err != nil {
		if isMgmtAuthHTTPError(err) {
			noteMgmtAuthFailure(err.Error())
			return fmt.Errorf("CPA 拒绝该 Key（401/403）。请填 CPA management 密钥，不要用 CPAMP(:18317) 管理密码。详情: %w", err)
		}
		return err
	}
	clearMgmtAuthCircuit()
	return nil
}


type authListMeta struct {
	OK        bool   `json:"ok"`
	Stale     bool   `json:"stale"`
	Error     string `json:"error,omitempty"`
	CachedAt  int64  `json:"cached_at_ms,omitempty"`
	AgeMS     int64  `json:"age_ms,omitempty"`
	XAITotal  int    `json:"xai_total"`
	XAIEnabled int   `json:"xai_enabled"`
	XAIDisabled int  `json:"xai_disabled"`
}

func authListInventoryMeta() authListMeta {
	authListCacheMu.Lock()
	defer authListCacheMu.Unlock()
	meta := authListMeta{
		OK:          authListLastErr == "" && len(authListCacheData) > 0,
		Stale:       authListLastStale,
		Error:       authListLastErr,
		XAITotal:    authListLastXAI,
		XAIEnabled:  authListLastEn,
		XAIDisabled: authListLastDis,
	}
	if !authListCacheAt.IsZero() {
		meta.CachedAt = authListCacheAt.UnixMilli()
		meta.AgeMS = time.Since(authListCacheAt).Milliseconds()
	}
	return meta
}

func recountXAI(files []xaiquota.AuthFile) (total, en, dis int) {
	for _, f := range files {
		if !xaiquota.IsXAIProvider(f.Provider, "") {
			continue
		}
		total++
		if f.Disabled {
			dis++
		} else {
			en++
		}
	}
	return total, en, dis
}

func copyAuthFiles(in []xaiquota.AuthFile) []xaiquota.AuthFile {
	if len(in) == 0 {
		return nil
	}
	out := make([]xaiquota.AuthFile, len(in))
	copy(out, in)
	return out
}

func (m *mgmtAuth) List() ([]xaiquota.AuthFile, error) {
	if m == nil || m.url == "" || m.key == "" {
		return nil, fmt.Errorf("management not configured")
	}
	if open, left, last := mgmtAuthCircuitOpen(); open {
		// Prefer sticky cache over hammering CPA with bad key.
		authListCacheMu.Lock()
		stale := copyAuthFiles(authListCacheData)
		authListLastErr = fmt.Sprintf("auth circuit open %s: %s", left.Round(time.Second), last)
		authListLastStale = true
		authListCacheMu.Unlock()
		if len(stale) > 0 {
			return stale, nil
		}
		return nil, fmt.Errorf("management auth circuit open for %s (stop hammering CPA to avoid IP ban): %s", left.Round(time.Second), last)
	}
	cacheKey := m.url + "|" + m.key
	authListCacheMu.Lock()
	if len(authListCacheData) > 0 && authListCacheKey == cacheKey && time.Since(authListCacheAt) < authListCacheTTL {
		out := copyAuthFiles(authListCacheData)
		authListLastStale = false
		authListLastErr = ""
		authListCacheMu.Unlock()
		return out, nil
	}
	// keep a sticky snapshot for failure fallback (may be older than TTL)
	staleSnap := copyAuthFiles(authListCacheData)
	staleKey := authListCacheKey
	staleAt := authListCacheAt
	authListCacheMu.Unlock()

	body, err := mgmtHTTP(http.MethodGet, m.url+"/v0/management/auth-files", nil, m.key)
	if err != nil {
		if isMgmtAuthHTTPError(err) {
			noteMgmtAuthFailure(err.Error())
		}
		// sticky fallback: never force zero inventory into UI/metrics on transient errors
		if len(staleSnap) > 0 && staleKey == cacheKey && !staleAt.IsZero() && time.Since(staleAt) < authListStaleMax {
			authListCacheMu.Lock()
			authListLastErr = err.Error()
			authListLastStale = true
			authListCacheMu.Unlock()
			return staleSnap, nil
		}
		authListCacheMu.Lock()
		authListLastErr = err.Error()
		authListLastStale = true
		authListCacheMu.Unlock()
		return nil, err
	}
	var resp struct {
		Files []mgmtAuthEntry `json:"files"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		if len(staleSnap) > 0 && staleKey == cacheKey && !staleAt.IsZero() && time.Since(staleAt) < authListStaleMax {
			authListCacheMu.Lock()
			authListLastErr = "decode: " + err.Error()
			authListLastStale = true
			authListCacheMu.Unlock()
			return staleSnap, nil
		}
		return nil, fmt.Errorf("decode auth-files: %w", err)
	}
	out := make([]xaiquota.AuthFile, 0, len(resp.Files))
	for _, f := range resp.Files {
		account := f.Account
		if account == "" {
			account = f.Email
		}
		at := f.AccountType
		if at == "" {
			at = f.Type
		}
		out = append(out, xaiquota.AuthFile{
			AuthIndex:   f.AuthIndex,
			Name:        f.Name,
			Provider:    f.Provider,
			Account:     account,
			Disabled:    f.Disabled,
			Success:     f.Success,
			Failed:      f.Failed,
			Note:        f.Note,
			Label:       f.Label,
			Prefix:      f.Prefix,
			Tag:         f.Tag,
			AccountType: at,
		})
	}
	// Sticky guard removed: a successful empty response from CPA means
	// all credentials were deleted (e.g. after patrol sweep). Accept as real.
	// Sticky protection only applies to network/decode errors above, not to
	// a valid HTTP 200 with zero files.
	xt, xe, xd := recountXAI(out)
	authListCacheMu.Lock()
	authListCacheAt = time.Now()
	authListCacheKey = cacheKey
	authListCacheData = copyAuthFiles(out)
	authListLastErr = ""
	authListLastStale = false
	authListLastXAI = xt
	authListLastEn = xe
	authListLastDis = xd
	authListCacheMu.Unlock()
	clearMgmtAuthCircuit()
	return out, nil
}

// invalidateAuthListCache drops cached inventory and all derived metrics after mutate ops.
func invalidateAuthListCache() {
	authListCacheMu.Lock()
	authListCacheData = nil
	authListCacheAt = time.Time{}
	authListCacheKey = ""
	authListLastErr = ""
	authListLastStale = false
	authListLastXAI = 0
	authListLastEn = 0
	authListLastDis = 0
	authListCacheMu.Unlock()
}

func (m *mgmtAuth) SetDisabled(authIndex string, disabled bool) (bool, error) {
	if m == nil || m.url == "" || m.key == "" {
		return false, fmt.Errorf("management not configured")
	}
	files, err := m.List()
	if err != nil {
		return false, err
	}
	var name string
	prev := false
	found := false
	for _, f := range files {
		if f.AuthIndex == authIndex {
			name = f.Name
			prev = f.Disabled
			found = true
			break
		}
	}
	if !found || name == "" {
		return false, fmt.Errorf("auth file not found for index %s", authIndex)
	}
	if prev == disabled {
		return prev, nil
	}
	payload, _ := json.Marshal(map[string]any{"name": name, "disabled": disabled})
	if _, err := mgmtHTTP(http.MethodPatch, m.url+"/v0/management/auth-files/status", payload, m.key); err != nil {
		return prev, err
	}
	invalidateAuthListCache()
	return prev, nil
}


func (m *mgmtAuth) Delete(authIndex string) error {
	if m == nil || m.url == "" || m.key == "" {
		return fmt.Errorf("management not configured")
	}
	files, err := m.List()
	if err != nil {
		return err
	}
	var name string
	for _, f := range files {
		if f.AuthIndex == authIndex {
			name = f.Name
			break
		}
	}
	if name == "" {
		return fmt.Errorf("auth file not found for index %s", authIndex)
	}
	target := m.url + "/v0/management/auth-files?name=" + urlEncode(name)
	err = mgmtHTTPDelete(target, m.key)
	if err == nil {
		invalidateAuthListCache()
	}
	return err
}

func mgmtHTTPDelete(target, key string) error {
	client := newMgmtHTTPClient(15 * time.Second)
	req, err := http.NewRequest(http.MethodDelete, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Management-Key", key)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("mgmt DELETE %s status %d: %s", target, resp.StatusCode, truncate(string(raw), 160))
	}
	return nil
}

func urlEncode(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
			b.WriteByte(c)
		} else if c == ' ' {
			b.WriteByte('+')
		} else {
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}
func mgmtHTTP(method, target string, body []byte, key string) ([]byte, error) {
	client := newMgmtHTTPClient(15 * time.Second)
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, target, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Management-Key", key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return raw, fmt.Errorf("mgmt %s %s status %d: %s", method, target, resp.StatusCode, truncate(string(raw), 160))
	}
	return raw, nil
}


// writePluginConfig merges patch into CPA plugin config and PUTs full config back.
// CPA partial PUT replaces the whole plugin config block, so we always GET+merge first.
func writePluginConfig(cfg xaiquota.Config, patch map[string]any) error {
	base := resolveManagementBaseURL(cfg.ManagementURL)
	key := resolveManagementKey(cfg.ManagementKey)
	if base == "" || key == "" {
		return fmt.Errorf("management not configured (set management_url/key or CPA_MANAGEMENT_BASE_URL / CPA_MANAGEMENT_KEY)")
	}
	target := base + "/v0/management/plugins/" + pluginID + "/config"
	raw, err := mgmtHTTP(http.MethodGet, target, nil, key)
	if err != nil {
		return fmt.Errorf("get plugin config: %w", err)
	}
	var full map[string]any
	if err := json.Unmarshal(raw, &full); err != nil {
		return fmt.Errorf("decode plugin config: %w", err)
	}
	if full == nil {
		full = map[string]any{}
	}
	for k, v := range patch {
		full[k] = v
	}
	body, err := json.Marshal(full)
	if err != nil {
		return err
	}
	if _, err := mgmtHTTP(http.MethodPut, target, body, key); err != nil {
		return fmt.Errorf("put plugin config: %w", err)
	}
	return nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "..."
}