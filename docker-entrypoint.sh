#!/bin/sh
# Entry point for the promui container.
#
# Runs as root by default only to chown the mounted file_sd volume to the
# unprivileged runtime user, then drops privileges via su-exec.

set -e

FILES_SD_DIR="${PROMUI_FILES_SD_DIR:-/data/file_sd}"

if [ "$(id -u)" = "0" ]; then
    # Ensure the mounted file_sd directory is writable by the runtime user.
    chown -R promui:promui "$FILES_SD_DIR" 2>/dev/null || true

    if [ "$#" -gt 0 ] && [ "${1#-}" != "$1" ]; then
        # First argument is a flag: prepend the binary name.
        set -- promui "$@"
    fi

    if [ "$1" = "promui" ]; then
        shift
        exec su-exec promui:promui promui "$@"
    fi

    exec su-exec promui:promui "$@"
fi

exec "$@"
