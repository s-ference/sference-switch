package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The model-discovery contract: the /model picker only lists gateway models
// when (a) the base URL is non-Anthropic and (b) the discovery flag is set.
// A live /v1/models fetch additionally needs a token-style credential, but
// the picker is primed from its gateway-models.json cache without one — so
// on() primes that cache from the door and installs no credential (an
// apiKeyHelper would shadow the claude.ai login and disable org connectors).

// primeAdapter returns an adapter whose desired door port is a live httptest
// server serving the given /v1/models payload, plus a temp CLAUDE_CONFIG_DIR.
func primeAdapter(t *testing.T, modelsJSON string) *claudeAdapter {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(modelsJSON))
	}))
	t.Cleanup(srv.Close)
	port := strings.TrimPrefix(srv.URL, "http://127.0.0.1:")

	a, _ := testAdapter(t)
	a.desiredPort = port
	p, _ := strconv.Atoi(port)
	_ = p
	a.gatewayPorts = map[string]bool{port: true}
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	return a
}

func readCache(t *testing.T) gatewayModelsCache {
	t.Helper()
	raw, err := os.ReadFile(gatewayModelsCachePath())
	if err != nil {
		t.Fatalf("read picker cache: %v", err)
	}
	var cache gatewayModelsCache
	if err := json.Unmarshal(raw, &cache); err != nil {
		t.Fatalf("parse picker cache: %v", err)
	}
	return cache
}

const primeModelsPayload = `{"data":[
  {"id":"claude-opus-5-5","display_name":"Claude Opus 5.5"},
  {"id":"claude-sference-kimi-k3","display_name":"Kimi-K3 via Sference"},
  {"id":"deepseek-ai/DeepSeek-V4-Flash","display_name":"no claude in the id"}
]}`

func TestClaudeOnPrimesPickerCache(t *testing.T) {
	a := primeAdapter(t, primeModelsPayload)
	writeSettingsFile(t, a, `{"env":{}}`)
	if code := a.on(); code != 0 {
		t.Fatal("on failed")
	}
	env := readTree(t, a.settingsPath)["env"].(map[string]any)
	if env[claudeDiscoveryEnvKey] != claudeDiscoveryEnvValue {
		t.Errorf("discovery flag = %v", env[claudeDiscoveryEnvKey])
	}
	if _, ok := readTree(t, a.settingsPath)["apiKeyHelper"]; ok {
		t.Errorf("no apiKeyHelper may be installed (it shadows the claude.ai login)")
	}
	cache := readCache(t)
	if cache.BaseURL != a.desiredURL() {
		t.Errorf("cache baseUrl = %q, want %q", cache.BaseURL, a.desiredURL())
	}
	if len(cache.Models) != 2 {
		t.Fatalf("cache models = %v (the non-claude id must be filtered out)", cache.Models)
	}
	if cache.Models[1]["id"] != "claude-sference-kimi-k3" || cache.Models[1]["display_name"] != "Kimi-K3 via Sference" {
		t.Errorf("alias entry = %+v", cache.Models[1])
	}
	if fi, err := os.Stat(gatewayModelsCachePath()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("cache mode = %v, %v", fi, err)
	}
}

func TestClaudeOffRemovesPrimedCache(t *testing.T) {
	a := primeAdapter(t, primeModelsPayload)
	writeSettingsFile(t, a, `{"env":{}}`)
	if code := a.on(); code != 0 {
		t.Fatal("on failed")
	}
	if code := a.off(); code != 0 {
		t.Fatal("off failed")
	}
	if _, err := os.Stat(gatewayModelsCachePath()); !os.IsNotExist(err) {
		t.Errorf("primed cache survived off")
	}
}

