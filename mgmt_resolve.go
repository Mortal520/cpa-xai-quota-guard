package main

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/mortal/cpa-xai-quota-guard/internal/xaiquota"
)

// Default CPA management listen address (same default as CLIProxyAPI / grok-inspection).
const defaultManagementBaseURL = "http://127.0.0.1:8317"

func envTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func isLoopbackHost(host string) bool {
	h := strings.TrimSpace(strings.ToLower(host))
	if h == "" {
		return false
	}
	if h == "localhost" || h == "127.0.0.1" || h == "::1" {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// loopbackAwareProxy skips HTTP(S)_PROXY for loopback management calls
// (corporate proxies often break 127.0.0.1 inside Docker).
func loopbackAwareProxy(req *http.Request) (*url.URL, error) {
	if req != nil && req.URL != nil && isLoopbackHost(req.URL.Hostname()) {
		return nil, nil
	}
	return http.ProxyFromEnvironment(req)
}

func newMgmtHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy: loopbackAwareProxy,
		},
	}
}

func managementTLSPreferred() bool {
	return envTruthy(os.Getenv("CPA_TLS")) ||
		envTruthy(os.Getenv("CPA_TLS_ENABLE")) ||
		envTruthy(os.Getenv("TLS_ENABLE")) ||
		envTruthy(os.Getenv("CPA_MANAGEMENT_TLS"))
}

// configuredManagementBaseURL returns env-forced base URL when set.
// CPA_MANAGEMENT_BASE_URL / CPA_BASE_URL take absolute priority (Docker host network etc).
func configuredManagementBaseURL() (string, bool) {
	if value := firstNonEmpty(os.Getenv("CPA_MANAGEMENT_BASE_URL"), os.Getenv("CPA_BASE_URL")); value != "" {
		return strings.TrimRight(strings.TrimSpace(value), "/"), true
	}
	return "", false
}

// resolveManagementBaseURL picks CPA management API origin for in-process calls.
// Order: explicit cfg → env CPA_MANAGEMENT_BASE_URL → PORT/CPA_PORT on 127.0.0.1 → default :8317.
// Pure CPA: empty yaml management_url still works for co-located plugin (Docker same network namespace).
func resolveManagementBaseURL(cfgURL string) string {
	if u := strings.TrimRight(strings.TrimSpace(cfgURL), "/"); u != "" {
		return u
	}
	if value, ok := configuredManagementBaseURL(); ok {
		return value
	}
	scheme := "http"
	if managementTLSPreferred() {
		scheme = "https"
	}
	if port := strings.TrimSpace(firstNonEmpty(os.Getenv("PORT"), os.Getenv("CPA_PORT"))); port != "" {
		port = strings.TrimPrefix(port, ":")
		return fmt.Sprintf("%s://127.0.0.1:%s", scheme, port)
	}
	if scheme == "https" {
		return "https://127.0.0.1:8317"
	}
	return defaultManagementBaseURL
}

// resolveManagementKey: yaml/config key first, then process env for headless pure-CPA installs.
func resolveManagementKey(cfgKey string) string {
	if k := strings.TrimSpace(cfgKey); k != "" {
		return k
	}
	return firstNonEmpty(os.Getenv("MANAGEMENT_PASSWORD"), os.Getenv("CPA_MANAGEMENT_KEY"), os.Getenv("MANAGEMENT_KEY"))
}

// normalizeRuntimeConfig fills empty management_url/key from env/defaults so pure CPA works
// without requiring CPAMP or explicit management_url in plugin yaml.
func normalizeRuntimeConfig(cfg *xaiquota.Config) {
	if cfg == nil {
		return
	}
	cfg.ManagementURL = resolveManagementBaseURL(cfg.ManagementURL)
	cfg.ManagementKey = resolveManagementKey(cfg.ManagementKey)
}

// managementURLSource describes how the effective management base was chosen (no secrets).
func managementURLSource(rawURL string) string {
	if strings.TrimSpace(rawURL) != "" {
		return "config"
	}
	if _, ok := configuredManagementBaseURL(); ok {
		return "env"
	}
	return "default"
}
