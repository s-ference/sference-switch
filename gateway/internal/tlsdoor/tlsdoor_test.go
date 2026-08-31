package tlsdoor

import (
	"compress/zlib"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func TestFetchSferenceModels(t *testing.T) {
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/admin/model-catalog" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"state":"ready","models":[
			{"slug":"zai-org/GLM-5.2","display_name":"GLM 5.2","alias":"claude-sference-glm-5-2","available":true},
			{"slug":"moonshotai/Kimi-K3","display_name":"Kimi K3","alias":"claude-sference-kimi-k3","available":true},
			{"slug":"unavailable/model","display_name":"Unavailable","alias":"claude-sference-unavail","available":false}
		]}`)
	}))
	defer admin.Close()

	adminAddr := strings.TrimPrefix(admin.URL, "http://")
	models := fetchSferenceModels(adminAddr)
	if len(models) != 2 {
		t.Fatalf("got %d models, want 2 (unavailable filtered out)", len(models))
	}
	if models[0].Model != "claude-sference-glm-5-2" {
		t.Errorf("first model = %q", models[0].Model)
	}
	if models[0].Name != "[Sference] GLM 5.2" {
		t.Errorf("first name = %q", models[0].Name)
	}
}

// A 1M-context model publishes ONLY its [1m] entry: Claude Code believes an
// undecorated id holds 200k tokens, so listing both would offer the model at
// a fifth of its real window right next to the full one. Sub-1M models keep
// their bare alias.
func TestFetchSferenceModelsPrefersOneMillionEntry(t *testing.T) {
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"state":"ready","models":[
			{"slug":"zai-org/GLM-5.3","display_name":"GLM 5.3","alias":"claude-sference-glm-5-3","alias_1m":"claude-sference-glm-5-3[1m]","available":true},
			{"slug":"bottlecapai/ThinkingCap","display_name":"ThinkingCap","alias":"claude-sference-thinkingcap","available":true}
		]}`)
	}))
	defer admin.Close()

	models := fetchSferenceModels(strings.TrimPrefix(admin.URL, "http://"))
	if len(models) != 2 {
		t.Fatalf("got %d entries, want 2 (one per model): %+v", len(models), models)
	}
	byID := map[string]sferenceModelEntry{}
	for _, m := range models {
		if _, duplicate := byID[m.Model]; duplicate {
			t.Errorf("model %q published twice", m.Model)
		}
		byID[m.Model] = m
	}
	glm, ok := byID["claude-sference-glm-5-3[1m]"]
	if !ok {
		t.Fatalf("[1m] entry missing, bare %q must not be listed instead: %+v",
			"claude-sference-glm-5-3", byID)
	}
	if _, bareListed := byID["claude-sference-glm-5-3"]; bareListed {
		t.Error("bare alias of a 1M model is listed alongside its [1m] entry")
	}
	if !strings.Contains(glm.Name, "1M") {
		t.Errorf("1M entry name = %q, want the 1M variant marked", glm.Name)
	}
	if _, ok := byID["claude-sference-thinkingcap"]; !ok {
		t.Errorf("sub-1M bare alias missing: %+v", byID)
	}
}

func TestFetchSferenceModelsUnreachable(t *testing.T) {
	models := fetchSferenceModels("127.0.0.1:1")
	if models != nil {
		t.Fatalf("got %d models from unreachable admin, want nil", len(models))
	}
}

func TestInjectSferenceModels(t *testing.T) {
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"state":"ready","models":[
			{"slug":"zai-org/GLM-5.2","display_name":"GLM 5.2","alias":"claude-sference-glm-5-2","available":true}
		]}`)
	}))
	defer admin.Close()
	adminAddr := strings.TrimPrefix(admin.URL, "http://")

	body := []byte(`{"additional_model_options":[],"other_field":"preserved"}`)
	injected := injectSferenceModels(body, adminAddr)

	var parsed map[string]interface{}
	if err := json.Unmarshal(injected, &parsed); err != nil {
		t.Fatalf("injected body is not valid JSON: %v", err)
	}
	opts, ok := parsed["additional_model_options"].([]interface{})
	if !ok {
		t.Fatal("additional_model_options missing or not array")
	}
	if len(opts) != 1 {
		t.Fatalf("got %d options, want 1", len(opts))
	}
	entry := opts[0].(map[string]interface{})
	if entry["model"] != "claude-sference-glm-5-2" {
		t.Errorf("model = %q", entry["model"])
	}
	if entry["name"] != "[Sference] GLM 5.2" {
		t.Errorf("name = %q", entry["name"])
	}
	if parsed["other_field"] != "preserved" {
		t.Errorf("other_field = %v, want preserved", parsed["other_field"])
	}
}

func TestInjectSferenceModelsDedupes(t *testing.T) {
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"state":"ready","models":[
			{"slug":"zai-org/GLM-5.2","display_name":"GLM 5.2","alias":"claude-sference-glm-5-2","available":true}
		]}`)
	}))
	defer admin.Close()
	adminAddr := strings.TrimPrefix(admin.URL, "http://")

	body := []byte(`{"additional_model_options":[{"model":"claude-sference-glm-5-2","name":"existing"}]}`)
	injected := injectSferenceModels(body, adminAddr)

	var parsed map[string]interface{}
	json.Unmarshal(injected, &parsed)
	opts := parsed["additional_model_options"].([]interface{})
	if len(opts) != 1 {
		t.Fatalf("got %d options, want 1 (deduped)", len(opts))
	}
}