func TestClaudeOffKeepsForeignCache(t *testing.T) {
	a := primeAdapter(t, primeModelsPayload)
	writeSettingsFile(t, a, `{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:`+a.desiredPort+`","CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY":"1"}}`)
	// A cache belonging to some other gateway must survive off.
	foreign := gatewayModelsCache{BaseURL: "https://corp-gateway.example.com", FetchedAt: 1, Models: []map[string]any{{"id": "claude-x"}}}
	raw, _ := json.Marshal(foreign)
	if err := os.MkdirAll(filepath.Dir(gatewayModelsCachePath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gatewayModelsCachePath(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := a.off(); code != 0 {
		t.Fatal("off failed")
	}
	cache := readCache(t)
	if cache.BaseURL != foreign.BaseURL {
		t.Errorf("foreign cache clobbered: %q", cache.BaseURL)
	}
}

// A down door must not fail on(): the wiring lands, priming only warns.
func TestClaudeOnPrimeFailureIsWarnOnly(t *testing.T) {
	a, out := testAdapter(t) // door port 8081: nothing listens
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	writeSettingsFile(t, a, `{"env":{}}`)
	if code := a.on(); code != 0 {
		t.Fatal("on failed despite a down door")
	}
	if !strings.Contains(out.String(), "could not prime") {
		t.Errorf("expected a prime warning, got %q", out.String())
	}
	if v, _ := envValue(t, a.settingsPath, claudeManagedEnvKey); v != "http://127.0.0.1:8081" {
		t.Errorf("base URL not written: %q", v)
	}
}

func TestClaudeDiscoveryFlagBackupIsNotPoison(t *testing.T) {
	a, _ := testAdapter(t)
	bak := &claudeBackup{Values: map[string]string{claudeDiscoveryEnvKey: "1"}}
	if a.poisonedBackup(bak) {
		t.Errorf("a backed-up discovery flag must not poison the restore (it is inert without the gateway)")
	}
}

// A user-set "0" is a deliberate opt-out: on() still wires (the flag is
// required for the picker) but off() must restore their value.
func TestClaudeOffRestoresUserDiscoveryZero(t *testing.T) {
	a := primeAdapter(t, primeModelsPayload)
	writeSettingsFile(t, a, `{"env":{"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY":"0"}}`)
	if code := a.on(); code != 0 {
		t.Fatal("on failed")
	}
	if v, _ := envValue(t, a.settingsPath, claudeDiscoveryEnvKey); v != "1" {
		t.Fatalf("on did not enable discovery: %q", v)
	}
	if code := a.off(); code != 0 {
		t.Fatal("off failed")
	}
	if v, ok := envValue(t, a.settingsPath, claudeDiscoveryEnvKey); !ok || v != "0" {
		t.Errorf("user's \"0\" not restored: %q ok=%v", v, ok)
	}
}

// The managed-extension path: a second on() while already managed must not
// record the switch-written "1" as the restore value — off() strips, never
// resurrects our own flag.
func TestClaudeOnWhileManagedDoesNotResurrectFlagOnOff(t *testing.T) {
	a := primeAdapter(t, primeModelsPayload)
	writeSettingsFile(t, a, `{"env":{}}`)
	if code := a.on(); code != 0 {
		t.Fatal("first on failed")
	}
	if code := a.on(); code != 0 {
		t.Fatal("second on failed")
	}
	bak, err := loadClaudeBackup(a.backupPath)
	if err != nil || bak == nil {
		t.Fatalf("backup: %v %v", bak, err)
	}
	if v, ok := bak.Values[claudeDiscoveryEnvKey]; ok {
		t.Errorf("backup recorded our own flag value %q as restore state", v)
	}
	if code := a.off(); code != 0 {
		t.Fatal("off failed")
	}
	if _, ok := envValue(t, a.settingsPath, claudeDiscoveryEnvKey); ok {
		t.Errorf("switch-written discovery flag resurrected by off")
	}
}

// An empty catalog primes an empty cache without error; no temp file is
// left behind by the atomic write.
func TestClaudePrimeEmptyCatalog(t *testing.T) {
	a := primeAdapter(t, `{"data":[]}`)
	writeSettingsFile(t, a, `{"env":{}}`)
	if code := a.on(); code != 0 {
		t.Fatal("on failed")
	}
	cache := readCache(t)
	if len(cache.Models) != 0 {
		t.Errorf("empty catalog primed %d models", len(cache.Models))
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(gatewayModelsCachePath()), ".gateway-models-*.tmp"))
	if err != nil || len(matches) != 0 {
		t.Errorf("temp files left behind: %v %v", matches, err)
	}
}

// A cache primed for a door port that has since changed is still ours:
// the backup's recorded URL removes it on off.
func TestClaudeOffRemovesCacheForOldPort(t *testing.T) {
	a := primeAdapter(t, primeModelsPayload)
	writeSettingsFile(t, a, `{"env":{}}`)
	if code := a.on(); code != 0 {
		t.Fatal("on failed")
	}
	// The door moved: the cache's baseUrl no longer matches any gateway
	// port, but the backup remembers it.
	bak, err := loadClaudeBackup(a.backupPath)
	if err != nil || bak == nil {
		t.Fatalf("backup: %v %v", bak, err)
	}
	if bak.PrimedBaseURL == "" {
		t.Fatal("backup did not record the primed base URL")
	}
	a.gatewayPorts = map[string]bool{"59999": true}
	a.desiredPort = "59999"
	if code := a.off(); code != 0 {
		t.Fatal("off failed")
	}
	if _, err := os.Stat(gatewayModelsCachePath()); !os.IsNotExist(err) {
		t.Errorf("cache for the old port survived off")
	}
}
