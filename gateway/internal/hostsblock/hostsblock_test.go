package hostsblock

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleHosts = `##
# Host Database
#
127.0.0.1	localhost
255.255.255.255	broadcasthost
::1             localhost
`

const sampleBlock = "\n# sference-switch begin\n127.0.0.1 api.anthropic.com\n::1 api.anthropic.com\n# sference-switch end\n"

func writeHosts(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPresentDetectsBlock(t *testing.T) {
	if Present(writeHosts(t, sampleHosts)) {
		t.Error("clean file reported present")
	}
	if !Present(writeHosts(t, sampleHosts+sampleBlock)) {
		t.Error("block not detected")
	}
}

func TestRemoveRestoresByteIdentical(t *testing.T) {
	p := writeHosts(t, sampleHosts+sampleBlock)
	if err := Remove(p); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != sampleHosts {
		t.Errorf("after remove:\n%q\nwant:\n%q", data, sampleHosts)
	}
	if Present(p) {
		t.Error("block still present after remove")
	}
}

func TestRemoveIsIdempotent(t *testing.T) {
	p := writeHosts(t, sampleHosts)
	if err := Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := Remove(p); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveKeepsOtherEntries(t *testing.T) {
	custom := sampleHosts + "10.0.0.5\tmy-printer.local\n" + sampleBlock + "10.0.0.6\tnas.local\n"
	p := writeHosts(t, custom)
	if err := Remove(p); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	s := string(data)
	if !strings.Contains(s, "my-printer.local") || !strings.Contains(s, "nas.local") {
		t.Errorf("unrelated entries lost:\n%q", s)
	}
	if strings.Contains(s, "sference-switch") {
		t.Errorf("marker text survived:\n%q", s)
	}
}

func TestRemoveMissingEndMarkerFailsLoudly(t *testing.T) {
	p := writeHosts(t, sampleHosts+"\n# sference-switch begin\n127.0.0.1 api.anthropic.com\n")
	if err := Remove(p); err == nil {
		t.Fatal("expected an error for a truncated block")
	}
}
