// Package tlsdoor implements the transparent-interception front door: a
// TLS-terminating listener on 127.0.0.1:443 that presents a locally-trusted
// certificate for api.anthropic.com (minted from the Sference Switch local
// CA) and forwards the decrypted HTTP traffic into the router listener.
//
// This is the /etc/hosts interception model: the harness (Claude Code)
// connects to api.anthropic.com, /etc/hosts resolves it to 127.0.0.1, and
// this listener answers with a cert the system keychain trusts. No harness
// configuration is changed — no ANTHROPIC_BASE_URL, no env vars.
//
// The door is intentionally thin: it terminates TLS and pipes bytes to the
// router. All routing logic (model mapping, fallback, telemetry) lives in
// the router, unchanged.
package tlsdoor

import (
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sference/sference-switch/gateway/internal/dnsbypass"
	"gopkg.in/yaml.v3"
)

// routerPaths are the paths the Sference router owns (inference + model
// discovery). Everything else — OAuth, feature flags, telemetry, MCP
// registry, bootstrap — is Claude Code's control plane and must pass
// through to the real api.anthropic.com, or the session breaks.
var routerPaths = []string{
	"/v1/messages",
	"/v1/messages/count_tokens",
	"/v1/models",
}

// isRouterPath reports whether a request path belongs to the router.
func isRouterPath(path string) bool {
	for _, p := range routerPaths {
		if path == p {
			return true
		}
	}
	return false
}

// isBootstrapPath reports whether a request is Claude Code's bootstrap
// config fetch. The bootstrap response carries the picker's
// `additional_model_options` array; we inject Sference models into it so
// they appear in `/model` without requiring ANTHROPIC_BASE_URL (which
// Claude Code's gateway-model-discovery code path needs, and which our
// transparent-interception model deliberately does not set).
func isBootstrapPath(path string) bool {
	return strings.Contains(path, "/api/claude_cli/bootstrap")
}

// realAnthropicClient is an HTTPS client that dials the real
// api.anthropic.com (resolved via public DNS) for passthrough traffic.
var realAnthropicClient = newRealAnthropicClient()

func newRealAnthropicClient() *http.Client {
	tlsCfg := &tls.Config{ServerName: "api.anthropic.com"}
	if os.Getenv("SFERENCE_SWITCH_INSECURE_TLS") != "" {
		tlsCfg.InsecureSkipVerify = true
	}
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				if host == "api.anthropic.com" {
					ip, err := dnsbypass.ResolveHost(ctx, "api.anthropic.com")
					if err != nil {
						return nil, fmt.Errorf("resolve real api.anthropic.com: %w", err)
					}
					addr = net.JoinHostPort(ip, port)
				}
				d := net.Dialer{Timeout: 10 * time.Second}
				return d.DialContext(ctx, network, addr)
			},
			TLSClientConfig: tlsCfg,
			// Pool aggressively: Claude Code's control plane fires dozens of
			// requests per minute, each on a fresh TLS connection. Without
			// pooling, ephemeral ports exhaust and the router hop wedges.
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 100,
			IdleConnTimeout:     90 * time.Second,
		},
		Timeout: 0,
	}
}

// debugLogging gates the per-connection and per-request tracing. It is off
// by default: the door runs as a launchd daemon whose stderr is an
// unrotated root-owned file, and a request storm turns per-connection
// tracing into tens of thousands of synchronous writes that slow the door
// down exactly when it is already struggling. Handshake and dial *failures*
// are always logged — those are diagnostics, not tracing.
var debugLogging = os.Getenv("SFERENCE_SWITCH_DOOR_DEBUG") != ""

func debugf(format string, args ...any) {
	if debugLogging {
		fmt.Fprintf(os.Stderr, format, args...)
	}
}

// loggingTLSListener wraps a TLS listener and logs handshake failures,
// which http.Server.Serve otherwise swallows silently (a failed handshake
// is just a closed connection to the server).
type loggingTLSListener struct {
	net.Listener
}

func (l *loggingTLSListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[tlsdoor] accept error: %v\n", err)
			return nil, err
		}
		tc, ok := conn.(*tls.Conn)
		if !ok {
			return conn, nil
		}
		if herr := tc.Handshake(); herr != nil {
			// A failed handshake (client probe, wrong SNI, cert distrust) is
			// per-connection noise, not a server error. Log and keep accepting —
			// returning the error would make http.Server.Serve exit.
			fmt.Fprintf(os.Stderr, "[tlsdoor] handshake error from %s: %v\n", tc.RemoteAddr(), herr)
			tc.Close()
			continue
		}
		debugf("[tlsdoor] handshake OK from %s (alpn=%s)\n", tc.RemoteAddr(), tc.ConnectionState().NegotiatedProtocol)
		return conn, nil
	}
}

