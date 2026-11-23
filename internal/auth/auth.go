// Package auth implements HTTP Basic authentication against an htpasswd file.
package auth

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	goauth "github.com/abbot/go-http-auth"
)

// Htpasswd checks Basic Auth credentials against an htpasswd file that is
// re-read on every request, so external edits take effect immediately.
// Supported hashes are the ones the go-http-auth library verifies:
// $apr1$ (MD5-crypt), bcrypt and SHA-crypt.
type Htpasswd struct {
	Path string
	// ProxyHeader is the header carrying the client address when promui
	// runs behind a trusted proxy that normalizes it (e.g.
	// X-Forwarded-For); its leftmost entry keys the lockout. Empty: the
	// direct connection address is used.
	ProxyHeader string

	mu   sync.Mutex // serializes file reads and the auth library call
	loMu sync.Mutex // protects lockouts
	// lockouts tracks failed logins per client address
	lockouts map[string]*lockState
}

// brute-force lockout thresholds
const (
	lockoutThreshold  = 5 // failed logins within the window
	lockoutWindow     = 10 * time.Minute
	lockoutDuration   = 15 * time.Minute
	lockoutMaxClients = 4096 // prune the map above this size
)

type lockState struct {
	fails       int
	windowStart time.Time
	lockedUntil time.Time
}

// NewHtpasswd returns a checker for the htpasswd file at path.
func NewHtpasswd(path, proxyHeader string) *Htpasswd {
	return &Htpasswd{Path: path, ProxyHeader: proxyHeader, lockouts: make(map[string]*lockState)}
}

// check verifies the request's Basic Auth credentials against the
// htpasswd file, read once per call.
func (h *Htpasswd) check(r *http.Request) string {
	data, err := os.ReadFile(h.Path)
	if err != nil {
		return ""
	}
	entries := parseHtpasswd(string(data))
	// an unknown user is verified against a random hash from the file,
	// so the response time does not reveal which users exist
	secret := func(user, realm string) string {
		if hash, ok := entries[user]; ok {
			return hash
		}
		return randomHash(entries)
	}
	user := goauth.NewBasicAuthenticator("promui", secret).CheckAuth(r)
	// a match against a random hash must not log the unknown user in
	if _, ok := entries[user]; !ok {
		return ""
	}
	return user
}

// randomHash picks a random hash from entries for the dummy
// verification; the choice is for timing only. Only hashes the library
// recognizes are considered, so the dummy verify stays on a known path.
func randomHash(entries map[string]string) string {
	hashes := make([]string, 0, len(entries))
	for _, hash := range entries {
		if knownHash(hash) {
			hashes = append(hashes, hash)
		}
	}
	if len(hashes) == 0 {
		return ""
	}
	return hashes[rand.IntN(len(hashes))]
}

// knownHash reports whether the library can verify the hash.
func knownHash(hash string) bool {
	return strings.HasPrefix(hash, "$apr1$") ||
		strings.HasPrefix(hash, "$2a$") ||
		strings.HasPrefix(hash, "$2b$") ||
		strings.HasPrefix(hash, "$2y$") ||
		strings.HasPrefix(hash, "{SHA}") ||
		strings.HasPrefix(hash, "{MD5}")
}

// parseHtpasswd parses user:hash lines, ignoring comments and blanks.
func parseHtpasswd(data string) map[string]string {
	entries := make(map[string]string)
	for _, line := range splitLines(data) {
		if line == "" || line[0] == '#' {
			continue
		}
		for i := 0; i < len(line); i++ {
			if line[i] == ':' {
				entries[line[:i]] = line[i+1:]
				break
			}
		}
	}
	return entries
}

func splitLines(data string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			end := i
			if end > start && data[end-1] == '\r' {
				end--
			}
			lines = append(lines, data[start:end])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}

// Middleware wraps next so every request requires valid Basic Auth
// credentials. Failed attempts get a 401 with a Basic challenge; a
// client is locked out after repeated failed logins.
func (h *Htpasswd) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r, h.ProxyHeader)
		now := time.Now()
		if h.isLocked(ip, now) {
			unauthorized(w)
			return
		}
		h.mu.Lock()
		user := h.check(r)
		h.mu.Unlock()
		if user == "" {
			// only count attempts that presented credentials; a page
			// load without them is not a login attempt
			if _, _, ok := r.BasicAuth(); ok {
				h.recordFailure(ip, now)
			}
			unauthorized(w)
			return
		}
		h.recordSuccess(ip)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, user)))
	})
}

// clientIP returns the client address used for lockouts: the leftmost
// entry of ProxyHeader when it is set and non-empty, otherwise the host
// part of RemoteAddr.
func clientIP(r *http.Request, header string) string {
	if header != "" {
		v := r.Header.Get(header)
		if i := strings.IndexByte(v, ','); i >= 0 {
			v = v[:i]
		}
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// isLocked reports whether ip is currently locked out.
func (h *Htpasswd) isLocked(ip string, now time.Time) bool {
	h.loMu.Lock()
	defer h.loMu.Unlock()
	st := h.lockouts[ip]
	return st != nil && now.Before(st.lockedUntil)
}

// recordFailure counts a failed login for ip and locks it after
// lockoutThreshold failures within lockoutWindow.
func (h *Htpasswd) recordFailure(ip string, now time.Time) {
	h.loMu.Lock()
	defer h.loMu.Unlock()
	if len(h.lockouts) > lockoutMaxClients {
		for k, st := range h.lockouts {
			if !now.Before(st.lockedUntil) && now.Sub(st.windowStart) >= lockoutWindow {
				delete(h.lockouts, k)
			}
		}
	}
	st := h.lockouts[ip]
	if st == nil {
		st = &lockState{windowStart: now}
		h.lockouts[ip] = st
	}
	if now.Sub(st.windowStart) >= lockoutWindow {
		st.windowStart = now
		st.fails = 0
	}
	st.fails++
	if st.fails >= lockoutThreshold {
		st.fails = 0
		st.lockedUntil = now.Add(lockoutDuration)
		slog.Info("client locked out after repeated login failures", "client", ip)
	}
}

// recordSuccess clears the failure count for ip.
func (h *Htpasswd) recordSuccess(ip string) {
	h.loMu.Lock()
	defer h.loMu.Unlock()
	delete(h.lockouts, ip)
}

// unauthorized writes the 401 Basic challenge.
func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="promui"`)
	http.Error(w, "401 unauthorized", http.StatusUnauthorized)
}

type userKey struct{}

// User returns the authenticated user name of the request, if any.
func User(r *http.Request) string {
	u, _ := r.Context().Value(userKey{}).(string)
	return u
}
