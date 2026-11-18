package prom

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

const mockConfigYAML = `global:
  scrape_interval: 30s
  scrape_timeout: 10s
scrape_configs:
  - job_name: node
    scrape_interval: 15s
    file_sd_configs:
      - files:
          - /files_sd/node_*.yml
        refresh_interval: 1m
      - files:
          - /files_sd/extra/*.yml
        refresh_interval: 30s
  - job_name: blackbox
    metrics_path: /probe
    scheme: http
    file_sd_configs:
      - files:
          - /files_sd/blackbox_*.yml
        refresh_interval: 2m
`

func newMockProm(t *testing.T) *httptest.Server {
	t.Helper()
	cfgJSON, err := json.Marshal(map[string]any{
		"status": "success",
		"data":   map[string]string{"yaml": mockConfigYAML},
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/status/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(cfgJSON)
	})
	mux.HandleFunc("/api/v1/targets", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"activeTargets":[` +
			`{"scrapePool":"node","scrapeUrl":"http://node1:9100/metrics","health":"up","lastError":"","lastScrape":"2026-11-17T12:00:00Z","labels":{"job":"node","instance":"node1:9100"},"discoveredLabels":{}}` +
			`],"droppedTargets":[{"discoveredLabels":{"__address__":"gone:9100"}}]}}`))
	})
	return httptest.NewServer(mux)
}

func fetchMockConfig(t *testing.T) *Config {
	t.Helper()
	srv := newMockProm(t)
	defer srv.Close()
	cfg, err := NewClient(srv.URL).FetchConfig(context.Background())
	if err != nil {
		t.Fatalf("FetchConfig: %v", err)
	}
	return cfg
}

func TestFetchConfig(t *testing.T) {
	cfg := fetchMockConfig(t)
	if len(cfg.ScrapeConfigs) != 2 {
		t.Fatalf("want 2 scrape configs, got %d", len(cfg.ScrapeConfigs))
	}
	node := cfg.ScrapeConfigs[0]
	if node.JobName != "node" || len(node.FileSDConfigs) != 2 {
		t.Fatalf("unexpected node job: %+v", node)
	}
	if got := node.FileSDConfigs[0].Files[0]; got != "/files_sd/node_*.yml" {
		t.Fatalf("unexpected glob: %q", got)
	}
	if node.FileSDConfigs[0].RefreshInterval != "1m" {
		t.Fatalf("unexpected refresh_interval: %q", node.FileSDConfigs[0].RefreshInterval)
	}
	if node.ScrapeInterval != "15s" {
		t.Fatalf("unexpected scrape_interval: %q", node.ScrapeInterval)
	}
	bb := cfg.ScrapeConfigs[1]
	if bb.MetricsPath != "/probe" || bb.Scheme != "http" {
		t.Fatalf("unexpected blackbox job: %+v", bb)
	}
}

func TestFetchTargets(t *testing.T) {
	srv := newMockProm(t)
	defer srv.Close()

	ts, err := NewClient(srv.URL).FetchTargets(context.Background())
	if err != nil {
		t.Fatalf("FetchTargets: %v", err)
	}
	if len(ts.Active) != 1 || ts.Active[0].ScrapeURL != "http://node1:9100/metrics" {
		t.Fatalf("unexpected active targets: %+v", ts.Active)
	}
	if len(ts.Dropped) != 1 || ts.Dropped[0]["__address__"] != "gone:9100" {
		t.Fatalf("unexpected dropped targets: %+v", ts.Dropped)
	}
}

func TestFetchConfigAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	if _, err := NewClient(srv.URL).FetchConfig(context.Background()); err == nil {
		t.Fatal("want error on HTTP 500")
	}
}

func TestMatchFiles(t *testing.T) {
	cfg := fetchMockConfig(t)
	files := []string{
		"node_a.yml",        // node
		"node_b.yaml",       // no match: .yaml
		"blackbox_http.yml", // blackbox
		"unrelated.yml",     // none
	}
	got := cfg.MatchFiles(files, "/files_sd")
	cases := map[string]int{
		"node_a.yml":        1,
		"node_b.yaml":       0,
		"blackbox_http.yml": 1,
		"unrelated.yml":     0,
	}
	for f, want := range cases {
		if len(got[f]) != want {
			t.Errorf("file %s: want %d jobs, got %v", f, want, got[f])
		}
	}
	if got["node_a.yml"][0] != "node" {
		t.Errorf("node_a.yml should map to node, got %v", got["node_a.yml"])
	}
}

func TestMatchFilesSharedAcrossJobs(t *testing.T) {
	// one file matched by globs of two different jobs
	cfg := &Config{ScrapeConfigs: []ScrapeConfig{
		{JobName: "a", FileSDConfigs: []FileSDConfig{{Files: []string{"/files_sd/x_*.yml"}}}},
		{JobName: "b", FileSDConfigs: []FileSDConfig{{Files: []string{"/files_sd/*_probe.yml"}}}},
	}}
	got := cfg.MatchFiles([]string{"x_probe.yml"}, "/files_sd")
	if len(got["x_probe.yml"]) != 2 {
		t.Fatalf("x_probe.yml should match jobs a and b, got %v", got["x_probe.yml"])
	}
}

func TestMultipleFileSDBlocksOneJob(t *testing.T) {
	cfg := &Config{ScrapeConfigs: []ScrapeConfig{
		{JobName: "j", FileSDConfigs: []FileSDConfig{
			{Files: []string{"/files_sd/j_main_*.yml"}},
			{Files: []string{"/files_sd/extra/*.yml"}},
		}},
	}}
	got := cfg.MatchFiles([]string{"j_main_1.yml", "extra_thing.yml"}, "/files_sd")
	if len(got["j_main_1.yml"]) != 1 || got["j_main_1.yml"][0] != "j" {
		t.Errorf("j_main_1.yml: %v", got["j_main_1.yml"])
	}
	if len(got["extra_thing.yml"]) != 0 {
		t.Errorf("file outside the extra/ dir must not match: %v", got["extra_thing.yml"])
	}
}

func TestJobHasGlobFor(t *testing.T) {
	sc := ScrapeConfig{JobName: "node", FileSDConfigs: []FileSDConfig{
		{Files: []string{"/files_sd/node_*.yml"}},
	}}
	if !JobHasGlobFor(sc, "node_a1.yml", "/files_sd") {
		t.Error("relative name against mounted glob should match")
	}
	if JobHasGlobFor(sc, "blackbox_a.yml", "/files_sd") {
		t.Error("other job prefix should not match")
	}
}

func TestGlobSnippet(t *testing.T) {
	if got := GlobSnippet("/files_sd", "myjob"); got != "/files_sd/myjob_*.yml" {
		t.Fatalf("unexpected snippet: %q", got)
	}
}