// Config describes one TLS front door.
type Config struct {
	// ListenAddr is the address to bind, e.g. "127.0.0.1:443".
	ListenAddr string
	// RouterTarget is the plain-HTTP router listener behind this door,
	// e.g. "127.0.0.1:45272".
	RouterTarget string
	// AdminTarget is the admin listener, e.g. "127.0.0.1:45273". The
	// bootstrap injection fetches the model catalog from here — the
	// router listener does not serve admin endpoints.
	AdminTarget string
	// CertFile and KeyFile are the leaf certificate for api.anthropic.com
	// and its private key, minted from the local CA.
	CertFile string
	KeyFile  string
}

// Door is a TLS-terminating front door.
type Door struct {
	cfg      Config
	listener net.Listener
	server   *http.Server
	mu       sync.Mutex
	stopped  bool
}

// New creates a Door. Start binds the listener.
func New(cfg Config) (*Door, error) {
	if cfg.ListenAddr == "" {
		return nil, fmt.Errorf("tlsdoor: ListenAddr is required")
	}
	if cfg.RouterTarget == "" {
		return nil, fmt.Errorf("tlsdoor: RouterTarget is required")
	}
	if cfg.CertFile == "" || cfg.KeyFile == "" {
		return nil, fmt.Errorf("tlsdoor: CertFile and KeyFile are required")
	}
	return &Door{cfg: cfg}, nil
}

// Start binds the TLS listener. The certificate is loaded at bind time so
// a missing or invalid cert fails fast.
func (d *Door) Start() error {
	cert, err := tls.LoadX509KeyPair(d.cfg.CertFile, d.cfg.KeyFile)
	if err != nil {
		return fmt.Errorf("tlsdoor: load certificate: %w", err)
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		// Advertise h2 so Claude Code's HTTP/2 client works; fall back to
		// http/1.1 for older clients.
		NextProtos: []string{"h2", "http/1.1"},
		MinVersion: tls.VersionTLS12,
	}
	ln, err := net.Listen("tcp", d.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("tlsdoor: listen %s: %w", d.cfg.ListenAddr, err)
	}
	d.listener = &loggingTLSListener{Listener: tls.NewListener(ln, tlsCfg)}

	// The handler forwards every request to the router over plain HTTP on
	// loopback. The router sees the original Host header (api.anthropic.com)
	// and processes the request exactly as if it arrived on the plain door.
	// NOTE: the listener is already a tls.Listener (handshake happens in
	// Accept), so the server must NOT have TLSConfig set — that would make it
	// attempt a second TLS handshake on the already-decrypted connection.
	// HTTP/2 is not auto-enabled in this configuration; curl/Claude Code fall
	// back to HTTP/1.1, which is sufficient for the POC.
	d.server = &http.Server{
		Handler:           d,
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
		ConnState: func(conn net.Conn, state http.ConnState) {
			debugf("[tlsdoor] conn %s -> %s\n", conn.RemoteAddr(), state)
		},
	}
	return nil
}

// Serve blocks serving the bound listener until Shutdown.
func (d *Door) Serve() error {
	if d.listener == nil {
		return fmt.Errorf("tlsdoor: Serve before Start")
	}
	err := d.server.Serve(d.listener)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// Shutdown gracefully drains in-flight requests.
func (d *Door) Shutdown(ctxDone <-chan struct{}) error {
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return nil
	}
	d.stopped = true
	d.mu.Unlock()
	if d.server != nil {
		return d.server.Close()
	}
	return nil
}

// routerClient is a dedicated client for the loopback hop to the router.
// It uses its own transport (not http.DefaultClient) so the control-plane
// passthrough flood cannot exhaust the shared pool and wedge the router hop.
var routerClient = &http.Client{
	Transport: &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
	},
	Timeout: 0,
}

