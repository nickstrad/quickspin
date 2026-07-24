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

# --- 5. A cross-compiled Go binary runs inside the VM -------------------------
#
# LINUX_ARCH is passed on the make command line so the build and $LINUX_BIN can
# never disagree, whichever way the arch was chosen. The Makefile's `?=` would
# also honour it via the environment; the explicit form keeps this script
# correct even if that assignment changes.
run_or_fail "make build-linux failed." \
    make -C "$REPO_ROOT" LINUX_ARCH="$LINUX_ARCH" build-linux >/dev/null

[[ -x "$LINUX_BIN" ]] || fail "Expected ${LINUX_BIN} after make build-linux, but it is missing."

# `--` separates limactl's own flags from the guest command. The binary lives
# under $HOME, which Lima mounts into the guest, so no copy step is needed.
# `fail` inside a command substitution can only exit that subshell, so the
# explicit `|| exit 1` is what stops the script here.
guest_output="$(run_or_fail "The linux/${LINUX_ARCH} binary did not run inside '${VM_NAME}'." \
    limactl shell "$VM_NAME" -- "$LINUX_BIN" validate)" || exit 1

pass "Cross-compiled binary runs in the VM (output: ${guest_output})."

printf '\nAll checks passed.\n'
