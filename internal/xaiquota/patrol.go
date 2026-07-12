package xaiquota

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// PatrolConfig controls the proactive credential patrol.
type PatrolConfig struct {
	PatrolEnabled   bool
	PatrolInterval  float64 // seconds between sweeps (0 = timer-only/manual)
	PatrolTimeout   float64 // per-probe HTTP timeout seconds
	PatrolBatchSize int     // max credentials per sweep (0 = unlimited)
	PatrolAuthDir   string  // directory containing auth file JSONs
}

// patrolState tracks the in-progress or last-completed sweep.
type patrolState struct {
	mu              sync.Mutex
	running         bool
	startedAtMS     int64
	completedAtMS   int64
	totalProbed     int
	totalDeleted     int
	totalErrors     int
	totalAlive      int
	currentBatch    int
	lastError       string
	lastSweepLog    []patrolLogEntry
	stopRequested   bool
}

type patrolLogEntry struct {
	TimeMS   int64  `json:"time_ms"`
	AuthIndex string `json:"auth_index"`
	FileName string `json:"file_name"`
	Account  string `json:"account"`
	Action   string `json:"action"`   // "alive", "deleted", "error", "cooldown_skip"
	Reason   string `json:"reason"`
	HTTPCode int    `json:"http_code,omitempty"`
}

// PatrolStatus is the JSON view for the UI.
type PatrolStatus struct {
	Running       bool              `json:"running"`
	StartedAtMS   int64             `json:"started_at_ms"`
	CompletedAtMS int64             `json:"completed_at_ms"`
	TotalProbed   int               `json:"total_probed"`
	TotalDeleted  int               `json:"total_deleted"`
	TotalErrors   int               `json:"total_errors"`
	TotalAlive    int               `json:"total_alive"`
	CurrentBatch  int               `json:"current_batch"`
	LastError     string            `json:"last_error,omitempty"`
	RecentLog     []patrolLogEntry  `json:"recent_log,omitempty"`
}

// authFileJSON is the on-disk structure of a CPA auth file.
type authFileJSON struct {
	AccessToken string            `json:"access_token"`
	BaseURL     string            `json:"base_url"`
	AuthKind    string            `json:"auth_kind"`
	Type         string            `json:"type"`
	Email       string            `json:"email"`
	Disabled    bool              `json:"disabled"`
	Headers     map[string]string `json:"headers"`
}

// PatrolSweep iterates all enabled xAI auth files, reads the token from disk,
// probes the upstream directly, and deletes dead credentials.
func (g *Guard) PatrolSweep() PatrolStatus {
	g.mu.Lock()
	if g.patrol.running {
		g.mu.Unlock()
		return g.PatrolStatus()
	}
	g.patrol.running = true
	g.patrol.startedAtMS = time.Now().UnixMilli()
	g.patrol.completedAtMS = 0
	g.patrol.totalProbed = 0
	g.patrol.totalDeleted = 0
	g.patrol.totalErrors = 0
	g.patrol.totalAlive = 0
	g.patrol.currentBatch = 0
	g.patrol.lastError = ""
	g.patrol.lastSweepLog = nil
	g.patrol.stopRequested = false
	g.mu.Unlock()

	defer func() {
		g.mu.Lock()
		g.patrol.running = false
		g.patrol.completedAtMS = time.Now().UnixMilli()
		g.mu.Unlock()
	}()

	cfg := g.Config()
	if !cfg.Enabled {
		g.patrol.lastError = "plugin disabled"
		return g.PatrolStatus()
	}
	if g.auth == nil {
		g.patrol.lastError = "auth lookup nil"
		return g.PatrolStatus()
	}
	authDir := strings.TrimSpace(cfg.PatrolAuthDir)
	if authDir == "" {
		g.patrol.lastError = "patrol_auth_dir not configured"
		return g.PatrolStatus()
	}

	files, err := g.auth.List()
	if err != nil {
		g.patrol.lastError = fmt.Sprintf("list auth files: %v", err)
		return g.PatrolStatus()
	}

	// Filter: only xAI, only enabled, only with a path we can read.
	batchLimit := cfg.PatrolBatchSize
	if batchLimit <= 0 {
		batchLimit = len(files)
	}

	probeTimeout := time.Duration(cfg.PatrolTimeout) * time.Second
	if probeTimeout <= 0 {
		probeTimeout = 15 * time.Second
	}

	for _, f := range files {
		// Check stop request between iterations.
		g.mu.Lock()
		stop := g.patrol.stopRequested
		g.mu.Unlock()
		if stop {
			break
		}
		if !IsXAIProvider(f.Provider, "") {
			continue
		}
		if f.Disabled {
			continue
		}
		if g.patrol.totalProbed >= batchLimit {
			break
		}

		g.patrol.mu.Lock()
		g.patrol.totalProbed++
		g.patrol.currentBatch++
		g.patrol.mu.Unlock()

		result := g.probeOneCredential(f, authDir, probeTimeout)
		g.appendPatrolLog(result)
	}

	return g.PatrolStatus()
}

