// Package web serves the promui HTTP API and pages.
package web

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"sync"

	"promui/internal/filesd"
	"promui/internal/prom"
	uiweb "promui/web"
)

// Options configures the web server.
type Options struct {
	// FilesSDDir is the host directory with file_sd files.
	FilesSDDir string
	// MountPoint is the path under which Prometheus sees the directory
	// (used for suggested snippets, e.g. /files_sd).
	MountPoint string
	// Version of the binary, shown in the page footer and /api/config.
	Version string
}

// Server wires the files_sd directory, the Prometheus client and the
// HTTP handlers together.
type Server struct {
	Dir  *filesd.Dir
	Prom *prom.Client
	Opt  Options
	Log  *slog.Logger

	tpl *template.Template
	// serializes read-modify-write cycles (backup + write/delete)
	mu sync.Mutex
}

// NewServer builds a Server serving dir against the Prometheus client.
func NewServer(dir *filesd.Dir, client *prom.Client, opt Options, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		Dir:  dir,
		Prom: client,
		Opt:  opt,
		Log:  log,
		tpl:  template.Must(template.ParseFS(uiweb.Content, "*.html")),
	}
}

// Handler returns the complete HTTP handler (without auth).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/config", s.handleAPIConfig)
	mux.HandleFunc("GET /api/targets", s.handleAPITargets)
	mux.HandleFunc("GET /api/files", s.handleAPIFiles)
	mux.HandleFunc("GET /api/files/{name}", s.handleGetFile)
	mux.HandleFunc("POST /api/files", s.handleCreateFile)
	mux.HandleFunc("PUT /api/files/{name}", s.handlePutFile)
	mux.HandleFunc("DELETE /api/files/{name}", s.handleDeleteFile)
	// Pages and assets get no-cache: embed files carry no modtime, so
	// heuristic browser caching would keep serving a stale UI.
	// The file editor is a modal on the index page (single-page app),
	// so there is no /edit/ route anymore.
	mux.HandleFunc("GET /{$}", noCache(s.handleIndex))
	mux.Handle("GET /static/", noCacheHandler(http.FileServerFS(uiweb.Content)))
	return mux
}

// noCache wraps a handler func with Cache-Control: no-cache.
func noCache(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		next(w, r)
	}
}

// noCacheHandler wraps an http.Handler with Cache-Control: no-cache.
func noCacheHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}

// FilesView is the payload of GET /api/files: the file list straight from
// the files_sd directory, independent of the Prometheus API.
type FilesView struct {
	Files []string `json:"files"`
	Error string   `json:"error,omitempty"`
}

// handleAPIFiles lists all file_sd files; used by the edit picker so it
// works even when Prometheus is unreachable.
func (s *Server) handleAPIFiles(w http.ResponseWriter, r *http.Request) {
	files, err := s.Dir.List()
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if files == nil {
		files = []string{} // emit [] rather than null for an empty dir
	}
	writeJSON(w, http.StatusOK, FilesView{Files: files})
}

// ---- view models ----

// FileView is one file_sd file with the jobs whose globs match it.
type FileView struct {
	Name string   `json:"name"`
	Jobs []string `json:"jobs"`
}

// JobView is a scrape job with its file_sd_configs and matched files.
type JobView struct {
	Name           string              `json:"name"`
	FileSDConfigs  []prom.FileSDConfig `json:"fileSdConfigs"`
	ScrapeInterval string              `json:"scrapeInterval"`
	ScrapeTimeout  string              `json:"scrapeTimeout"`
	MetricsPath    string              `json:"metricsPath"`
	Scheme         string              `json:"scheme"`
	Files          []FileView          `json:"files"`
}

// ConfigView is the payload of GET /api/config.
type ConfigView struct {
	Jobs         []JobView `json:"jobs"`
	Others       []string  `json:"others"` // files no job reads
	TargetsError string    `json:"targetsError,omitempty"`
	ConfigError  string    `json:"configError,omitempty"`
	FilesSDDir   string    `json:"filesSdDir"`
	MountPoint   string    `json:"mountPoint"`
	Version      string    `json:"version"`
}

