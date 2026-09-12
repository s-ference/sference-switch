package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// manifestServer serves a valid latest.json with the given version.
func manifestServer(t *testing.T, version string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/stable/latest.json") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{
			"schema_version": 1, "product": "sference-switch", "channel": "stable",
			"tag": "v%s", "version": "%s", "published_at": "2026-08-22T00:00:00Z",
			"os": "darwin", "arch": "universal",
			"filename": "sference-switch_%s_darwin_universal.zip",
			"path": "sference-switch/v%s/sference-switch_%s_darwin_universal.zip",
			"checksums_path": "sference-switch/v%s/checksums.txt",
			"sha256": "%064x", "size": 12345678,
			"signing": "adhoc", "notarized": false, "minimum_macos": "13.0"
		}`, version, version, version, version, version, version, 1)
	}))
}

// stubUpdateSeams points the checker at the given server with a fixed
// running version, and restores the real seams on cleanup.
func stubUpdateSeams(t *testing.T, baseURL, currentVersion string) {
	t.Helper()
	oldBase, oldChannel, oldVersion := updateCheckBaseURL, updateCheckChannel, updateCheckVersion
	updateCheckBaseURL = func() string { return baseURL }
	updateCheckChannel = func() string { return "stable" }
	updateCheckVersion = func() string { return currentVersion }
	t.Cleanup(func() {
		updateCheckBaseURL, updateCheckChannel, updateCheckVersion = oldBase, oldChannel, oldVersion
	})
}

// TestCheckForUpdateAvailable: a newer manifest version flips available and
// records both versions plus the check time.
func TestCheckForUpdateAvailable(t *testing.T) {
	srv := manifestServer(t, "0.2.0")
	defer srv.Close()
	stubUpdateSeams(t, srv.URL, "0.1.0")

	g := &Gateway{}
	g.checkForUpdate()

	got := g.updateSnapshot()
	if !got.Available {
		t.Fatal("available = false, want true (0.2.0 > 0.1.0)")
	}
	if got.LatestVersion != "0.2.0" || got.CurrentVersion != "0.1.0" {
		t.Fatalf("versions = %q/%q, want latest 0.2.0 current 0.1.0",
			got.LatestVersion, got.CurrentVersion)
	}
	if got.CheckedAt.IsZero() {
		t.Fatal("checked_at not recorded")
	}
}

// TestCheckForUpdateCurrent: an equal or older manifest leaves available
// false but still records the check.
func TestCheckForUpdateCurrent(t *testing.T) {
	srv := manifestServer(t, "0.1.0")
	defer srv.Close()
	stubUpdateSeams(t, srv.URL, "0.1.0")

	g := &Gateway{}
	g.checkForUpdate()

	got := g.updateSnapshot()
	if got.Available {
		t.Fatal("available = true, want false (same version)")
	}
	if got.LatestVersion != "0.1.0" || got.CheckedAt.IsZero() {
		t.Fatalf("snapshot = %+v, want latest 0.1.0 with a check time", got)
	}
}

// TestCheckForUpdateDevSkips: a dev build never fetches — the server must
// see zero requests and the snapshot stays zero.
func TestCheckForUpdateDevSkips(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	stubUpdateSeams(t, srv.URL, "dev")

	g := &Gateway{}
	g.checkForUpdate()

	if hits != 0 {
		t.Fatalf("dev build fetched the manifest %d times, want 0", hits)
	}
	if got := g.updateSnapshot(); got.Available || got.LatestVersion != "" {
		t.Fatalf("dev snapshot = %+v, want zero value", got)
	}
}

// TestCheckForUpdateFailureKeepsState: a failed fetch must not erase the
// last known good result.
func TestCheckForUpdateFailureKeepsState(t *testing.T) {
	srv := manifestServer(t, "0.2.0")
	stubUpdateSeams(t, srv.URL, "0.1.0")

	g := &Gateway{}
	g.checkForUpdate()
	if !g.updateSnapshot().Available {
		t.Fatal("setup: want available after first check")
	}

	srv.Close() // every subsequent fetch fails
	g.checkForUpdate()

	got := g.updateSnapshot()
	if !got.Available || got.LatestVersion != "0.2.0" {
		t.Fatalf("snapshot after failure = %+v, want the retained 0.2.0 result", got)
	}
}

// TestAdminStatusServesUpdate: /v1/admin/status exposes the cached snapshot
// under "update"; a gateway that never checked reports available=false with
// empty versions (the app renders no update UI for that).
func TestAdminStatusServesUpdate(t *testing.T) {
	srv := manifestServer(t, "0.2.0")
	defer srv.Close()
	stubUpdateSeams(t, srv.URL, "0.1.0")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, upstream.URL)
	g, adminL, _ := newGateway(t, cfg, resolvedAnthropicSference(t))
	defer adminL.Close()
	stop := start(t, g)
	defer stop()

	// Before any check: zero value served.
	block := adminStatusGet(t, g)["update"].(map[string]any)
	if block["available"] != false || block["latest_version"] != "" {
		t.Fatalf("unchecked update block = %+v, want available=false, empty latest", block)
	}

	g.checkForUpdate()
	block = adminStatusGet(t, g)["update"].(map[string]any)
	if block["available"] != true {
		t.Fatalf("update block = %+v, want available=true", block)
	}
	if block["latest_version"] != "0.2.0" || block["current_version"] != "0.1.0" {
		t.Fatalf("update block versions = %v/%v, want 0.2.0/0.1.0",
			block["latest_version"], block["current_version"])
	}
	if block["checked_at"] == "" {
		t.Fatal("update block checked_at empty after a check")
	}
}

// TestAdminUpdateCheckTriggersFetch: calling the endpoint performs a real
// manifest fetch (the menubar-on-open path) and returns the fresh status —
// a just-published release is seen immediately, not up to 6 h later.
func TestAdminUpdateCheckTriggersFetch(t *testing.T) {
	srv := manifestServer(t, "0.2.0")
	defer srv.Close()
	stubUpdateSeams(t, srv.URL, "0.1.0")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, upstream.URL)
	g, adminL, _ := newGateway(t, cfg, resolvedAnthropicSference(t))
	defer adminL.Close()
	stop := start(t, g)
	defer stop()

	// Before: no check has run, so the update block is the zero value.
	if block := adminStatusGet(t, g)["update"].(map[string]any); block["available"] != false {
		t.Fatalf("pre-check update block = %+v, want available=false", block)
	}

	// The app-triggered check must flip it to available.
	resp, err := http.Post(adminURL(g, "/v1/admin/update/check"), "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update/check status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Available      bool   `json:"available"`
		LatestVersion  string `json:"latest_version"`
		CurrentVersion string `json:"current_version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.Available || body.LatestVersion != "0.2.0" || body.CurrentVersion != "0.1.0" {
		t.Fatalf("update/check = %+v, want available with 0.2.0/0.1.0", body)
	}
}

// TestAdminUpdateCheckThrottles: repeated calls reuse the last result within
// the throttle window instead of hammering the manifest server.
func TestAdminUpdateCheckThrottles(t *testing.T) {
	// atomic: the handler runs on the server goroutine while the assertion
	// reads from the test goroutine.
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"schema_version": 1, "product": "sference-switch", "channel": "stable",
			"tag": "0.3.0", "version": "0.3.0",
		})
	}))
	defer srv.Close()
	stubUpdateSeams(t, srv.URL, "0.2.0")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, upstream.URL)
	g, adminL, _ := newGateway(t, cfg, resolvedAnthropicSference(t))
	defer adminL.Close()
	stop := start(t, g)
	defer stop()

	for i := 0; i < 5; i++ {
		resp, err := http.Post(adminURL(g, "/v1/admin/update/check"), "application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	// Exactly one: >1 means the throttle leaked, 0 means the endpoint
	// stopped fetching altogether (a regression that always throttles,
	// including the first call, would pass a `<= 1` assertion).
	if got := hits.Load(); got != 1 {
		t.Fatalf("manifest server hit %d times in the throttle window, want exactly 1", got)
	}
}
