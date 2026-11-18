// Package prom implements a read-only client for the Prometheus HTTP API.
package prom

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"go.yaml.in/yaml/v4"
)

// FileSDConfig is a single file_sd_configs entry of a scrape job.
type FileSDConfig struct {
	Files           []string `yaml:"files"            json:"files"`
	RefreshInterval string   `yaml:"refresh_interval" json:"refreshInterval"`
}

// ScrapeConfig is a scrape_configs entry of the Prometheus configuration.
type ScrapeConfig struct {
	JobName         string         `yaml:"job_name"         json:"jobName"`
	FileSDConfigs   []FileSDConfig `yaml:"file_sd_configs"  json:"fileSdConfigs"`
	ScrapeInterval  string         `yaml:"scrape_interval"  json:"scrapeInterval"`
	ScrapeTimeout   string         `yaml:"scrape_timeout"   json:"scrapeTimeout"`
	MetricsPath     string         `yaml:"metrics_path"     json:"metricsPath"`
	Scheme          string         `yaml:"scheme"           json:"scheme"`
}

// Config is the parsed Prometheus configuration: only the fields promui uses.
type Config struct {
	Global struct {
		ScrapeInterval string `yaml:"scrape_interval" json:"scrapeInterval"`
		ScrapeTimeout  string `yaml:"scrape_timeout"  json:"scrapeTimeout"`
	} `yaml:"global" json:"global"`
	ScrapeConfigs []ScrapeConfig `yaml:"scrape_configs" json:"scrapeConfigs"`
}

// Target is a target as reported by /api/v1/targets.
type Target struct {
	DiscoveredLabels map[string]string `json:"discoveredLabels"`
	Labels           map[string]string `json:"labels"`
	ScrapePool       string            `json:"scrapePool"`
	ScrapeURL        string            `json:"scrapeUrl"`
	GlobalURL        string            `json:"globalUrl"`
	Health           string            `json:"health"`
	LastError        string            `json:"lastError"`
	LastScrape       time.Time         `json:"lastScrape"`
	LastScrapeDur    float64           `json:"lastScrapeDuration"`
}

type apiResponse struct {
	Status string          `json:"status"`
	Data   json.RawMessage `json:"data"`
}

type statusConfigResponse struct {
	Status string `json:"status"`
	Data   struct {
		YAML string `json:"yaml"`
	} `json:"data"`
}

type targetsResponse struct {
	Status string `json:"status"`
	Data   struct {
		ActiveTargets  []Target `json:"activeTargets"`
		DroppedTargets []struct {
			DiscoveredLabels map[string]string `json:"discoveredLabels"`
		} `json:"droppedTargets"`
	} `json:"data"`
}

// Client talks to the Prometheus HTTP API (read-only endpoints only).
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// NewClient returns a client for the Prometheus at baseURL.
func NewClient(baseURL string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), HTTP: &http.Client{Timeout: 10 * time.Second}}
}

func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prometheus %s: unexpected status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// FetchConfig returns the parsed configuration from /api/v1/status/config.
func (c *Client) FetchConfig(ctx context.Context) (*Config, error) {
	body, err := c.get(ctx, "/api/v1/status/config")
	if err != nil {
		return nil, err
	}
	var scr statusConfigResponse
	if err := json.Unmarshal(body, &scr); err != nil {
		return nil, fmt.Errorf("decode /api/v1/status/config response: %w", err)
	}
	if scr.Status != "success" {
		return nil, fmt.Errorf("prometheus /api/v1/status/config: status %q", scr.Status)
	}
	var cfg Config
	if err := yaml.Unmarshal([]byte(scr.Data.YAML), &cfg); err != nil {
		return nil, fmt.Errorf("parse prometheus config yaml: %w", err)
	}
	return &cfg, nil
}

// FetchTargets returns active and dropped targets from /api/v1/targets.
type Targets struct {
	Active  []Target
	Dropped []map[string]string
}

// FetchTargets returns the current targets from /api/v1/targets.
func (c *Client) FetchTargets(ctx context.Context) (*Targets, error) {
	body, err := c.get(ctx, "/api/v1/targets")
	if err != nil {
		return nil, err
	}
	var tr targetsResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("decode /api/v1/targets response: %w", err)
	}
	if tr.Status != "success" {
		return nil, fmt.Errorf("prometheus /api/v1/targets: status %q", tr.Status)
	}
	dropped := make([]map[string]string, 0, len(tr.Data.DroppedTargets))
	for _, d := range tr.Data.DroppedTargets {
		dropped = append(dropped, d.DiscoveredLabels)
	}
	return &Targets{Active: tr.Data.ActiveTargets, Dropped: dropped}, nil
}

// MatchFiles reports for every file (relative to the files_sd directory)
// which jobs pick it up, as a map file -> list of job names.
// A file matching no glob at all belongs to "Others".
// mountPoint is the path under which Prometheus mounts the directory.
func (cfg *Config) MatchFiles(files []string, mountPoint string) map[string][]string {
	result := make(map[string][]string, len(files))
	for _, f := range files {
		var jobs []string
		for _, sc := range cfg.ScrapeConfigs {
			if jobMatchesFile(sc, f, mountPoint) {
				jobs = append(jobs, sc.JobName)
			}
		}
		result[f] = jobs
	}
	return result
}

// JobHasGlobFor reports whether any file_sd glob of the job covers name
// (a file path relative to the files_sd directory).
func JobHasGlobFor(sc ScrapeConfig, name, mountPoint string) bool {
	return jobMatchesFile(sc, name, mountPoint)
}

func jobMatchesFile(sc ScrapeConfig, name, mountPoint string) bool {
	for _, fsd := range sc.FileSDConfigs {
		for _, pattern := range fsd.Files {
			if globMatch(pattern, name, mountPoint) {
				return true
			}
		}
	}
	return false
}

// globMatch matches a files_sd glob against a file path relative to the
// files_sd directory. Prometheus globs are usually absolute (e.g.
// /files_sd/node_*.yml), so besides the literal pattern the glob with
// the mount point prefix stripped is tried against the relative path.
func globMatch(pattern, name, mountPoint string) bool {
	patterns := []string{pattern}
	if mountPoint != "" {
		if rest, ok := strings.CutPrefix(pattern, mountPoint+"/"); ok {
			patterns = append(patterns, rest)
		}
	}
	for _, p := range patterns {
		if ok, err := path.Match(p, name); err == nil && ok {
			return true
		}
	}
	return false
}

// GlobSnippet returns the files glob promui suggests for a file created
// under the given directory prefix, e.g. "/files_sd/myjob_*.yml".
func GlobSnippet(dirPrefix, jobName string) string {
	return fmt.Sprintf("%s/%s_*.yml", strings.TrimRight(dirPrefix, "/"), jobName)
}
