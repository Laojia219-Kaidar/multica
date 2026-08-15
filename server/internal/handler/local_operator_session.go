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
//
// One same-machine exception: the self-host Next.js frontend. Its rewrite
// layer stamps X-Forwarded-* on every hop, and its forwarding chain always
// originates on the compose network or the host itself. Accept that chain
// only when the first forwarded hop is a compose-network/loopback source AND
// the forwarded Host is loopback — a real reverse proxy fronting a public
// origin fails both checks.
func isDirectLoopbackRequest(r *http.Request) bool {
	if hasForwardingHeader(r) {
		return isSameMachineForwardedChain(r)
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	// Docker self-host: the same-machine Next.js frontend reaches the backend
	// through the compose network, and host-originated requests traverse the
	// bridge gateway (e.g. 172.17.0.1). Both show up as private-range source
	// addresses that no off-machine client can produce on a loopback-bound
	// deployment. Keep this narrower than RFC1918: the compose default pools
	// and the bridge gateway only.
	if isComposeNetworkSource(ip) {
		return true
	}
	return false
}

// isSameMachineForwardedChain accepts X-Forwarded-* chains only when the
// first forwarded hop came from the compose network / host and the forwarded
// Host names a loopback origin. The Next.js same-origin proxy produces
// exactly this shape; anything fronting an external origin does not.
func isSameMachineForwardedChain(r *http.Request) bool {
	forwardedHost := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if forwardedHost == "" {
		return false
	}
	if host, _, err := net.SplitHostPort(forwardedHost); err == nil {
		forwardedHost = host
	}
	if h := net.ParseIP(forwardedHost); h == nil || !h.IsLoopback() {
		if !strings.EqualFold(forwardedHost, "localhost") {
			return false
		}
	}

	firstHop := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if idx := strings.IndexAny(firstHop, ","); idx >= 0 {
		firstHop = strings.TrimSpace(firstHop[:idx])
	}
	ip := net.ParseIP(firstHop)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || isComposeNetworkSource(ip)
}

// isComposeNetworkSource reports whether ip belongs to the Docker compose
// default networks (172.16/12 and 10.x as used by compose's default address
// pools) or the docker bridge gateway. Off-machine traffic can only appear
// here if the operator published the backend port beyond loopback AND opened
// a firewall path — both outside this deployment's contract.
func isComposeNetworkSource(ip net.IP) bool {
	if ip.To4() == nil {
		return false
	}
	for _, cidr := range []string{"172.16.0.0/12", "10.0.0.0/8"} {
		if _, network, err := net.ParseCIDR(cidr); err == nil && network.Contains(ip) {
			return true
		}
	}
	return false
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
