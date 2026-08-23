package gateway

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/sference/sference-switch/gateway/internal/auth"
)

// auth_logout_admin.go implements POST /v1/admin/auth/logout — the in-UI
// sign-out, with the same semantics as `sference-switch auth logout`:
// revoke the grant server-side when it is the switch's own (best-effort,
// so it disappears from the console's devices page), remove the own auth
// file, and live-reload the credential.
//
// auth.Delete also stamps the sign-out marker that suppresses the shared
// CLI fallback — without it the reload would immediately re-sign the
// switch in from ~/.sference/credentials.json and sign-out would look
// like a no-op. A shared-file credential is never revoked or deleted (it
// belongs to the sference CLI); the marker alone signs the switch out of
// it. The one refusal is an env-var credential (SFERENCE_API_KEY), which
// no file operation can remove.

func (g *Gateway) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		g.reject(w, 405, "method not allowed")
		return
	}
	tok, _, err := auth.Load("")
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if tok != nil && tok.Kind == auth.KindEnv {
		// Message stays under 80 chars: the menubar renders it via
		// menuErrorLabel, which truncates beyond that.
		writeJSON(w, 200, map[string]any{
			"ok":    false,
			"error": "credential comes from SFERENCE_API_KEY — unset the environment variable",
		})
		return
	}
	if tok != nil && tok.Kind == auth.KindDevice && tok.RefreshToken != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if err := auth.RevokeToken(ctx, auth.DefaultHostFunc(), tok.RefreshToken); err != nil {
			fmt.Fprintf(os.Stderr, "[gateway] auth: logout revoke failed (continuing): %v\n", err)
		}
	}
	if err := auth.Delete(nil); err != nil {
		writeJSON(w, 200, map[string]any{
			"ok":    false,
			"error": fmt.Sprintf("removing credential: %v", err),
		})
		return
	}
	fmt.Fprintf(os.Stderr, "[gateway] auth: signed out via admin endpoint\n")
	g.refreshAuth()
	writeJSON(w, 200, map[string]any{"ok": true, "state": "signed_out"})
}
