// Package release holds the release-manifest model and the fetch/validate
// logic shared by the CLI's `upgrade` command and the gateway's
// update-availability checker. The manifest is flat — one field per line,
// no nested objects — because the POSIX-sh installer extracts fields with
// sed and macOS has no guaranteed jq.
package release

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// Manifest mirrors the flat manifest.json published to the release CDN.
type Manifest struct {
	SchemaVersion int    `json:"schema_version"`
	Product       string `json:"product"`
	Channel       string `json:"channel"`
	Tag           string `json:"tag"`
	Version       string `json:"version"`
	PublishedAt   string `json:"published_at"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	Filename      string `json:"filename"`
	Path          string `json:"path"`
	ChecksumsPath string `json:"checksums_path"`
	SHA256        string `json:"sha256"`
	Size          int64  `json:"size"`
	Signing       string `json:"signing"`
	Notarized     bool   `json:"notarized"`
	MinimumMacOS  string `json:"minimum_macos"`
}

// BaseURL resolves the release base URL. SFERENCE_SWITCH_BASE_URL overrides
// it for local testing against a mirror of the S3 layout.
func BaseURL() string {
	if v := os.Getenv("SFERENCE_SWITCH_BASE_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://get.sference.com"
}

// Channel resolves the release channel. SFERENCE_SWITCH_CHANNEL overrides
// it; the path shape reserves channels beyond stable for later.
func Channel() string {
	if v := os.Getenv("SFERENCE_SWITCH_CHANNEL"); v != "" {
		return v
	}
	return "stable"
}

// FetchManifest downloads and validates latest.json for the channel.
func FetchManifest(client *http.Client, baseURL, channel string) (*Manifest, error) {
	url := baseURL + "/sference-switch/" + channel + "/latest.json"
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch manifest: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("fetch manifest: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read manifest: %v", err)
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %v", err)
	}
	if m.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported manifest schema_version: %d", m.SchemaVersion)
	}
	if m.Product != "sference-switch" {
		return nil, fmt.Errorf("manifest product is %q, want sference-switch", m.Product)
	}
	if m.OS != "darwin" {
		return nil, fmt.Errorf("manifest os is %q, want darwin", m.OS)
	}
	if m.Arch != "universal" {
		return nil, fmt.Errorf("manifest arch is %q, want universal", m.Arch)
	}
	if !isHex64(m.SHA256) {
		return nil, fmt.Errorf("manifest sha256 is not 64 lowercase hex")
	}
	if strings.Contains(m.Path, "..") || strings.HasPrefix(m.Path, "/") {
		return nil, fmt.Errorf("manifest path is unsafe: %q", m.Path)
	}
	if !strings.HasPrefix(m.Path, "sference-switch/") {
		return nil, fmt.Errorf("manifest path must start with sference-switch/")
	}
	if strings.Contains(m.ChecksumsPath, "..") || strings.HasPrefix(m.ChecksumsPath, "/") {
		return nil, fmt.Errorf("manifest checksums_path is unsafe: %q", m.ChecksumsPath)
	}
	if !strings.HasPrefix(m.ChecksumsPath, "sference-switch/") {
		return nil, fmt.Errorf("manifest checksums_path must start with sference-switch/")
	}
	return &m, nil
}

// CompareSemver compares two semver strings. Returns -1, 0, or 1.
// "dev" compares as less than everything.
func CompareSemver(a, b string) int {
	if a == "dev" {
		return -1
	}
	if b == "dev" {
		return 1
	}
	a = strings.TrimPrefix(a, "v")
	b = strings.TrimPrefix(b, "v")
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		var av, bv int
		if i < len(aParts) {
			av, _ = strconv.Atoi(aParts[i])
		}
		if i < len(bParts) {
			bv, _ = strconv.Atoi(bParts[i])
		}
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}
	return 0
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
