package handler

import (
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/logger"
)

// localOperatorSessionEnvironment is intentionally an allowlist. Empty,
// staging, unknown, prod, and production environments all fail closed.
func localOperatorSessionEnvironment() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("APP_ENV"))) {
	case "local", "development", "test":
		return true
	default:
		return false
	}
}

func hasForwardingHeader(r *http.Request) bool {
	for _, name := range []string{
		"X-Forwarded-For",
		"X-Forwarded-Host",
		"X-Forwarded-Proto",
		"X-Real-Ip",
		"Forwarded",
	} {
		if strings.TrimSpace(r.Header.Get(name)) != "" {
			return true
		}
	}
	return false
}

// isDirectLoopbackRequest refuses every proxied request. A backend behind a
// reverse proxy sees the proxy as loopback, so trusting forwarding headers
// would turn this candidate-only entry into a remote session issuer.
func isDirectLoopbackRequest(r *http.Request) bool {
	if hasForwardingHeader(r) {
		return false
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isLoopbackOrigin(r *http.Request) bool {
	raw := strings.TrimSpace(r.Header.Get("Origin"))
	if raw == "" || raw == "null" {
		return false
	}
	origin, err := url.Parse(raw)
	if err != nil || origin.Scheme != "http" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return false
	}
	host := origin.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (h *Handler) localOperatorSessionAvailable(r *http.Request) bool {
	return h != nil && h.cfg.LocalOperatorSessionEnabled && h.cfg.LocalOperatorUserID.Valid &&
		localOperatorSessionEnvironment() && isDirectLoopbackRequest(r)
}

// StartLocalOperatorSession creates an ordinary cookie session for one
// prevalidated, existing local operator. It accepts no user input and returns
// no JWT or identity material; consumers recover the session through /api/me.
func (h *Handler) StartLocalOperatorSession(w http.ResponseWriter, r *http.Request) {
	if !h.localOperatorSessionAvailable(r) || !isLoopbackOrigin(r) {
		http.NotFound(w, r)
		return
	}

	user, err := h.Queries.GetUser(r.Context(), h.cfg.LocalOperatorUserID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	token, err := h.issueJWT(user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create session")
		return
	}
	if err := auth.SetAuthCookies(w, token); err != nil {
		auth.ClearAuthCookies(w)
		writeError(w, http.StatusInternalServerError, "failed to create session")
		return
	}
	if h.CFSigner != nil {
		for _, cookie := range h.CFSigner.SignedCookies(time.Now().Add(auth.AuthTokenTTL())) {
			http.SetCookie(w, cookie)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	slog.Info("local operator session created", append(logger.RequestAttrs(r), "user_id", uuidToString(user.ID))...)
	w.WriteHeader(http.StatusNoContent)
}
