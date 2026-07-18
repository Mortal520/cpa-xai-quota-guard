package main

import (
	"os"
	"testing"
)

func TestResolveManagementBaseURL_Default(t *testing.T) {
	os.Unsetenv("CPA_MANAGEMENT_BASE_URL")
	os.Unsetenv("CPA_BASE_URL")
	os.Unsetenv("PORT")
	os.Unsetenv("CPA_PORT")
	os.Unsetenv("CPA_TLS")
	got := resolveManagementBaseURL("")
	if got != defaultManagementBaseURL {
		t.Fatalf("empty cfg want %s got %s", defaultManagementBaseURL, got)
	}
}

func TestResolveManagementBaseURL_ExplicitWins(t *testing.T) {
	os.Setenv("CPA_MANAGEMENT_BASE_URL", "http://env.example:9")
	defer os.Unsetenv("CPA_MANAGEMENT_BASE_URL")
	got := resolveManagementBaseURL("http://yaml:8317/")
	if got != "http://yaml:8317" {
		t.Fatalf("explicit yaml should win, got %s", got)
	}
}

func TestResolveManagementBaseURL_Env(t *testing.T) {
	os.Unsetenv("CPA_BASE_URL")
	os.Setenv("CPA_MANAGEMENT_BASE_URL", "http://10.10.10.5:8317/")
	defer os.Unsetenv("CPA_MANAGEMENT_BASE_URL")
	got := resolveManagementBaseURL("")
	if got != "http://10.10.10.5:8317" {
		t.Fatalf("env base, got %s", got)
	}
}

func TestResolveManagementBaseURL_Port(t *testing.T) {
	os.Unsetenv("CPA_MANAGEMENT_BASE_URL")
	os.Unsetenv("CPA_BASE_URL")
	os.Setenv("PORT", "9999")
	defer os.Unsetenv("PORT")
	got := resolveManagementBaseURL("")
	if got != "http://127.0.0.1:9999" {
		t.Fatalf("port env, got %s", got)
	}
}

func TestResolveManagementKey_EnvFallback(t *testing.T) {
	os.Unsetenv("MANAGEMENT_PASSWORD")
	os.Unsetenv("CPA_MANAGEMENT_KEY")
	os.Unsetenv("MANAGEMENT_KEY")
	if resolveManagementKey("  k1  ") != "k1" {
		t.Fatal("cfg key")
	}
	os.Setenv("CPA_MANAGEMENT_KEY", "from-env")
	defer os.Unsetenv("CPA_MANAGEMENT_KEY")
	if resolveManagementKey("") != "from-env" {
		t.Fatal("env key")
	}
}

func TestIsLoopbackHost(t *testing.T) {
	if !isLoopbackHost("127.0.0.1") || !isLoopbackHost("localhost") {
		t.Fatal("loopback")
	}
	if isLoopbackHost("10.10.10.5") {
		t.Fatal("lan not loopback")
	}
}
