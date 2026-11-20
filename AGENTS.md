# promui — coding conventions

## Stack

- Go 1.27 (`go 1.27` in `go.mod`); code and comments in English, comments
  short, lowercase, only where needed.
- Prefer the standard library. The only third-party dependencies:
  - `go.yaml.in/yaml/v4` for YAML (gopkg.in/yaml.v3 is frozen);
  - `github.com/abbot/go-http-auth` for htpasswd hash verification
    (`$apr1$`, bcrypt, SHA-crypt) — stdlib has none of these.
- Logging via `log/slog`: startup, file operations and backups,
  Prometheus API errors, failed authentication.
- All UI text (pages, labels, notifications, warnings, snippets) in English.

## Layout

- `cmd/promui/main.go` — flags and startup. Every flag has a `PROMUI_*`
  environment-variable equivalent (`envStr`); an explicit flag wins.
- `internal/prom` — read-only Prometheus client (`/api/v1/status/config`,
  `/api/v1/targets`), config parsing (jobs → file_sd_configs → globs).
- `internal/filesd` — scanning, parsing, validation, atomic writes, backups.
- `internal/auth` — Basic Auth against an htpasswd file.
- `internal/web` — HTTP handlers.
- `web/` — frontend: page templates and static assets (vanilla JS, CSS),
  embedded via `embed.FS` from `web/embed.go`; no npm build step.
- `Dockerfile`, `docker-entrypoint.sh`, `.dockerignore` — multi-stage build
  (golang:1.27-alpine → alpine:3.21), unprivileged runtime user, su-exec.
- `deploy/` — demo stack: `example/compose.yml` (prometheus, blackbox-exporter, promui) or `example/compose-auth.yml` with example basic auth, `example/prometheus.yml`, `example/blackbox.yml`, `example/files_sd/` example targets.

## Behavior rules

- `prometheus.yml` is never modified by promui. Missing job/glob coverage
  is reported in the UI with a ready-to-copy snippet instead.
- File → job mapping is built from the real `file_sd_configs.files` globs
  of the live configuration (fetched read-only), not from name prefixes.
  A file appears under every job whose globs match it; unmatched files are
  grouped under "Others".
- Every save or delete of an existing file first creates a backup
  `<name>.bkp_<YYYYMMDD_HHMMSS>_<9-digit ns>` next to it; the name does not
  match `*.yml` globs, so Prometheus never loads backups. The UI only
  notifies about the backup; retention, restore and deletion of backups
  are manual.
- Writes are atomic (temp file + rename in the same directory). File ids
  are sanitized to `[a-zA-Z0-9._-]`; path traversal is rejected.
- Special labels (`__scheme__`, `__metrics_path__`, `__scrape_interval__`,
  `__scrape_timeout__`, `__param_<name>`) are validated on save; unknown
  `__…` labels are stored as-is.

## Testing

`go vet ./...` and `go test ./...` must pass. Tests use `httptest` and
`t.TempDir()`; no network, no Docker.
