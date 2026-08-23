package gateway

// update_check.go — background release-manifest checker. On boot and every
// updateCheckInterval it fetches latest.json from the release CDN and caches
// whether a newer Sference Switch exists; /v1/admin/status serves the cached
// snapshot so the menubar app can surface "Update available" without any
// network logic of its own.
//
// Silence rules: a dev build never checks (its version compares as older
// than everything, so it would nag forever), and a fetch failure keeps the
// last known state and logs to stderr only — update information is
// best-effort and must never alarm.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/sference/sference-switch/gateway/internal/release"
	"github.com/sference/sference-switch/gateway/internal/version"
)

// updateCheckInterval is how often the gateway re-polls latest.json. The
// manifest itself is CDN-cached for a minute; this cadence is about how
// quickly an install learns about a release, not about origin load.
const updateCheckInterval = 6 * time.Hour

// updateStatus is the cached outcome of the last completed check. The zero
// value (never checked, or a dev build) reports available=false with empty
// versions, which the app renders as no update UI.
type updateStatus struct {
	Available      bool
	LatestVersion  string
	CurrentVersion string
	CheckedAt      time.Time
}

// Test seams: tests substitute a local manifest server and a fixed version.
var (
	updateCheckBaseURL    = release.BaseURL
	updateCheckChannel    = release.Channel
	updateCheckHTTPClient = &http.Client{Timeout: 10 * time.Second}
	updateCheckVersion    = func() string { return version.Version }
)

// startUpdateCheck starts exactly one check loop, alongside the catalog
// refresh loops in Run. New/Serve stay side-effect free for tests.
func (g *Gateway) startUpdateCheck(ctx context.Context) {
	if !g.updateCheckStarted.CompareAndSwap(false, true) {
		return
	}
	g.wg.Add(1)
	go g.runUpdateCheck(ctx)
}

func (g *Gateway) runUpdateCheck(ctx context.Context) {
	defer g.wg.Done()
	g.checkForUpdate()
	timer := time.NewTimer(updateCheckInterval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			g.checkForUpdate()
			timer.Reset(updateCheckInterval)
		}
	}
}

// checkForUpdate performs one manifest fetch and records the outcome. A dev
// build skips the fetch entirely; a failed fetch keeps the previous state.
func (g *Gateway) checkForUpdate() {
	current := updateCheckVersion()
	if current == "dev" {
		return
	}
	m, err := release.FetchManifest(updateCheckHTTPClient, updateCheckBaseURL(), updateCheckChannel())
	if err != nil {
		fmt.Fprintf(os.Stderr, "[gateway] update check failed: %v\n", err)
		return
	}
	g.updateMu.Lock()
	g.update = updateStatus{
		Available:      release.CompareSemver(current, m.Version) < 0,
		LatestVersion:  m.Version,
		CurrentVersion: current,
		CheckedAt:      time.Now().UTC(),
	}
	g.updateMu.Unlock()
}

func (g *Gateway) updateSnapshot() updateStatus {
	g.updateMu.Lock()
	defer g.updateMu.Unlock()
	return g.update
}

func (g *Gateway) updateStatusJSON() map[string]any {
	s := g.updateSnapshot()
	return map[string]any{
		"available":       s.Available,
		"latest_version":  s.LatestVersion,
		"current_version": s.CurrentVersion,
		"checked_at":      rfc3339OrEmpty(s.CheckedAt),
	}
}
