// Package auth implements HTTP Basic authentication against an htpasswd file.
package auth

import (
	"context"
	"net/http"
	"os"
	"sync"

	goauth "github.com/abbot/go-http-auth"
)

// Htpasswd checks Basic Auth credentials against an htpasswd file that is
// re-read on every request, so external edits take effect immediately.
// Supported hashes are the ones the go-http-auth library verifies:
// $apr1$ (MD5-crypt), bcrypt and SHA-crypt.
type Htpasswd struct {
	Path string

	mu sync.Mutex // serializes file reads and the auth library call
}

// NewHtpasswd returns a checker for the htpasswd file at path.
func NewHtpasswd(path string) *Htpasswd { return &Htpasswd{Path: path} }

// secrets re-reads the htpasswd file on each call (the go-http-auth
// HtpasswdFileProvider panics on malformed files, which we must avoid).
func (h *Htpasswd) secrets(user, realm string) string {
	data, err := os.ReadFile(h.Path)
	if err != nil {
		return ""
	}
	entries := parseHtpasswd(string(data))
	hash, ok := entries[user]
	if !ok {
		return ""
	}
	return hash
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
// credentials. Failed attempts get a 401 with a Basic challenge.
func (h *Htpasswd) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		ba := goauth.NewBasicAuthenticator("promui", h.secrets)
		user := ba.CheckAuth(r)
		h.mu.Unlock()
		if user == "" {
			w.Header().Set("WWW-Authenticate", `Basic realm="promui"`)
			http.Error(w, "401 unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, user)))
	})
}

type userKey struct{}

// User returns the authenticated user name of the request, if any.
func User(r *http.Request) string {
	u, _ := r.Context().Value(userKey{}).(string)
	return u
}
