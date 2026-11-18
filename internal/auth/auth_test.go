package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// real hashes of the password "secret"
const bcryptLine = "admin:$2a$05$4juTqUbGwuaNkGhTeqLPqe9oRzmSyNId.WwlML1J2Rp9y7kcVq0rC"
const apr1Line = "apr1user:$apr1$somesalt$..ZeRvGlKotUj3Cak5iHh."

func newAuthServer(t *testing.T, hashLine string) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	ht := filepath.Join(dir, "htpasswd")
	if err := os.WriteFile(ht, []byte(hashLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewHtpasswd(ht)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(h.Middleware(mux))
	t.Cleanup(srv.Close)
	return srv, ht
}

func TestBasicAuthBcrypt(t *testing.T) {
	srv, _ := newAuthServer(t, bcryptLine)

	// no credentials: 401 with challenge
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no creds: %d, want 401", resp.StatusCode)
	}
	if resp.Header.Get("WWW-Authenticate") != `Basic realm="promui"` {
		t.Fatalf("challenge = %q", resp.Header.Get("WWW-Authenticate"))
	}

	// wrong password
	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.SetBasicAuth("admin", "wrong")
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", resp.StatusCode)
	}

	// correct password
	req, _ = http.NewRequest("GET", srv.URL, nil)
	req.SetBasicAuth("admin", "secret")
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("correct password: %d", resp.StatusCode)
	}
}

func TestBasicAuthApr1(t *testing.T) {
	srv, _ := newAuthServer(t, apr1Line)
	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.SetBasicAuth("apr1user", "secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("apr1 login: %d, want 200", resp.StatusCode)
	}
}

func TestHtpasswdReload(t *testing.T) {
	srv, ht := newAuthServer(t, bcryptLine)

	// user missing at first
	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.SetBasicAuth("bob", "pw")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bob before edit: %d", resp.StatusCode)
	}

	// add bob externally; next request must accept him
	if err := os.WriteFile(ht, []byte(bcryptLine+"\nbob:$2a$05$4juTqUbGwuaNkGhTeqLPqe9oRzmSyNId.WwlML1J2Rp9y7kcVq0rC\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	req, _ = http.NewRequest("GET", srv.URL, nil)
	req.SetBasicAuth("bob", "secret")
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bob after edit: %d, want 200 (file must be re-read)", resp.StatusCode)
	}
}

func TestMalformedHtpasswdNoPanic(t *testing.T) {
	// a broken htpasswd file must yield 401, not crash the server
	srv, _ := newAuthServer(t, "this is not valid:at all")
	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.SetBasicAuth("admin", "secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("malformed file: %d, want 401", resp.StatusCode)
	}
}