// probeResult holds the outcome of probing one credential.
type probeResult struct {
	authIndex string
	fileName  string
	account   string
	action    string // "alive", "deleted", "error", "cooldown_skip"
	reason    string
	httpCode  int
}

// probeOneCredential reads the auth file, extracts token, sends a minimal probe.
func (g *Guard) probeOneCredential(f AuthFile, authDir string, timeout time.Duration) probeResult {
	filePath := filepath.Join(authDir, f.Name)
	raw, err := os.ReadFile(filePath)
	if err != nil {
		// Try with .json extension if not present
		if !strings.HasSuffix(f.Name, ".json") {
			raw, err = os.ReadFile(filePath + ".json")
		}
	}
	if err != nil {
		return probeResult{
			authIndex: f.AuthIndex,
			fileName:  f.Name,
			account:   f.Account,
			action:    "error",
			reason:    fmt.Sprintf("read auth file: %v", err),
		}
	}

	var af authFileJSON
	if err := json.Unmarshal(raw, &af); err != nil {
		return probeResult{
			authIndex: f.AuthIndex,
			fileName:  f.Name,
			account:   f.Account,
			action:    "error",
			reason:    fmt.Sprintf("parse auth file: %v", err),
		}
	}

	if af.AccessToken == "" {
		return probeResult{
			authIndex: f.AuthIndex,
			fileName:  f.Name,
			account:   f.Account,
			action:    "error",
			reason:    "no access_token in auth file",
		}
	}

	// Skip if currently in plugin_auto cooldown.
	live := g.storeGet(f.AuthIndex)
	if live != nil && live.State == StateAutoDisabled && live.DisableSource == SourcePluginAuto {
		return probeResult{
			authIndex: f.AuthIndex,
			fileName:  f.Name,
			account:   f.Account,
			action:    "cooldown_skip",
			reason:    "currently in plugin_auto cooldown",
		}
	}

	baseURL := strings.TrimRight(strings.TrimSpace(af.BaseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.x.ai/v1"
	}

	// Minimal request: 1 token, cheapest model.
	probeBody := `{"model":"grok-3","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`
	req, err := http.NewRequest(http.MethodPost, baseURL+"/chat/completions", strings.NewReader(probeBody))
	if err != nil {
		return probeResult{
			authIndex: f.AuthIndex,
			fileName:  f.Name,
			account:   f.Account,
			action:    "error",
			reason:    fmt.Sprintf("build request: %v", err),
		}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+af.AccessToken)
	// Apply custom headers from auth file (e.g. X-XAI-Token-Auth).
	for k, v := range af.Headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: timeout}
	if cfg := g.Config(); cfg.PatrolProxyURL != "" {
		if proxyURL, err := url.Parse(cfg.PatrolProxyURL); err == nil {
			client.Transport = &http.Transport{Proxy: http.ProxyURL(proxyURL)}
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		g.patrol.mu.Lock()
		g.patrol.totalErrors++
		g.patrol.mu.Unlock()
		return probeResult{
			authIndex: f.AuthIndex,
			fileName:  f.Name,
			account:   f.Account,
			action:    "error",
			reason:    fmt.Sprintf("probe request failed (network): %v", err),
		}
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(resp.Body)
	bodyStr := string(bodyBytes)
	code := resp.StatusCode

	// 200 = alive; 429 = rate-limited (alive, just quota); 5xx = server (alive, transient).
	// 403 permission-denied / 401 invalid / 402 spending-limit = dead → delete.
	if code == http.StatusOK {
		g.patrol.mu.Lock()
		g.patrol.totalAlive++
		g.patrol.mu.Unlock()
		return probeResult{
			authIndex: f.AuthIndex,
			fileName:  f.Name,
			account:   f.Account,
			action:    "alive",
			reason:    "200 OK",
			httpCode:  code,
		}
	}
	if code == http.StatusTooManyRequests {
		g.patrol.mu.Lock()
		g.patrol.totalAlive++
		g.patrol.mu.Unlock()
		return probeResult{
			authIndex: f.AuthIndex,
			fileName:  f.Name,
			account:   f.Account,
			action:    "alive",
			reason:    "429 rate-limited (quota exhausted, not dead)",
			httpCode:  code,
		}
	}
	if IsPermissionDenied(code, bodyStr) || IsInvalidCredentials(code, bodyStr) || IsSpendingLimitBlocked(code, bodyStr) {
		// Dead credential → delete.
		if err := g.auth.Delete(f.AuthIndex); err != nil {
			g.patrol.mu.Lock()
			g.patrol.totalErrors++
			g.patrol.mu.Unlock()
			return probeResult{
				authIndex: f.AuthIndex,
				fileName:  f.Name,
				account:   f.Account,
				action:    "error",
				reason:    fmt.Sprintf("delete failed: %v", err),
				httpCode:  code,
			}
		}
		_ = g.storeMarkActive(f.AuthIndex)
		if g.store != nil {
			_ = g.store.AppendDelete(DeleteEvent{
				AuthIndex:   f.AuthIndex,
				FileName:    f.Name,
				Account:     f.Account,
				Provider:    "xai",
				Reason:      fmt.Sprintf("patrol: %s", truncate(bodyStr, 240)),
				DeletedAtMS: time.Now().UnixMilli(),
			})
		}
		g.patrol.mu.Lock()
		g.patrol.totalDeleted++
		g.patrol.mu.Unlock()
		g.logf("warn", "patrol 删除死号 auth=%s file=%s code=%d reason=%s", f.AuthIndex, f.Name, code, truncate(bodyStr, 120))
		g.NotifyWebhook("patrol_dead_credential_delete", map[string]any{
			"auth_index": f.AuthIndex,
			"file_name":  f.Name,
			"account":    f.Account,
			"http_code":  code,
			"reason":     truncate(bodyStr, 160),
		})
		return probeResult{
			authIndex: f.AuthIndex,
			fileName:  f.Name,
			account:   f.Account,
			action:    "deleted",
			reason:    truncate(bodyStr, 200),
			httpCode:  code,
		}
	}

	// Other status codes: treat as alive (don't delete on unknown errors).
	g.patrol.mu.Lock()
	g.patrol.totalAlive++
	g.patrol.mu.Unlock()
	return probeResult{
		authIndex: f.AuthIndex,
		fileName:  f.Name,
		account:   f.Account,
		action:    "alive",
		reason:    fmt.Sprintf("HTTP %d (not a dead-credential signal)", code),
		httpCode:  code,
	}
}

// appendPatrolLog adds a probe result to the in-memory sweep log (capped at 500 entries).
func (g *Guard) appendPatrolLog(r probeResult) {
	entry := patrolLogEntry{
		TimeMS:    time.Now().UnixMilli(),
		AuthIndex: r.authIndex,
		FileName:  r.fileName,
		Account:   r.account,
		Action:    r.action,
		Reason:    r.reason,
		HTTPCode:  r.httpCode,
	}
	g.patrol.mu.Lock()
	if len(g.patrol.lastSweepLog) >= 500 {
		g.patrol.lastSweepLog = g.patrol.lastSweepLog[len(g.patrol.lastSweepLog)-499:]
	}
	g.patrol.lastSweepLog = append(g.patrol.lastSweepLog, entry)
	g.patrol.mu.Unlock()
}

// PatrolStatus returns the current patrol state for the UI.
func (g *Guard) PatrolStatus() PatrolStatus {
	g.patrol.mu.Lock()
	defer g.patrol.mu.Unlock()

	log := make([]patrolLogEntry, len(g.patrol.lastSweepLog))
	copy(log, g.patrol.lastSweepLog)

	return PatrolStatus{
		Running:       g.patrol.running,
		StartedAtMS:   g.patrol.startedAtMS,
		CompletedAtMS: g.patrol.completedAtMS,
		TotalProbed:   g.patrol.totalProbed,
		TotalDeleted:  g.patrol.totalDeleted,
		TotalErrors:   g.patrol.totalErrors,
		TotalAlive:    g.patrol.totalAlive,
		CurrentBatch:  g.patrol.currentBatch,
		LastError:     g.patrol.lastError,
		RecentLog:     log,
	}
}

// PatrolStop signals an in-progress sweep to stop after the current credential.
func (g *Guard) PatrolStop() {
	g.patrol.mu.Lock()
	g.patrol.stopRequested = true
	g.patrol.mu.Unlock()
}

// PatrolRunOnce triggers an async manual sweep if not already running.
// Returns current status immediately; the sweep runs in a goroutine.
func (g *Guard) PatrolRunOnce() PatrolStatus {
	g.mu.Lock()
	if g.patrol.running {
		g.mu.Unlock()
		return g.PatrolStatus()
	}
	g.mu.Unlock()
	go g.PatrolSweep()
	return g.PatrolStatus()
}