// ServeHTTP routes the decrypted request: inference/model paths go to the
// Sference router over plain loopback HTTP; the bootstrap config path is
// fetched from the real Anthropic and post-processed to inject Sference
// models into the picker; everything else passes through to the real
// api.anthropic.com over HTTPS, resolved via public DNS to bypass the
// /etc/hosts entry.
func (d *Door) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if isRouterPath(r.URL.Path) {
		debugf("[tlsdoor] %s %s -> router\n", r.Method, r.URL.Path)
		d.proxyTo(w, r, "http://"+d.cfg.RouterTarget, routerClient)
		return
	}
	if isBootstrapPath(r.URL.Path) && r.Method == http.MethodGet {
		debugf("[tlsdoor] %s %s -> bootstrap inject\n", r.Method, r.URL.Path)
		d.proxyBootstrap(w, r)
		return
	}
	debugf("[tlsdoor] %s %s -> passthrough api.anthropic.com\n", r.Method, r.URL.Path)
	d.proxyTo(w, r, "https://api.anthropic.com", realAnthropicClient)
}

// proxyTo forwards r to base+path using client, streaming the response.
func (d *Door) proxyTo(w http.ResponseWriter, r *http.Request, base string, client *http.Client) {
	target := base + r.URL.RequestURI()
	proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		http.Error(w, "tlsdoor: build proxy request: "+err.Error(), http.StatusBadGateway)
		return
	}
	// Copy headers verbatim — the upstream needs the original Host,
	// Authorization, anthropic-version, etc.
	for k, vs := range r.Header {
		for _, v := range vs {
			proxyReq.Header.Add(k, v)
		}
	}
	proxyReq.Host = r.Host
	resp, err := client.Do(proxyReq)
	if err != nil {
		http.Error(w, "tlsdoor: upstream unreachable: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	// Stream the response body (SSE, etc.) without buffering.
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			return
		}
	}
}

// pickerInjectEnabled reads the picker_inject setting from the gateway
// config file. The door is a separate process from the router, so it reads
// the YAML directly rather than going through the admin API (which would
// add a network round-trip on every bootstrap request). Defaults to true
// when the field is absent.
func pickerInjectEnabled() bool {
	dir := os.Getenv("SFERENCE_SWITCH_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = home + "/.sference/switch"
	}
	path := dir + "/gateway.yaml"
	data, err := os.ReadFile(path)
	if err != nil {
		bootstrapLog.log("pickerInject: cannot read %s: %v — defaulting to true", path, err)
		return true
	}
	var cfg struct {
		Global struct {
			PickerInject *bool `yaml:"picker_inject"`
		} `yaml:"global"`
	}
	if yaml.Unmarshal(data, &cfg) != nil {
		bootstrapLog.log("pickerInject: cannot parse %s — defaulting to true", path)
		return true
	}
	result := cfg.Global.PickerInject == nil || *cfg.Global.PickerInject
	bootstrapLog.log("pickerInject: read %s, result=%t", path, result)
	return result
}

// bootstrapLog writes diagnostic lines to a user-readable file so the
// bootstrap injection path can be debugged without sudo (the daemon's
// stderr is root-owned). One file, appended to, truncated at 1 MB.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var bootstrapLog = newBootstrapLogger()

func newBootstrapLogger() *bootstrapLogger {
	dir := os.Getenv("SFERENCE_SWITCH_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = home + "/.sference/switch"
	}
	return &bootstrapLogger{path: dir + "/logs/tlsdoor-bootstrap.log"}
}

type bootstrapLogger struct {
	path string
}

func (l *bootstrapLogger) log(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("[%s] %s\n", time.Now().Format("15:04:05.000"), msg)
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[tlsdoor-bootstrap] %s", line)
		return
	}
	defer f.Close()
	if fi, _ := f.Stat(); fi != nil && fi.Size() > 1<<20 {
		f.Truncate(0)
		f.Seek(0, 0)
	}
	f.WriteString(line)
}

