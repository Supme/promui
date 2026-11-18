# promui

A small web UI for managing Prometheus `file_sd` files, designed to run
next to Prometheus (e.g. on the Docker host with the `files_sd` volume
mounted).

Prometheus has no UI plugins — promui is a standalone Go binary that:

- reads the live configuration through the **read-only** API
  (`/api/v1/status/config`), extracts scrape jobs with their
  `file_sd_configs` (globs, `refresh_interval`) and other parameters;
- maps files to jobs by the **real globs** from the configuration
  (`filepath.Match` semantics), not by name-prefix heuristics: a file is
  shown under every job whose globs match it, and files no job reads are
  grouped under "Others";
- shows target statuses from `/api/v1/targets` (scrape URL, health,
  last error, last scrape);
- edits `<job>_<id>.yml` files (list of target groups
  `{targets, labels}` — the native file_sd format), with special-label
  hints, autocompletion and validation (`__scheme__`, `__metrics_path__`,
  `__scrape_interval__`, `__scrape_timeout__`, `__param_<name>`);
- **never modifies `prometheus.yml`**: when a newly created file is not
  covered by any glob of its job, promui shows a warning plus a
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
  --files-sd-dir /var/lib/prometheus/files_sd \  # required, host dir
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
| `--files-sd-mount` | `/files_sd` | path under which Prometheus sees the directory (used in suggested snippets) |

Flags can also be set through environment variables — `PROMUI_FILES_SD_DIR`,
`PROMUI_PROMETHEUS_URL`, `PROMUI_LISTEN`, `PROMUI_HTPASSWD`,
`PROMUI_FILES_SD_MOUNT`; an explicitly passed flag wins over the environment.

Create the htpasswd file outside promui, e.g. `htpasswd -B -c file admin`.

## Docker demo stack

A ready-to-run stack with Prometheus, the Blackbox exporter and promui lives
in `deploy/`:

```sh
cd deploy
docker compose up -d --build
```

| service | URL |
| --- | --- |
| prometheus | http://localhost:9090 |
| blackbox-exporter | http://localhost:9115 |
| promui | http://localhost:8080 |

`deploy/prometheus.yml` defines scrape jobs for node_exporter,
windows_exporter and blackbox probes (icmp / http / tcp / tls), all fed from
`file_sd` globs (`/etc/prometheus/file_sd/<job>_*.yml`). Example targets are
flat files in `deploy/file_sd/` named `<job>_<name>.yml`, matching those
globs.

Both containers bind-mount the same host directory `deploy/file_sd/`:
Prometheus reads it as `/etc/prometheus/file_sd`, promui edits it as
`/data/file_sd`. The example files are therefore visible from the first
start, and every edit in the UI lands directly in that host directory and
is picked up by Prometheus after `refresh_interval` (15s here) — no
restarts.

ICMP probes need the `NET_RAW` capability (already set for
blackbox-exporter in `compose.yml`); on some hosts you may also need
`sysctl net.ipv4.ping_group_range="0 2147483647"`.


## Pages & API

- `GET /` — jobs with `file_sd_configs` and their files (per real globs),
  targets table, create-file form with coverage warnings.
- `GET /edit/{name}` — group editor with special-label reference.
- `GET /api/config` — parsed jobs, file→job mapping, "Others".
- `GET /api/targets` — active targets.
- `GET/PUT/DELETE /api/files/{name}`, `POST /api/files` — file CRUD with
  validation, atomic writes and backups.

Changes take effect after the job's `refresh_interval` (default 5m) —
set a lower one in `prometheus.yml` if you iterate quickly.

## Notes

- Editing rewrites the whole file: comments and non-standard fields are
  lost (the editor says so explicitly).
- Special labels act per group; targets needing different
  `metrics_path`/`scheme` belong in separate groups.
- Only the two intentional third-party deps are used:
  `go.yaml.in/yaml/v4` and `github.com/abbot/go-http-auth`.
