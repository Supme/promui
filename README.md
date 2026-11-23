# PromUI

[English](README.md) · [Русский](README-ru.md)

A small web UI for managing Prometheus `file_sd` files, designed to run
next to Prometheus (e.g. on the Docker host with the `files_sd` volume
mounted).

PromUI is an application that:

- reads the live configuration through the **read-only** API
  (`/api/v1/status/config`), extracts scrape jobs with their
  `file_sd_configs` (globs, `refresh_interval`) and other parameters;
- maps files to jobs by the **real globs** from the configuration
  (`path.Match` semantics, relative to the files_sd directory), not by
  name-prefix heuristics: a file is shown under every job whose globs match
  it, and files no job reads are grouped under "Others";
- shows target statuses from `/api/v1/targets` (scrape URL, health,
  last error, last scrape);
- edits `<job>_<id>.yml` files (list of target groups
  `{targets, labels}` — the native file_sd format), with special-label
  hints, autocompletion and validation (`__scheme__`, `__metrics_path__`,
  `__scrape_interval__`, `__scrape_timeout__`, `__param_<name>`);
- **never modifies `prometheus.yml`**: when a newly created file is not
  covered by any glob of its job, PromUI shows a warning plus a
  ready-to-copy snippet for the missing `file_sd_configs` entry (or a
  whole `scrape_configs` block for an unknown job);
- creates a backup before every save/delete:
  `<name>.bkp_<YYYYMMDD_HHMMSS>_<9-digit ns>` — the suffix does not match
  `*.yml` globs, so Prometheus never loads backups. Backups are managed
  manually; the UI only notifies about their creation.

## Build

```sh
go build -o promui ./cmd/promui
go vet ./... && go test ./...
```

## Run

```sh
./promui \
  --files-sd-dir /etc/prometheus/files_sd \       # required, host dir
  --prometheus-url http://127.0.0.1:9090 \       # read-only API
  --listen 127.0.0.1:8080 \                      # UI address
  --htpasswd /etc/promui/htpasswd                # optional Basic Auth
```

Flags:

| flag | default | meaning |
| --- | --- | --- |
| `--files-sd-dir` | — (required) | directory with file_sd files |
| `--prometheus-url` | `http://127.0.0.1:9090` | Prometheus base URL |
| `--listen` | `127.0.0.1:8080` | listen address |
| `--htpasswd` | empty (no auth) | htpasswd file; re-read per request; supports `$apr1$`, bcrypt, SHA-crypt |
| `--proxy-header` | empty (use the connection address) | header with the client IP behind a trusted proxy, e.g. `X-Forwarded-For` (used as the lockout key) |
| `--files-sd-mount` | `/files_sd` | path under which Prometheus sees the directory (used in suggested snippets) |
| `--version` | `false` | print version and exit |

Flags can also be set through environment variables — `PROMUI_FILES_SD_DIR`,
`PROMUI_PROMETHEUS_URL`, `PROMUI_LISTEN`, `PROMUI_HTPASSWD`,
`PROMUI_PROXY_HEADER`, `PROMUI_FILES_SD_MOUNT`; an explicitly passed
flag wins over the environment.

Create the htpasswd file outside promui, e.g. `htpasswd -B -c file admin`.

A client is locked out for 15 minutes after 5 failed logins within 10
minutes (keyed by the connection address, or the `--proxy-header` value
behind a proxy).

## Systemd

Run promui as a service: the unit is `deploy/promui.service`,
configuration is the `PROMUI_*` variables in `/etc/default/promui`
(same names as the flags; template: `deploy/promui.env.example`).

```sh
sudo useradd --system --shell /usr/sbin/nologin promui
sudo install -d -o promui -g promui /etc/prometheus/files_sd
sudo install -m 755 promui /usr/local/bin/promui
sudo cp deploy/promui.service /etc/systemd/system/promui.service
sudo cp deploy/promui.env.example /etc/default/promui
sudo vi /etc/default/promui
sudo systemctl daemon-reload
sudo systemctl enable --now promui
```

The service runs as the unprivileged `promui` user and restarts on
failure; the files_sd directory must be writable by that user.

## Docker

Image: `ghcr.io/supme/promui:latest` or use version tag `ghcr.io/supme/promui:0.0.1`

### demo stack

A ready-to-run stack with Prometheus, the Blackbox exporter and PromUI lives in `deploy/example`:

```sh
cd deploy/example
docker compose up -d --build
```

| service | URL |
| --- | --- |
| prometheus | http://localhost:9090 |
| blackbox-exporter | http://localhost:9115 |
| promui | http://localhost:8080 |

`deploy/example/prometheus.yml` defines scrape jobs for node_exporter,
windows_exporter and blackbox probes (icmp / http / tcp / tls), all fed from
`file_sd` globs (`/etc/prometheus/files_sd/<job>_*.yml`). Example targets are
flat files in `deploy/example/files_sd/` named `<job>_<name>.yml`, matching
those globs.

Both containers bind-mount the same host directory
`deploy/example/files_sd/`: Prometheus reads it as
`/etc/prometheus/files_sd`, PromUI edits it as `/data/files_sd`. The example
files are therefore visible from the first
start, and every edit in the UI lands directly in that host directory and
is picked up by Prometheus after `refresh_interval` (15s here) — no
restarts.

ICMP probes need the `NET_RAW` capability (already set for
blackbox-exporter in `deploy/example/compose.yml`); on some hosts you may
also need
`sysctl net.ipv4.ping_group_range="0 2147483647"`.


## Pages & API

- `GET /` — the single page: jobs with `file_sd_configs` and their files
  (per real globs), targets table, create-file form with coverage warnings,
  group editor (modal) with special-label reference.
- `GET /api/config` — parsed jobs, file→job mapping, "Others".
- `GET /api/targets` — active targets.
- `GET /api/files` — file list from the directory (works even when
  Prometheus is unreachable).
- `GET/PUT/DELETE /api/files/{name}`, `POST /api/files` — file CRUD with
  validation, atomic writes and backups.

Changes take effect after the job's `refresh_interval` (default 5m) —
set a lower one in `prometheus.yml` if you iterate quickly.

## Notes

- Editing rewrites the whole file: non-standard fields are lost, and only
  the leading comment block is preserved.
- Special labels act per group; targets needing different
  `metrics_path`/`scheme` belong in separate groups.
- Only the two intentional third-party deps are used:
  `go.yaml.in/yaml/v4` and `github.com/abbot/go-http-auth`.
