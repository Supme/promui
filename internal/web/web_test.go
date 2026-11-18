package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"promui/internal/filesd"
	"promui/internal/prom"
)

const testConfigYAML = `global:
  scrape_interval: 30s
scrape_configs:
  - job_name: node
    file_sd_configs:
      - files:
          - /files_sd/node_*.yml
  - job_name: blackbox
    file_sd_configs:
      - files:
          - /files_sd/blackbox_*.yml
  - job_name: special
    file_sd_configs:
      - files:
          - /files_sd/special/*.yml
`

func newTestServer(t *testing.T) (*Server, *httptest.Server, string) {
	t.Helper()
	cfgJSON, err := json.Marshal(map[string]any{
		"status": "success",
		"data":   map[string]string{"yaml": testConfigYAML},
	})
	if err != nil {
		t.Fatal(err)
	}
	promSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/status/config":
			_, _ = w.Write(cfgJSON)
		case "/api/v1/targets":
			_, _ = w.Write([]byte(`{"status":"success","data":{"activeTargets":[],"droppedTargets":[]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(promSrv.Close)

	dirPath := t.TempDir()
	s := NewServer(&filesd.Dir{Path: dirPath}, prom.NewClient(promSrv.URL),
		Options{FilesSDDir: dirPath, MountPoint: "/files_sd"}, nil)
	ui := httptest.NewServer(s.Handler())
	t.Cleanup(ui.Close)
	return s, ui, dirPath
}

func doJSON(t *testing.T, method, url string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, url, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	return resp, out
}

func TestAPIConfigMapping(t *testing.T) {
	_, ui, dir := newTestServer(t)
	os.WriteFile(filepath.Join(dir, "node_a.yml"), []byte("[]"), 0o644)
	os.WriteFile(filepath.Join(dir, "node_extra.yml"), []byte("[]"), 0o644)
	os.WriteFile(filepath.Join(dir, "orphan.yml"), []byte("[]"), 0o644)

	resp, out := doJSON(t, "GET", ui.URL+"/api/config", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	jobs := out["jobs"].([]any)
	if len(jobs) != 3 {
		t.Fatalf("want 3 jobs, got %d", len(jobs))
	}
	node := jobs[0].(map[string]any)
	if node["name"] != "node" {
		t.Fatalf("first job = %v", node["name"])
	}
	files := node["files"].([]any)
	if len(files) != 2 {
		t.Fatalf("node should list 2 files, got %v", files)
	}
	others := out["others"].([]any)
	if len(others) != 1 || others[0] != "orphan.yml" {
		t.Fatalf("others = %v", others)
	}
}

func TestCreateFileCovered(t *testing.T) {
	_, ui, dir := newTestServer(t)
	resp, out := doJSON(t, "POST", ui.URL+"/api/files", map[string]string{"job": "node", "id": "a1"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status %d: %v", resp.StatusCode, out)
	}
	if out["file"] != "node_a1.yml" {
		t.Fatalf("file = %v", out["file"])
	}
	if w, _ := out["warning"].(string); w != "" {
		t.Fatalf("covered job must not warn: %v", w)
	}
	if _, err := os.Stat(filepath.Join(dir, "node_a1.yml")); err != nil {
		t.Fatalf("file not created: %v", err)
	}
}

func TestCreateFileCoveredBlackbox(t *testing.T) {
	_, ui, _ := newTestServer(t)
	resp, out := doJSON(t, "POST", ui.URL+"/api/files", map[string]string{"job": "blackbox", "id": "http"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status %d: %v", resp.StatusCode, out)
	}
	if w, _ := out["warning"].(string); w != "" {
		t.Fatalf("blackbox_http.yml is covered by blackbox_*.yml: %q", w)
	}
}

func TestCreateFileJobExistsButPathUncovered(t *testing.T) {
	_, ui, _ := newTestServer(t)
	// job "special" only reads /files_sd/special/*.yml; the new file
	// /files_sd/special_a1.yml is NOT covered -> warning + snippet
	resp, out := doJSON(t, "POST", ui.URL+"/api/files", map[string]string{"job": "special", "id": "a1"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status %d: %v", resp.StatusCode, out)
	}
	warn, _ := out["warning"].(string)
	if !strings.Contains(warn, "special") || !strings.Contains(warn, "globs") {
		t.Fatalf("unexpected warning: %q", warn)
	}
	snippet, _ := out["snippet"].(string)
	if !strings.Contains(snippet, "/files_sd/special_*.yml") {
		t.Fatalf("snippet should suggest the glob, got: %q", snippet)
	}
}

func TestCreateFileUnknownJobWarningAndSnippet(t *testing.T) {
	_, ui, _ := newTestServer(t)
	resp, out := doJSON(t, "POST", ui.URL+"/api/files", map[string]string{"job": "fluentd", "id": "log1"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status %d: %v", resp.StatusCode, out)
	}
	warn, _ := out["warning"].(string)
	if !strings.Contains(warn, "fluentd") || !strings.Contains(warn, "not present") {
		t.Fatalf("unexpected warning: %q", warn)
	}
	snippet, _ := out["snippet"].(string)
	if !strings.Contains(snippet, "job_name: fluentd") || !strings.Contains(snippet, "/files_sd/fluentd_*.yml") {
		t.Fatalf("unexpected snippet: %q", snippet)
	}
}

func TestCreateFileInvalid(t *testing.T) {
	_, ui, _ := newTestServer(t)
	resp, _ := doJSON(t, "POST", ui.URL+"/api/files", map[string]string{"job": "", "id": "x"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty job: status %d", resp.StatusCode)
	}
	resp, _ = doJSON(t, "POST", ui.URL+"/api/files", map[string]string{"job": "node", "id": "a/b"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad id: status %d", resp.StatusCode)
	}
}

func TestPutFileValidationAndBackup(t *testing.T) {
	_, ui, dir := newTestServer(t)
	doJSON(t, "POST", ui.URL+"/api/files", map[string]string{"job": "node", "id": "a1"})

	// invalid: bad scheme and timeout > interval
	resp, out := doJSON(t, "PUT", ui.URL+"/api/files/node_a1.yml", map[string]any{
		"groups": []map[string]any{{
			"targets": []string{"h:1"},
			"labels":  map[string]string{"__scheme__": "ftp", "__scrape_interval__": "10s", "__scrape_timeout__": "1m"},
		}},
	})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d: %v", resp.StatusCode, out)
	}

	// valid save creates a backup
	resp, out = doJSON(t, "PUT", ui.URL+"/api/files/node_a1.yml", map[string]any{
		"groups": []map[string]any{{
			"targets": []string{"h1:9100", "h2:9100"},
			"labels":  map[string]string{"env": "prod"},
		}},
	})
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %v", resp.StatusCode, out)
	}
	backup, _ := out["backup"].(string)
	if backup == "" || !strings.HasPrefix(backup, "node_a1.yml.bkp_") {
		t.Fatalf("backup not reported: %q", backup)
	}
	if _, err := os.Stat(filepath.Join(dir, backup)); err != nil {
		t.Fatalf("backup missing on disk: %v", err)
	}

	// saved content is the new groups
	resp, out = doJSON(t, "GET", ui.URL+"/api/files/node_a1.yml", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("get: %d", resp.StatusCode)
	}
	groups := out["groups"].([]any)
	if len(groups) != 1 {
		t.Fatalf("groups = %v", groups)
	}
	g := groups[0].(map[string]any)
	if g["labels"].(map[string]any)["env"] != "prod" {
		t.Fatalf("labels = %v", g["labels"])
	}
}

func TestDeleteFileWithBackup(t *testing.T) {
	_, ui, dir := newTestServer(t)
	doJSON(t, "POST", ui.URL+"/api/files", map[string]string{"job": "node", "id": "a1"})
	resp, out := doJSON(t, "DELETE", ui.URL+"/api/files/node_a1.yml", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %v", resp.StatusCode, out)
	}
	backup, _ := out["backup"].(string)
	if backup == "" {
		t.Fatal("delete must report backup")
	}
	if _, err := os.Stat(filepath.Join(dir, "node_a1.yml")); !os.IsNotExist(err) {
		t.Fatal("file still exists after delete")
	}
	if _, err := os.Stat(filepath.Join(dir, backup)); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
}

func TestPathTraversalRejected(t *testing.T) {
	_, ui, _ := newTestServer(t)
	for _, name := range []string{"..%2F..%2Fetc%2Fpasswd", "x.yml.bkp_20260101_000000_000000001"} {
		resp, _ := doJSON(t, "GET", ui.URL+"/api/files/"+name, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("GET %s: status %d, want 400", name, resp.StatusCode)
		}
	}
	resp, _ := doJSON(t, "PUT", ui.URL+"/api/files/..%2Fsecret.yml", map[string]any{"groups": []any{}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("PUT traversal: status %d", resp.StatusCode)
	}
}

func TestPagesRender(t *testing.T) {
	_, ui, _ := newTestServer(t)
	for path, marker := range map[string]string{
		"/": "editor-modal", // SPA: the editor is a modal on the index page
	} {
		resp, err := http.Get(ui.URL + path)
		if err != nil || resp.StatusCode != 200 {
			t.Errorf("GET %s: %v %d", path, err, resp.StatusCode)
			continue
		}
		buf := new(bytes.Buffer)
		buf.ReadFrom(resp.Body)
		resp.Body.Close()
		if !strings.Contains(buf.String(), marker) {
			t.Errorf("GET %s: marker %q not found", path, marker)
		}
	}
	// The separate /edit/ page is gone; it must not be served anymore.
	resp, err := http.Get(ui.URL + "/edit/node_a.yml")
	if err != nil {
		t.Fatalf("GET /edit/: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /edit/node_a.yml: status %d, want 404", resp.StatusCode)
	}
}

func TestTargetsEndpoint(t *testing.T) {
	_, ui, _ := newTestServer(t)
	resp, out := doJSON(t, "GET", ui.URL+"/api/targets", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if _, has := out["targets"]; !has {
		t.Fatalf("payload = %v", out)
	}
}