// buildConfigView assembles jobs, per-job file lists and Others from the
// live Prometheus config and the directory contents.
func (s *Server) buildConfigView(ctx context.Context) *ConfigView {
	view := &ConfigView{FilesSDDir: s.Opt.FilesSDDir, MountPoint: s.Opt.MountPoint, Version: s.Opt.Version}
	cfg, err := s.Prom.FetchConfig(ctx)
	if err != nil {
		view.ConfigError = err.Error()
		s.Log.Error("fetch prometheus config", "err", err)
		return view
	}
	files, err := s.Dir.List()
	if err != nil {
		view.ConfigError = err.Error()
		return view
	}
	matched := cfg.MatchFiles(files, s.Opt.MountPoint)
	for _, sc := range cfg.ScrapeConfigs {
		jv := JobView{
			Name:           sc.JobName,
			FileSDConfigs:  sc.FileSDConfigs,
			ScrapeInterval: sc.ScrapeInterval,
			ScrapeTimeout:  sc.ScrapeTimeout,
			MetricsPath:    sc.MetricsPath,
			Scheme:         sc.Scheme,
		}
		if jv.ScrapeInterval == "" {
			jv.ScrapeInterval = cfg.Global.ScrapeInterval
		}
		for _, f := range files {
			for _, j := range matched[f] {
				if j == sc.JobName {
					jv.Files = append(jv.Files, FileView{Name: f, Jobs: matched[f]})
					break
				}
			}
		}
		view.Jobs = append(view.Jobs, jv)
	}
	for _, f := range files {
		if len(matched[f]) == 0 {
			view.Others = append(view.Others, f)
		}
	}
	// The API must return [] (not null) so clients can iterate safely.
	if view.Jobs == nil {
		view.Jobs = []JobView{}
	}
	if view.Others == nil {
		view.Others = []string{}
	}
	for i := range view.Jobs {
		if view.Jobs[i].FileSDConfigs == nil {
			view.Jobs[i].FileSDConfigs = []prom.FileSDConfig{}
		}
		if view.Jobs[i].Files == nil {
			view.Jobs[i].Files = []FileView{}
		}
	}
	return view
}

// TargetsView is the payload of GET /api/targets.
type TargetsView struct {
	Targets []prom.Target `json:"targets"`
	Error   string        `json:"error,omitempty"`
}

// handleAPIConfig serves the parsed configuration and file mapping.
func (s *Server) handleAPIConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.buildConfigView(r.Context()))
}

// handleAPITargets proxies /api/v1/targets in a UI-friendly shape.
func (s *Server) handleAPITargets(w http.ResponseWriter, r *http.Request) {
	view := &TargetsView{}
	t, err := s.Prom.FetchTargets(r.Context())
	if err != nil {
		view.Error = err.Error()
		writeJSON(w, http.StatusOK, view)
		return
	}
	view.Targets = t.Active
	writeJSON(w, http.StatusOK, view)
}

// handleGetFile returns the parsed groups of one file.
func (s *Server) handleGetFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	f, err := s.Dir.Read(name)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, f)
}

// CreateRequest is the body of POST /api/files.
type CreateRequest struct {
	Job string `json:"job"`
	ID  string `json:"id"`
}

// CreateResponse reports the created file plus config warnings.
type CreateResponse struct {
	File    string `json:"file"`
	Warning string `json:"warning,omitempty"`
	Snippet string `json:"snippet,omitempty"`
}

// handleCreateFile creates a new empty file and warns when the current
// prometheus.yml globs would not pick it up.
func (s *Server) handleCreateFile(w http.ResponseWriter, r *http.Request) {
	if !requireJSON(w, r) {
		return
	}
	var req CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "invalid json body: "+err.Error())
		return
	}
	req.Job = strings.TrimSpace(req.Job)
	req.ID = strings.TrimSpace(req.ID)
	if req.Job == "" {
		httpError(w, http.StatusBadRequest, "job is required")
		return
	}
	if !filesd.ValidID(req.ID) {
		httpError(w, http.StatusBadRequest, "invalid id: allowed characters are a-z A-Z 0-9 . _ -")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.Dir.Create(req.Job, req.ID); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	name := filesd.FileName(req.Job, req.ID)
	s.Log.Info("file created", "file", name)
	resp := CreateResponse{File: name}
	warning, snippet := s.coverageWarning(r.Context(), req.Job, name)
	resp.Warning, resp.Snippet = warning, snippet
	writeJSON(w, http.StatusCreated, resp)
}