// proxyBootstrap fetches Claude Code's bootstrap config from the real
// Anthropic and injects Sference models into additional_model_options so
// they appear in the /model picker. This is the same technique the
// mitmproxy-based `sference launch claude` uses, adapted for the TLS door:
// no ANTHROPIC_BASE_URL, no gateway-model-discovery env var — just
// response post-processing on the control-plane path we already intercept.
func (d *Door) proxyBootstrap(w http.ResponseWriter, r *http.Request) {
	// If picker injection is disabled, pass through untouched.
	if !pickerInjectEnabled() {
		bootstrapLog.log("picker_inject is false — passing bootstrap through untouched")
		debugf("[tlsdoor] %s %s -> passthrough (picker_inject off)\n", r.Method, r.URL.Path)
		d.proxyTo(w, r, "https://api.anthropic.com", realAnthropicClient)
		return
	}
	bootstrapLog.log("picker_inject is true — intercepting bootstrap")
	// Fetch the real bootstrap response from Anthropic.
	target := "https://api.anthropic.com" + r.URL.RequestURI()
	proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		bootstrapLog.log("build request error: %v", err)
		http.Error(w, "tlsdoor: build bootstrap request: "+err.Error(), http.StatusBadGateway)
		return
	}
	for k, vs := range r.Header {
		for _, v := range vs {
			proxyReq.Header.Add(k, v)
		}
	}
	proxyReq.Host = r.Host
	// We parse and rewrite this body, so upstream compression buys nothing
	// and costs correctness. Claude Code is a Bun binary and offers
	// "gzip, deflate, br, zstd"; forwarding that verbatim let Anthropic
	// pick brotli, which this door cannot decode — every bootstrap then
	// failed to parse and was forwarded corrupted. Asking for identity
	// removes the whole negotiation rather than chasing each new codec.
	proxyReq.Header.Set("Accept-Encoding", "identity")
	resp, err := realAnthropicClient.Do(proxyReq)
	if err != nil {
		bootstrapLog.log("upstream fetch error: %v", err)
		http.Error(w, "tlsdoor: bootstrap upstream unreachable: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	bootstrapLog.log("upstream returned status %d", resp.StatusCode)
	// Non-200: copy headers and pass through untouched.
	if resp.StatusCode != 200 {
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		bootstrapLog.log("non-200 from upstream — passing through untouched")
		_, _ = io.Copy(w, resp.Body)
		return
	}
	// 200: read, inject, write. The bootstrap body is small JSON (a few KB).
	// Anthropic returns the body gzip-compressed when the client sends
	// Accept-Encoding: gzip (Claude Code does). The passthrough proxyTo
	// streams bytes untouched so gzip is fine there, but here we parse
	// the body, so decompress first.
	//
	// Headers must be written AFTER modification: WriteHeader sends them
	// to the client immediately. If we copy Content-Encoding: gzip and
	// Content-Length from the upstream response and then call WriteHeader
	// before stripping them, the client sees gzip encoding on a
	// decompressed body and tries to gunzip plain JSON — getting garbage
	// and silently discarding the bootstrap.
	var bodyReader io.Reader = resp.Body
	encoding := strings.TrimSpace(resp.Header.Get("Content-Encoding"))
	switch {
	case encoding == "" || strings.EqualFold(encoding, "identity"):
		// Plain body, as requested above.
	case strings.EqualFold(encoding, "gzip"):
		// Defensive: an upstream may ignore Accept-Encoding: identity.
		gzReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			bootstrapLog.log("gzip reader error: %v", err)
			return
		}
		defer gzReader.Close()
		bodyReader = gzReader
	default:
		// An encoding we cannot decode. Stripping Content-Encoding from a
		// body we never decompressed hands the client compressed bytes
		// labelled as plain JSON, so it discards the entire bootstrap —
		// model access, org defaults and costs, not just our picker
		// entries. Forward it verbatim: skipping injection degrades one
		// nicety, corrupting the bootstrap breaks everything.
		bootstrapLog.log("cannot decode Content-Encoding %q — forwarding upstream response untouched", encoding)
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
		return
	}
	body, err := io.ReadAll(io.LimitReader(bodyReader, 1<<20))
	if err != nil {
		bootstrapLog.log("read body error: %v", err)
		return
	}
	bootstrapLog.log("bootstrap body size: %d bytes (decompressed)", len(body))
	injected := injectSferenceModels(body, d.cfg.AdminTarget)
	if len(injected) != len(body) {
		bootstrapLog.log("injected: body changed from %d to %d bytes", len(body), len(injected))
	} else {
		bootstrapLog.log("injected: body unchanged (no models added)")
	}
	// Copy upstream headers, then strip encoding and length so they
	// match the modified (decompressed, injected) body. Must happen
	// BEFORE WriteHeader.
	for k, vs := range resp.Header {
		if strings.EqualFold(k, "Content-Encoding") || strings.EqualFold(k, "Content-Length") {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(injected)
}

// sferenceModelEntry describes one model to inject into the picker. The
// shape matches Claude Code's additional_model_options entries.
type sferenceModelEntry struct {
	Model          string `json:"model"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	DisabledReason any    `json:"disabled_reason"`
}

// injectSferenceModels parses a bootstrap response, appends Sference
// models to additional_model_options (deduped against existing entries),
// and returns the modified JSON. On any parse failure it returns the
// original body unchanged — the picker is a nicety, not a requirement.
func injectSferenceModels(body []byte, adminTarget string) []byte {
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		bootstrapLog.log("inject: body is not valid JSON: %v", err)
		return body
	}
	// Fetch the model list from the router's admin endpoint.
	models := fetchSferenceModels(adminTarget)
	if len(models) == 0 {
		bootstrapLog.log("inject: no models returned from admin endpoint (adminTarget=%s)", adminTarget)
		return body
	}
	bootstrapLog.log("inject: %d models from admin endpoint", len(models))
	options, _ := parsed["additional_model_options"].([]any)
	existing := map[string]bool{}
	for _, o := range options {
		if m, ok := o.(map[string]any); ok {
			if id, ok := m["model"].(string); ok {
				existing[id] = true
			}
		}
	}
	bootstrapLog.log("inject: %d existing options in bootstrap, %d already present", len(options), len(existing))
	for _, m := range models {
		if existing[m.Model] {
			bootstrapLog.log("inject: skipping duplicate %s", m.Model)
			continue
		}
		options = append(options, m)
		existing[m.Model] = true
		bootstrapLog.log("inject: added %s (%s)", m.Model, m.Name)
	}
	parsed["additional_model_options"] = options
	out, err := json.Marshal(parsed)
	if err != nil {
		bootstrapLog.log("inject: marshal error: %v", err)
		return body
	}
	return out
}

// fetchSferenceModels queries the router's admin model-catalog endpoint
// and returns the configured Sference models in the picker entry shape.
// The router already knows the alias→slug mapping, display names, and
// availability — the door doesn't need its own catalog.
func fetchSferenceModels(adminTarget string) []sferenceModelEntry {
	url := "http://" + adminTarget + "/v1/admin/model-catalog"
	bootstrapLog.log("fetch: GET %s", url)
	resp, err := routerClient.Get(url)
	if err != nil {
		bootstrapLog.log("fetch: error: %v", err)
		return nil
	}
	defer resp.Body.Close()
	bootstrapLog.log("fetch: status %d", resp.StatusCode)
	if resp.StatusCode != 200 {
		bootstrapLog.log("fetch: non-200, returning nil")
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		bootstrapLog.log("fetch: read error: %v", err)
		return nil
	}
	var catalog struct {
		State  string `json:"state"`
		Models []struct {
			Slug            string `json:"slug"`
			DisplayName     string `json:"display_name"`
			Alias           string `json:"alias"`
			AliasOneMillion string `json:"alias_1m"`
			Available       bool   `json:"available"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &catalog); err != nil {
		bootstrapLog.log("fetch: parse error: %v", err)
		return nil
	}
	bootstrapLog.log("fetch: state=%s models=%d", catalog.State, len(catalog.Models))
	out := make([]sferenceModelEntry, 0, len(catalog.Models))
	// One picker entry per model. A 1M-context model (alias_1m set)
	// publishes only its [1m] id: Claude Code believes an undecorated id
	// holds 200k tokens and auto-compacts against that, so a bare entry
	// would offer the model at a fifth of its real window. The bare alias
	// stays routable at the router; it is just not listed.
	for _, m := range catalog.Models {
		if !m.Available || m.Alias == "" {
			bootstrapLog.log("fetch: skipping unavailable/no-alias: slug=%s alias=%s available=%v", m.Slug, m.Alias, m.Available)
			continue
		}
		name := m.DisplayName
		if name == "" {
			name = m.Slug
		}
		model := m.Alias
		entryName := "[Sference] " + name
		description := "Sference inference — " + m.Slug
		if m.AliasOneMillion != "" {
			model = m.AliasOneMillion
			entryName += " (1M context)"
			description += " with a 1M-token context window"
			bootstrapLog.log("fetch: publishing 1M entry %s", model)
		}
		out = append(out, sferenceModelEntry{
			Model:          model,
			Name:           entryName,
			Description:    description,
			DisabledReason: nil,
		})
	}
	bootstrapLog.log("fetch: returning %d entries", len(out))
	return out
}
