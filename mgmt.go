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
	return &mgmtAuth{
		url: strings.TrimRight(strings.TrimSpace(cfg.ManagementURL), "/"),
		key: strings.TrimSpace(cfg.ManagementKey),
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
	client := &http.Client{Timeout: 15 * time.Second}
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
	client := &http.Client{Timeout: 15 * time.Second}
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
	if cfg.ManagementURL == "" || cfg.ManagementKey == "" {
		return fmt.Errorf("management not configured")
	}
	base := strings.TrimRight(strings.TrimSpace(cfg.ManagementURL), "/")
	target := base + "/v0/management/plugins/" + pluginID + "/config"
	raw, err := mgmtHTTP(http.MethodGet, target, nil, cfg.ManagementKey)
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
	if _, err := mgmtHTTP(http.MethodPut, target, body, cfg.ManagementKey); err != nil {
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