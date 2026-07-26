#!/usr/bin/env bash
#
# One compiled-binary lifecycle smoke: create → inspect → list → destroy →
# destroy again → confirm absent, each in its own process, reading JSON output.
#
# It proves composition — Cobra argument handling, runtime.NewDockerRuntime(nil),
# the forwarded socket, a real daemon, and the process exit status — and nothing
# else. Flag parsing, output formats, sorting, and sentinel wrapping are covered
# far more cheaply through runtimetest.Fake in internal/cli.
#
# The caller supplies a working DOCKER_HOST; this script never chooses a daemon.
# hack/test-runtime-docker.sh is what points it at the dedicated test VM.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=hack/common.sh
source "${REPO_ROOT}/hack/common.sh"

BIN="${REPO_ROOT}/bin/quickspin"
# hack/test-runtime-docker.sh exports this so the smoke and the live Go suite pin
# the same image. The default is only for a standalone run; the requirement is a
# default process that stays up, so inspect and list are not racing the
# container's own exit.
IMAGE="${QUICKSPIN_SMOKE_IMAGE:-docker.io/library/nginx:1.27-alpine}"

require_command go
require_command jq "The smoke asserts on parsed JSON rather than grepped text."

[[ -n "${DOCKER_HOST:-}" ]] || fail "DOCKER_HOST is unset. Run this through 'make test-docker', which points it at the dedicated test VM."

# --- 0. The real binary -------------------------------------------------------
#
# Built rather than `go run`: the point is the compiled entrypoint, including its
# exit status, which `go run` conflates with its own.
make -C "$REPO_ROOT" build >/dev/null
[[ -x "$BIN" ]] || fail "Expected ${BIN} after make build, but it is missing."

pass "Built ${BIN}."

# --- 1. Create ----------------------------------------------------------------
created="$("$BIN" sandbox create "$IMAGE" --output json)"
# Captured first, then parsed, so a non-JSON payload can be printed by the
# diagnostic below. `|| true` for the same EOF-under-`set -e` reason as the
# `limactl list` read in hack/test-runtime-docker.sh.
IFS=$'\t' read -r id state < <(jq -r '[.id, .state] | @tsv' <<<"$created") || true

if [[ -z "$id" || "$id" == "null" ]]; then
    printf '%s\n' "$created" >&2
    fail "sandbox create printed no id (output above)."
fi

# Registered here, before any assertion that can fail: a smoke that died between
# create and destroy would leak the container and then fail the harness's leak
# check for the wrong reason.
cleanup() {
    "$BIN" sandbox destroy "$id" --output json >/dev/null 2>&1 || true
}
trap cleanup EXIT

[[ "$state" == "running" ]] || fail "sandbox create reported state '${state}', expected 'running'."

pass "sandbox create returned ${id} in state running."

# --- 2. Inspect ---------------------------------------------------------------
inspected="$("$BIN" sandbox inspect "$id" --output json)"
IFS=$'\t' read -r inspected_id inspected_state < <(jq -r '[.id, .state] | @tsv' <<<"$inspected") || true

if [[ "$inspected_id" != "$id" ]]; then
    printf '%s\n' "$inspected" >&2
    fail "sandbox inspect reported id '${inspected_id}', expected '${id}' (output above)."
fi
[[ "$inspected_state" == "running" ]] || fail "sandbox inspect reported state '${inspected_state}', expected 'running'."

pass "sandbox inspect finds ${id} running in a separate process."

# --- 3. List ------------------------------------------------------------------
#
# Membership, not a count: the daemon may hold sandboxes this script did not make.
if ! "$BIN" sandbox list --output json | jq -e --arg id "$id" 'any(.[]; .id == $id)' >/dev/null; then
    fail "sandbox list does not contain ${id}."
fi

pass "sandbox list contains ${id}."

# --- 4. Destroy, twice --------------------------------------------------------
"$BIN" sandbox destroy "$id" --output json >/dev/null || fail "sandbox destroy ${id} failed."

# Cleanup is retry safe by contract, and a nonzero exit here is what a recovery
# loop would trip over.
"$BIN" sandbox destroy "$id" --output json >/dev/null || fail "the second sandbox destroy ${id} failed; destroy must be idempotent."

pass "sandbox destroy ${id} succeeds twice."

# --- 5. Absence ---------------------------------------------------------------
#
# Inspect must fail, which also proves the binary exits nonzero on a runtime
# error rather than printing one and exiting 0.
if "$BIN" sandbox inspect "$id" --output json >/dev/null 2>&1; then
    fail "sandbox inspect ${id} succeeded after destroy, expected a not-found failure."
fi

pass "sandbox inspect ${id} fails after destroy."

trap - EXIT
printf '\nCLI smoke passed.\n'