func TestInjectSferenceModelsEmptyCatalog(t *testing.T) {
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"state":"signed_out","models":[]}`)
	}))
	defer admin.Close()
	adminAddr := strings.TrimPrefix(admin.URL, "http://")

	body := []byte(`{"additional_model_options":[]}`)
	injected := injectSferenceModels(body, adminAddr)
	if string(injected) != string(body) {
		t.Errorf("body modified when catalog is empty")
	}
}

func TestInjectSferenceModelsInvalidJSON(t *testing.T) {
	body := []byte(`not json`)
	injected := injectSferenceModels(body, "127.0.0.1:1")
	if string(injected) != string(body) {
		t.Errorf("body modified on invalid JSON")
	}
}

// --- proxyBootstrap: upstream content negotiation ---------------------
//
// Regression cover for the v0.1.1 bug where every bootstrap arrived
// brotli-encoded, failed to parse, and was forwarded with its
// Content-Encoding stripped — corrupting the whole bootstrap, not just
// the picker. Only the gzip branch had ever been exercised.

// bootstrapTestDoor wires a Door to a fake admin catalog and points
// realAnthropicClient at upstream. It restores the globals on cleanup.
func bootstrapTestDoor(t *testing.T, upstream *httptest.Server) *Door {
	t.Helper()

	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"state":"ready","models":[
			{"slug":"zai-org/GLM-5.2","display_name":"GLM 5.2","alias":"claude-sference-glm-5-2","available":true}
		]}`)
	}))
	t.Cleanup(admin.Close)

	// bootstrapLog is built from the environment at package-init time, so
	// redirect the var itself to keep the user's live log clean.
	prevLog := bootstrapLog
	bootstrapLog = &bootstrapLogger{path: filepath.Join(t.TempDir(), "bootstrap.log")}
	t.Cleanup(func() { bootstrapLog = prevLog })

	// picker_inject defaults to true when the config file is absent.
	t.Setenv("SFERENCE_SWITCH_CONFIG_DIR", t.TempDir())

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
	// The door always dials https://api.anthropic.com, so the fake upstream
	// must terminate TLS too; its self-signed cert is why verification is
	// skipped here.
	prevClient := realAnthropicClient
	realAnthropicClient = &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, upstreamURL.Host)
			},
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	t.Cleanup(func() { realAnthropicClient = prevClient })

	return &Door{cfg: Config{AdminTarget: strings.TrimPrefix(admin.URL, "http://")}}
}

func bootstrapRequest() *http.Request {
	r := httptest.NewRequest("GET", "https://api.anthropic.com/api/claude_cli/bootstrap", nil)
	// What Claude Code (a Bun binary) actually offers.
	r.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	return r
}

// The door parses and rewrites the body, so compression upstream buys
// nothing and costs correctness. It must ask for identity regardless of
// what the client offered.
func TestProxyBootstrapRequestsIdentityEncoding(t *testing.T) {
	var got string
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"additional_model_options":[]}`)
	}))
	defer upstream.Close()

	d := bootstrapTestDoor(t, upstream)
	d.proxyBootstrap(httptest.NewRecorder(), bootstrapRequest())

	if got != "identity" {
		t.Errorf("upstream Accept-Encoding = %q, want %q", got, "identity")
	}
}

// A server that honours Accept-Encoding the way a real CDN does: plain
// when identity is requested, compressed otherwise. Before the fix the
// door forwarded the client's "gzip, deflate, br, zstd", got a
// compressed body back, and failed to parse it.
func TestProxyBootstrapInjectsWhenUpstreamWouldCompress(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := `{"additional_model_options":[]}`
		if strings.Contains(r.Header.Get("Accept-Encoding"), "deflate") {
			w.Header().Set("Content-Encoding", "deflate")
			zw := zlib.NewWriter(w)
			defer zw.Close()
			fmt.Fprint(zw, body)
			return
		}
		fmt.Fprint(w, body)
	}))
	defer upstream.Close()

	d := bootstrapTestDoor(t, upstream)
	rec := httptest.NewRecorder()
	d.proxyBootstrap(rec, bootstrapRequest())

	var parsed struct {
		Options []struct {
			Model string `json:"model"`
		} `json:"additional_model_options"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("client received a body it cannot parse as JSON: %v", err)
	}
	if len(parsed.Options) != 1 || parsed.Options[0].Model != "claude-sference-glm-5-2" {
		t.Errorf("additional_model_options = %+v, want the injected Sference model", parsed.Options)
	}
}

// Defence in depth: if an upstream ignores identity and compresses
// anyway, the door must not strip Content-Encoding off a body it never
// decompressed. Emitting compressed bytes labelled as plain destroys the
// entire bootstrap; passing it through intact merely skips injection.
func TestProxyBootstrapPassesThroughUndecodableEncoding(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "br")
		w.Write([]byte{0x83, 0x01, 0x02, 0x03})
	}))
	defer upstream.Close()

	d := bootstrapTestDoor(t, upstream)
	rec := httptest.NewRecorder()
	d.proxyBootstrap(rec, bootstrapRequest())

	if enc := rec.Header().Get("Content-Encoding"); enc != "br" {
		t.Errorf("Content-Encoding = %q, want %q preserved alongside the undecoded body", enc, "br")
	}
	if body := rec.Body.Bytes(); len(body) != 4 || body[0] != 0x83 {
		t.Errorf("body = %v, want the upstream bytes forwarded unchanged", body)
	}
}
