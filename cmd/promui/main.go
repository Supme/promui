// promui is a web UI for managing Prometheus file_sd files.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"promui/internal/auth"
	"promui/internal/filesd"
	"promui/internal/prom"
	"promui/internal/web"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	// Every flag can also be set via a PROMUI_* environment variable;
	// an explicitly passed flag wins over the environment.
	var (
		filesSDDir    = flag.String("files-sd-dir", envStr("PROMUI_FILES_SD_DIR", ""), "directory with file_sd files (required)")
		prometheusURL = flag.String("prometheus-url", envStr("PROMUI_PROMETHEUS_URL", "http://127.0.0.1:9090"), "Prometheus base URL (read-only API)")
		listen        = flag.String("listen", envStr("PROMUI_LISTEN", "127.0.0.1:8080"), "listen address")
		htpasswd      = flag.String("htpasswd", envStr("PROMUI_HTPASSWD", ""), "htpasswd file for Basic Auth (empty: no authentication)")
		mountPoint    = flag.String("files-sd-mount", envStr("PROMUI_FILES_SD_MOUNT", "/files_sd"), "path under which Prometheus mounts the files_sd directory (for config snippets)")
		showVersion   = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("promui", version)
		return
	}


	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(log)

	if *filesSDDir == "" {
		log.Error("--files-sd-dir is required")
		flag.Usage()
		os.Exit(2)
	}
	if err := os.MkdirAll(*filesSDDir, 0o755); err != nil {
		log.Error("create files_sd dir", "dir", *filesSDDir, "err", err)
		os.Exit(1)
	}

	dir := &filesd.Dir{Path: *filesSDDir}
	client := prom.NewClient(*prometheusURL)
	srv := web.NewServer(dir, client, web.Options{
		FilesSDDir: *filesSDDir,
		MountPoint: strings.TrimRight(*mountPoint, "/"),
		Version:    version,
	}, log)

	var handler http.Handler = srv.Handler()
	if *htpasswd != "" {
		handler = auth.NewHtpasswd(*htpasswd).Middleware(handler)
		log.Info("basic auth enabled", "htpasswd", *htpasswd)
	}

	log.Info("promui listening",
		"listen", *listen,
		"files_sd_dir", *filesSDDir,
		"prometheus_url", *prometheusURL,
		"auth", *htpasswd != "")
	if err := http.ListenAndServe(*listen, handler); err != nil {
		log.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

// envStr returns the value of the environment variable name, or fallback
// when it is unset or empty.
func envStr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
