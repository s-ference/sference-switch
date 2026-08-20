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
// revoke the grant server-side (best-effort, so it disappears from the
// console's devices page), remove the switch's own auth file, and
// live-reload the credential.
//
// Two refusals, both 200 with ok:false so the app can show the reason:
// an env-var credential (SFERENCE_API_KEY) cannot be removed from here,
// and a shared-file credential belongs to the sference CLI — deleting
// it would sign the CLI out too.

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
	if tok == nil {
		writeJSON(w, 200, map[string]any{"ok": true, "state": "signed_out"})
		return
	}
	switch tok.Kind {
	case auth.KindEnv:
		// Messages stay under 80 chars: the menubar renders them via
		// menuErrorLabel, which truncates beyond that.
		writeJSON(w, 200, map[string]any{
			"ok":    false,
			"error": "credential comes from SFERENCE_API_KEY — unset the environment variable",
		})
		return
	case auth.KindSharedDevice, auth.KindSharedStatic:
		writeJSON(w, 200, map[string]any{
			"ok":    false,
			"error": "credential belongs to the sference CLI — run 'sference auth logout'",
		})
		return
	}
	if tok.Kind == auth.KindDevice && tok.RefreshToken != "" {
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
