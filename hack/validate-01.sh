#!/usr/bin/env bash
#
# Validates the plan 01 environment: a running Lima VM, a usable Docker daemon
# reached from the Mac, and a cross-compiled Go binary that runs inside the VM.
#
# Exits 0 only when every check passes. Any failure prints a one-line reason and
# exits non-zero.

set -euo pipefail

VM_NAME="${VM_NAME:-quickspin}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LINUX_ARCH="${LINUX_ARCH:-arm64}"
LINUX_BIN="${REPO_ROOT}/bin/linux-${LINUX_ARCH}/quickspin"

# Non-interactive shells (make, CI) do not always get a TTY, so only colorize
# when stdout really is a terminal.
if [[ -t 1 ]]; then
    GREEN=$'\033[32m'
    RED=$'\033[31m'
    RESET=$'\033[0m'
else
    GREEN=""
    RED=""
    RESET=""
fi

pass() {
    printf '%s✔%s %s\n' "$GREEN" "$RESET" "$1"
}

# fail prints to stderr so a caller can capture the checkmarks separately, then
# ends the script. Every check funnels through here, which is what keeps the
# output a clear message instead of a stack of raw command errors.
fail() {
    printf '%s✘%s %s\n' "$RED" "$RESET" "$1" >&2
    exit 1
}

require_command() {
    command -v "$1" >/dev/null 2>&1 || fail "$1 is not installed or not on PATH."
}

# run_or_fail runs a command with stderr folded into stdout. On failure it dumps
# that output so the real error is visible, then ends the script with the given
# message. On success it prints the captured output for the caller to use.
run_or_fail() {
    local message="$1"
    shift

    local output
    if ! output="$("$@" 2>&1)"; then
        printf '%s\n' "$output" >&2
        fail "$message"
    fi

    printf '%s' "$output"
}

require_command limactl
require_command docker
require_command make

# --- 1. The VM is running -----------------------------------------------------
#
# `limactl list <name> --format {{.Status}}` prints nothing (exit 0) for an
# unknown instance, so the empty case has to be handled separately from the
# stopped case.
vm_status="$(limactl list "$VM_NAME" --format '{{.Status}}' 2>/dev/null || true)"

if [[ -z "$vm_status" ]]; then
    fail "Lima instance '${VM_NAME}' does not exist. Run: make lima-vm-create"
fi

if [[ "$vm_status" != "Running" ]]; then
    fail "Lima instance '${VM_NAME}' is ${vm_status}, expected Running. Run: limactl start ${VM_NAME}"
fi

pass "Lima instance '${VM_NAME}' is running."

# --- 2. Non-interactive SSH ---------------------------------------------------
#
# This runs before the image pull and the cross-build below: it is the cheapest
# way to catch a broken guest connection, and every later check depends on it.
if ! limactl shell "$VM_NAME" true >/dev/null 2>&1; then
    fail "limactl shell ${VM_NAME} true failed; non-interactive SSH into the VM is broken."
fi

pass "Non-interactive SSH works (limactl shell ${VM_NAME} true)."

# --- 3. Docker on the Mac talks to a Linux daemon -----------------------------
#
# --format only expands .Server.* when the client actually reached a daemon, so
# an unreachable socket yields an error here rather than a bogus value. The
# active context comes from the same call rather than a second `docker` process.
if ! docker_version="$(docker version --format '{{.Server.Os}} {{.Client.Context}}' 2>/dev/null)"; then
    fail "docker version could not reach a daemon. Run: make host-docker-context-use"
fi

read -r server_os docker_context <<<"$docker_version"

if [[ "$server_os" != "linux" ]]; then
    fail "Docker server OS is '${server_os}', expected 'linux'."
fi

pass "docker version reports a Linux server (context: ${docker_context})."

# --- 4. A container actually runs ---------------------------------------------
if ! container_os="$(docker run --rm alpine uname -s 2>/dev/null)"; then
    fail "docker run --rm alpine uname -s failed. Is the daemon healthy and able to pull images?"
fi

if [[ "$container_os" != "Linux" ]]; then
    fail "Container uname -s printed '${container_os}', expected 'Linux'."
fi

pass "docker run --rm alpine uname -s prints Linux."

