#!/usr/bin/env bash
#
# The compiled-binary smoke: build it, run `serve` as a separate process, and
# drive it with a second process that talks over a real socket.
#
# It covers the process boundaries `go test` cannot: the compiled entrypoint,
# QUICKSPIN_SERVER discovery, and command exit status. The real-daemon lifecycle
# lives in internal/client/client_live_test.go.
#
# Nothing here creates a sandbox, so this script needs no Docker daemon and no
# fixture image. `serve` builds a Docker client from the environment, but the SDK
# does not connect until a request needs it, and none of the commands below does.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=hack/common.sh
source "${REPO_ROOT}/hack/common.sh"

BIN="${REPO_ROOT}/bin/quickspin"

# A fixed port rather than an ephemeral one: the server reports the port it was
# told to use, not the one the kernel picked, so port 0 would leave this script
# with no way to learn where to connect. Overridable for the collision case.
SERVER_PORT="${QUICKSPIN_SMOKE_PORT:-8757}"
SERVER_READY_TIMEOUT="${SERVER_READY_TIMEOUT:-30}"

require_command go
require_command make

# --- 1. The real binary -------------------------------------------------------
#
# Built rather than `go run`: the point is the compiled entrypoint, including its
# exit status, which `go run` conflates with its own.
make -C "$REPO_ROOT" build >/dev/null
[[ -x "$BIN" ]] || fail "Expected ${BIN} after make build, but it is missing."

pass "Built ${BIN}."

# --- 2. The control plane -----------------------------------------------------
#
# Its own database in a temp directory, never the developer's default.
STATE_DIR="$(mktemp -d)"
SERVER_LOG="${STATE_DIR}/serve.log"
export QUICKSPIN_SERVER="http://127.0.0.1:${SERVER_PORT}"

"$BIN" serve --host 127.0.0.1 --port "$SERVER_PORT" --db "${STATE_DIR}/quickspin.db" \
    >"$SERVER_LOG" 2>&1 &
SERVER_PID=$!

cleanup() {
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
    rm -rf "$STATE_DIR"
}
trap cleanup EXIT

# Probe with the client under test, avoiding an extra curl dependency.
probe_server() {
    list_output="$("$BIN" sandbox list --output json 2>/dev/null)"
}

server_ready=0
for ((attempt = 0; attempt < SERVER_READY_TIMEOUT; attempt++)); do
    if probe_server; then
        server_ready=1
        break
    fi
    kill -0 "$SERVER_PID" 2>/dev/null || break
    sleep 1
done

if (( ! server_ready )); then
    cat "$SERVER_LOG" >&2
    fail "The control plane at ${QUICKSPIN_SERVER} was not ready within ${SERVER_READY_TIMEOUT}s (server log above)."
fi

pass "A second process reached the control plane at ${QUICKSPIN_SERVER}."

# --- 3. An empty store lists as an empty JSON array ---------------------------
#
# `[]` rather than `null` or nothing at all: it is the one output assertion worth
# making from a separate process, because it is the shape every client parses.
[[ "$list_output" == "[]" ]] || fail "sandbox list on an empty store printed '${list_output}', expected '[]'."

pass "sandbox list prints an empty JSON array."

# --- 4. A failed command exits nonzero ----------------------------------------
#
# The one assertion no Go test can make: internal/cli returns an error, and
# main.go is what turns it into an exit status. An id the store has never seen
# is a 404 the server answers without consulting the runtime.
if "$BIN" sandbox inspect sbx-does-not-exist --output json >/dev/null 2>&1; then
    fail "sandbox inspect on an unknown id exited 0; a failed command must exit nonzero."
fi

pass "A failed command exits nonzero."

trap - EXIT
cleanup
printf '\nCLI smoke passed.\n'