// coverageWarning checks whether any glob of job covers the new file
// path (as seen by Prometheus: mountPoint/name). Returns a warning and
// a ready-to-copy prometheus.yml snippet when it does not.
func (s *Server) coverageWarning(ctx context.Context, job, name string) (string, string) {
	cfg, err := s.Prom.FetchConfig(ctx)
	if err != nil {
		return fmt.Sprintf("could not check coverage: %s", err.Error()), ""
	}
	var sc *prom.ScrapeConfig
	for i := range cfg.ScrapeConfigs {
		if cfg.ScrapeConfigs[i].JobName == job {
			sc = &cfg.ScrapeConfigs[i]
			break
		}
	}
	mounted := s.Opt.MountPoint + "/" + name
	if sc != nil && prom.JobHasGlobFor(*sc, name, s.Opt.MountPoint) {
		return "", ""
	}
	suggested := prom.GlobSnippet(s.Opt.MountPoint, job)
	if sc == nil {
		warning := fmt.Sprintf("job %q is not present in the prometheus.yml scrape_configs, Prometheus will not read %s until you add it.", job, mounted)
		snippet := fmt.Sprintf(`scrape_configs:
  - job_name: %s
    file_sd_configs:
      - files:
          - %s`, job, suggested)
		return warning, snippet
	}
	var existing []string
	for _, fsd := range sc.FileSDConfigs {
		existing = append(existing, fsd.Files...)
	}
	warning := fmt.Sprintf("none of the file_sd_configs globs of job %q covers %s; Prometheus will not read the file until you extend the globs. Current globs: %s.",
		job, mounted, strings.Join(existing, ", "))
	snippet := fmt.Sprintf(`# add to the file_sd_configs of job %q:
  - files:
      - %s`, job, suggested)
	return warning, snippet
}

// PutRequest is the body of PUT /api/files/{name}.
type PutRequest struct {
	Groups []filesd.Group `json:"groups"`
}

// PutResponse confirms a save and reports the created backup.
type PutResponse struct {
	Backup string `json:"backup"`
}

// handlePutFile validates, backs up and rewrites one file atomically.
func (s *Server) handlePutFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !filesd.ValidName(name) {
		httpError(w, http.StatusBadRequest, "invalid file name")
		return
	}
	if !requireJSON(w, r) {
		return
	}
	var req PutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "invalid json body: "+err.Error())
		return
	}
	f := &filesd.File{Groups: req.Groups}
	if errs := f.Validate(); len(errs) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"errors": errs})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.Dir.Read(name)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Keep the leading comment block (e.g. "# node_exporter targets")
	// of the file being rewritten.
	f.Header = cur.Header
	data, err := f.Render()
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	backup, err := s.Dir.Backup(name)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "backup failed: "+err.Error())
		return
	}
	if err := s.Dir.WriteFile(name, data); err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Log.Info("file saved", "file", name, "backup", backup)
	writeJSON(w, http.StatusOK, PutResponse{Backup: backup})
}

// handleDeleteFile backs up and removes one file.
func (s *Server) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !filesd.ValidName(name) {
		httpError(w, http.StatusBadRequest, "invalid file name")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.Dir.Read(name); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	backup, err := s.Dir.Backup(name)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "backup failed: "+err.Error())
		return
	}
	if err := s.Dir.Remove(name); err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Log.Info("file deleted", "file", name, "backup", backup)
	writeJSON(w, http.StatusOK, PutResponse{Backup: backup})
}

// handleIndex renders the main page.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{"Version": s.Opt.Version}
	if err := s.tpl.ExecuteTemplate(w, "index.html", data); err != nil {
		s.Log.Error("render index", "err", err)
	}
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// maxBodyBytes caps request bodies (file contents are small).
const maxBodyBytes = 10 << 20

// requireJSON rejects non-JSON bodies and caps the body size. For POST
// it also blocks cross-origin "simple" requests, which never carry
// Content-Type: application/json.
func requireJSON(w http.ResponseWriter, r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		httpError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	return true
}