# --- 4a. The gVisor runtime is registered and actually intercepts --------------
#
# The dmesg banner is proof of interception rather than of configuration: under
# gVisor the kernel identifying itself is runsc's sentry, which distinguishes a
# daemon that ran the container on runc anyway from one that rejected the flag.
gvisor_dmesg="$(run_or_fail "docker run --runtime=runsc failed. Is runsc registered in the guest's /etc/docker/daemon.json? Recreate the VM with: make lima-vm-delete lima-vm-create" \
    docker run --rm --runtime=runsc alpine dmesg)" || exit 1

if ! printf '%s' "$gvisor_dmesg" | grep -qi 'gvisor'; then
    printf '%s\n' "$gvisor_dmesg" >&2
    fail "The container started under --runtime=runsc but its dmesg does not mention gVisor, so it is not running on the sentry."
fi

pass "Containers run under gVisor (--runtime=runsc dmesg reports the sentry)."

# --- 5. A cross-compiled Go binary runs inside the VM -------------------------
#
# LINUX_ARCH is passed on the make command line so the build and $LINUX_BIN can
# never disagree, whichever way the arch was chosen. The Makefile's `?=` would
# also honour it via the environment; the explicit form keeps this script
# correct even if that assignment changes.
run_or_fail "make build-linux failed." \
    make -C "$REPO_ROOT" LINUX_ARCH="$LINUX_ARCH" build-linux >/dev/null

[[ -x "$LINUX_BIN" ]] || fail "Expected ${LINUX_BIN} after make build-linux, but it is missing."

# `sandbox list` needs a control plane in the guest to answer it, and starting
# one there is the only check that runs docker.New where the DOCKER_HOST lookup
# and the runsc registration check live. Its own port and database keep
# validation clear of anything `make serve-lima` left behind.
VALIDATE_PORT="${VALIDATE_PORT:-18080}"
SERVER_READY_TIMEOUT="${SERVER_READY_TIMEOUT:-30}"
serve_log="$(mktemp -t quickspin-validate-serve)"

# `--` separates limactl's own flags from the guest command. The binary lives
# under $HOME, which Lima mounts into the guest, so no copy step is needed.
# The guest writes its own pidfile because killing $serve_pid only closes the
# ssh session, and a session that died on its own leaves the port held.
limactl shell "$VM_NAME" -- sh -c \
    "echo \$\$ >\"\$HOME/quickspin-validate.pid\" && exec '${LINUX_BIN}' serve --host 127.0.0.1 --port ${VALIDATE_PORT} --db \"\$HOME/quickspin-validate.db\"" \
    >"$serve_log" 2>&1 &
serve_pid=$!

cleanup_serve() {
    kill "$serve_pid" 2>/dev/null || true
    # Reaped here so bash does not report the killed job on its own, which
    # would print "Terminated" after the script's own closing line.
    wait "$serve_pid" 2>/dev/null || true
    # sh -c so $HOME expands in the guest; limactl shell escapes bare arguments,
    # which would make rm look for a file literally named "$HOME/...".
    limactl shell "$VM_NAME" -- sh -c \
        'kill "$(cat "$HOME/quickspin-validate.pid" 2>/dev/null)" 2>/dev/null; rm -f "$HOME/quickspin-validate.pid" "$HOME/quickspin-validate.db"' \
        >/dev/null 2>&1 || true
    rm -f "$serve_log"
}
trap cleanup_serve EXIT

# Polled rather than a fixed sleep: the first start pays for schema creation, and
# a sleep long enough for that would be dead time on every later run.
serve_ready=0
for ((attempt = 0; attempt < SERVER_READY_TIMEOUT; attempt++)); do
    if guest_output="$(limactl shell "$VM_NAME" -- "$LINUX_BIN" \
        --server "http://127.0.0.1:${VALIDATE_PORT}" sandbox list --output json 2>/dev/null)"; then
        serve_ready=1
        break
    fi
    kill -0 "$serve_pid" 2>/dev/null || break
    sleep 1
done

if (( ! serve_ready )); then
    printf -- '--- guest control plane log ---\n' >&2
    cat "$serve_log" >&2
    fail "The linux/${LINUX_ARCH} control plane never answered inside '${VM_NAME}' within ${SERVER_READY_TIMEOUT}s. Is DOCKER_HOST set in the guest's /etc/environment, and is runsc registered with its daemon?"
fi

pass "The cross-compiled control plane runs in the VM and serves its API (sandbox list: ${guest_output})."

printf '\nAll checks passed.\n'
