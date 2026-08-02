#!/usr/bin/env bash
#
# Crash convergence for roadmap 06: kill -9 the control plane at each of the
# three half-done points, restart it, and prove the reconciler closes the gap.
#
# The three points are the ones a level-triggered loop exists to survive:
#
#   a) row inserted, container not yet created   (kill inside the create window)
#   b) container created, sandbox running        (kill with both worlds agreeing)
#   c) DELETE accepted, row stopping             (kill inside the destroy window)
#
# A fourth check runs the other direction: a container carrying the managed
# label with no row behind it must be destroyed, because the database is
# authoritative.
#
# Convergence is asserted as an invariant over both worlds rather than as a
# sequence of expected transitions: no row is left pending or stopping, every
# running row has a running container, every terminal row has none, and no
# managed container lacks a row. That statement is true of a healthy system at
# rest whatever path it took to get there, which is what makes it safe to poll.
#
# The daemon runs on the Mac against the VM's Docker daemon, the same shape as
# `make serve`, so kill -9 acts on a pid this script owns. Like
# hack/test-runtime-docker.sh, this script never switches the developer's Docker
# context: it exports DOCKER_HOST for its own children only.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=hack/common.sh
source "${REPO_ROOT}/hack/common.sh"

VM_NAME="${VM_NAME:-quickspin}"
BIN="${REPO_ROOT}/bin/quickspin"

# A fixed port for the same reason as the CLI smoke: the daemon is told its port
# rather than reporting one, so port 0 would leave this script nowhere to connect.
SERVER_PORT="${QUICKSPIN_VALIDATE06_PORT:-18106}"
SERVER_READY_TIMEOUT="${SERVER_READY_TIMEOUT:-60}"
DOCKER_READY_TIMEOUT="${DOCKER_READY_TIMEOUT:-90}"

# Spelled here because shell cannot import internal/runtime/docker/labels.go,
# which declares these a wire format for exactly this reason. Both are required:
# the id label must parse as a sandbox id or runtime.List skips the container,
# and a skipped container is invisible to the orphan sweep under test.
MANAGED_LABEL="quickspin.managed=true"
SANDBOX_ID_LABEL="quickspin.id"

# Two intervals, because the script needs the loop to be both fast and absent.
# Convergence phases run on the short one so a wait costs seconds. The crash
# phases run on the long one: the kill has to land between the write and the
# next tick, and the only way to make that window deterministic is to make it
# far longer than the work between them.
RECONCILE_INTERVAL="${RECONCILE_INTERVAL:-2s}"
CRASH_WINDOW_INTERVAL="${CRASH_WINDOW_INTERVAL:-5m}"

# Dominated by the image pull and container create a pass performs, not by the
# interval. Generous on purpose: a tight budget fails a correct reconciler on a
# loaded machine, which is the one failure this script must not produce.
CONVERGE_TIMEOUT="${CONVERGE_TIMEOUT:-90}"

# The default spec's alpine:3.20 exits the moment it starts, so a sandbox built
# from it converges to failed and could never satisfy "running row, running
# container". The image under test has to keep a process up; nginx is the same
# fixture the live Go suites pin.
SANDBOX_IMAGE="${QUICKSPIN_TEST_IMAGE:-docker.io/library/nginx:1.27-alpine}"

# Same reason as `make serve`: without it the isolation boundary would be runc.
export QUICKSPIN_DOCKER_RUNTIME="${QUICKSPIN_DOCKER_RUNTIME:-runsc}"

require_command limactl
require_command docker
require_command sqlite3
require_command jq
require_command make
require_command uuidgen

# --- 1. The VM and its forwarded socket ---------------------------------------
vm_status=""
vm_dir=""
IFS=$'\t' read -r vm_status vm_dir < <(
    limactl list "$VM_NAME" --format '{{.Status}}{{"\t"}}{{.Dir}}' 2>/dev/null || true
) || true

if [[ -z "${vm_status:-}" ]]; then
    fail "Lima instance '${VM_NAME}' does not exist. Run: make env-create"
fi
if [[ "$vm_status" != "Running" ]]; then
    fail "Lima instance '${VM_NAME}' is ${vm_status}, expected Running. Run: limactl start ${VM_NAME}"
fi

export DOCKER_HOST="unix://${vm_dir}/sock/docker.sock"

deadline=$((SECONDS + DOCKER_READY_TIMEOUT))
until docker version --format '{{.Server.Os}}' >/dev/null 2>&1; do
    (( SECONDS < deadline )) || fail "The Docker daemon at ${DOCKER_HOST} was not ready within ${DOCKER_READY_TIMEOUT}s."
    sleep 2
done

pass "Docker daemon reachable at ${DOCKER_HOST}."

# --- 2. The backend-native views of both worlds -------------------------------
#
# The container queries deliberately do not go through quickspin: quickspin is
# the thing under test, and a container it has lost track of is exactly the leak
# this script exists to catch.
managed_ids() {
    docker ps -aq --filter "label=${MANAGED_LABEL}"
}

managed_sandbox_ids() {
    docker ps -a --filter "label=${MANAGED_LABEL}" --format "{{.Label \"${SANDBOX_ID_LABEL}\"}}"
}

managed_containers() {
    docker ps -a --filter "label=${MANAGED_LABEL}" \
        --format "{{.ID}}  {{.State}}  {{.Label \"${SANDBOX_ID_LABEL}\"}}"
}

remove_managed() {
    local ids
    ids="$(managed_ids)"
    [[ -z "$ids" ]] && return 0
    # shellcheck disable=SC2086 # deliberate word splitting: one call, many ids
    docker rm -f $ids >/dev/null
}

container_state_of() {
    docker ps -a --filter "label=${SANDBOX_ID_LABEL}=$1" --format '{{.State}}'
}

# --- 3. Refuse a dirty baseline -----------------------------------------------
#
# This runs against the development VM, so the trap below may only remove
# containers this run is responsible for. Proving the daemon held none at the
# start is what makes "everything managed at the end is ours" true.
baseline="$(managed_containers)"
if [[ -n "$baseline" ]]; then
    printf '%s\n' "$baseline" >&2
    fail "The daemon at ${DOCKER_HOST} already holds the managed containers listed above. Clear them deliberately before running a leak check: docker rm -f \$(docker ps -aq --filter label=${MANAGED_LABEL})"
fi

pass "Baseline is clean: no managed containers before the run."

# --- 4. The binary, the database, and the daemon ------------------------------
make -C "$REPO_ROOT" build >/dev/null
[[ -x "$BIN" ]] || fail "Expected ${BIN} after make build, but it is missing."

# Its own database in a temp directory, never the developer's default: this
# script kills the process holding it and inspects it behind the process's back.
STATE_DIR="$(mktemp -d)"
DB="${STATE_DIR}/validate-06.db"
DAEMON_LOG="${STATE_DIR}/serve.log"
export QUICKSPIN_SERVER="http://127.0.0.1:${SERVER_PORT}"

DAEMON_PID=""

cleanup() {
    local code=$?

    if [[ -n "$DAEMON_PID" ]]; then
        kill "$DAEMON_PID" 2>/dev/null || true
        wait "$DAEMON_PID" 2>/dev/null || true
    fi

    local survivors
    survivors="$(managed_containers || true)"
    if [[ -n "$survivors" ]]; then
        printf '\n%sLEAK%s Managed containers survived the run:\n%s\n' "$RED" "$RESET" "$survivors" >&2
        # Recorded as a failure before removal, so the next run starts clean but
        # this run stays on record as having leaked.
        code=1
        remove_managed
        printf 'Removed the survivors listed above.\n' >&2
    fi

    if (( code != 0 )) && [[ -s "$DAEMON_LOG" ]]; then
        printf -- '\n--- control plane log ---\n' >&2
        tail -n 100 "$DAEMON_LOG" >&2
    fi

    rm -rf "$STATE_DIR"
    exit "$code"
}

# INT and TERM exit rather than clean up directly, so the cleanup path is only
# ever the EXIT trap and cannot re-enter itself.
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

start_daemon() {
    local interval="$1"

    "$BIN" serve --host 127.0.0.1 --port "$SERVER_PORT" --db "$DB" \
        --reconcile-interval "$interval" --log-level debug \
        >>"$DAEMON_LOG" 2>&1 &
    DAEMON_PID=$!

    local attempt
    for ((attempt = 0; attempt < SERVER_READY_TIMEOUT; attempt++)); do
        if "$BIN" sandbox list --output json >/dev/null 2>&1; then
            return 0
        fi
        kill -0 "$DAEMON_PID" 2>/dev/null || break
        sleep 1
    done

    fail "The control plane at ${QUICKSPIN_SERVER} was not ready within ${SERVER_READY_TIMEOUT}s."
}

# kill -9, not SIGTERM: a clean shutdown is the case roadmap 06 already handles.
# The wait reaps the job so bash does not print "Killed" over the report.
crash_daemon() {
    kill -9 "$DAEMON_PID" 2>/dev/null || true
    wait "$DAEMON_PID" 2>/dev/null || true
    DAEMON_PID=""
}

# The one shutdown that is not under test: it exists only to change the
# reconcile interval between phases without staging a crash.
stop_daemon() {
    kill "$DAEMON_PID" 2>/dev/null || true
    wait "$DAEMON_PID" 2>/dev/null || true
    DAEMON_PID=""
}

# --- 5. The convergence invariant ---------------------------------------------
#
# .timeout because the daemon holds the same file: WAL lets this read proceed
# during a write, but not during the checkpoint at the end of one.
db_rows() {
    sqlite3 -cmd '.timeout 5000' "$DB" 'SELECT platform_id, state FROM sandboxes ORDER BY platform_id;' \
        | tr '|' ' '
}

db_state_of() {
    sqlite3 -cmd '.timeout 5000' "$DB" "SELECT state FROM sandboxes WHERE platform_id = '$1';"
}

db_has_row() {
    local count
    count="$(sqlite3 -cmd '.timeout 5000' "$DB" "SELECT COUNT(*) FROM sandboxes WHERE platform_id = '$1';")"
    [[ "$count" != "0" ]]
}

# converged is the whole assertion: true when the two worlds agree in both
# directions and nothing is still in flight.
converged() {
    local id state cstate

    while read -r id state; do
        [[ -z "$id" ]] && continue
        cstate="$(container_state_of "$id")"
        case "$state" in
            pending | stopping)
                # Still mid-flight; the reconciler has not finished with it.
                return 1
                ;;
            running)
                [[ "$cstate" == "running" ]] || return 1
                ;;
            stopped | failed)
                [[ -z "$cstate" ]] || return 1
                ;;
            *)
                return 1
                ;;
        esac
    done <<<"$(db_rows)"

    while read -r id; do
        [[ -z "$id" ]] && continue
        db_has_row "$id" || return 1
    done <<<"$(managed_sandbox_ids)"

    return 0
}

report_worlds() {
    printf -- '--- sandboxes (sqlite) ---\n' >&2
    db_rows >&2
    printf -- '--- managed containers (docker) ---\n' >&2
    managed_containers >&2
}

wait_converged() {
    local what="$1"
    local deadline=$((SECONDS + CONVERGE_TIMEOUT))

    while ! converged; do
        if (( SECONDS >= deadline )); then
            report_worlds
            fail "${what}: the system did not converge within ${CONVERGE_TIMEOUT}s."
        fi
        sleep 1
    done

    pass "$what"
}

wait_for_state() {
    local id="$1" want="$2" what="$3"
    local deadline=$((SECONDS + CONVERGE_TIMEOUT))

    while [[ "$(db_state_of "$id")" != "$want" ]]; do
        if (( SECONDS >= deadline )); then
            report_worlds
            fail "${what}: sandbox ${id} never reached ${want} within ${CONVERGE_TIMEOUT}s."
        fi
        sleep 1
    done
}

create_sandbox() {
    "$BIN" sandbox create "$SANDBOX_IMAGE" --output json | jq -r .sandbox_id
}

# --- 6. Warm the image cache --------------------------------------------------
#
# A cold pull inside the first reconcile pass would be charged to the
# convergence budget, turning a slow registry into a failed assertion.
printf '\nPre-pulling %s so the first reconcile pass does not pay for it.\n' "$SANDBOX_IMAGE"
if ! docker pull "$SANDBOX_IMAGE" >/dev/null; then
    printf 'Pre-pull failed; continuing — the runtime pulls for itself.\n'
fi

# --- 7. Kill point (a): row inserted, no container yet ------------------------
#
# Started on the long interval so the first tick is minutes away: the create
# returns as soon as the row is written, and the kill cannot race a pass.
start_daemon "$CRASH_WINDOW_INTERVAL"
pass "Control plane is up at ${QUICKSPIN_SERVER} (db ${DB})."

SBX_A="$(create_sandbox)"
crash_daemon

state_a="$(db_state_of "$SBX_A")"
[[ "$state_a" == "pending" ]] || fail "Kill point (a): sandbox ${SBX_A} is '${state_a}' after the crash, expected 'pending'."

if [[ -n "$(container_state_of "$SBX_A")" ]]; then
    fail "Kill point (a): a container for ${SBX_A} already existed at the crash, so a reconcile pass ran despite --reconcile-interval ${CRASH_WINDOW_INTERVAL}."
fi

pass "Kill point (a): crashed with the row pending and no container."

start_daemon "$RECONCILE_INTERVAL"
wait_converged "Kill point (a): converged after restart"

state_a="$(db_state_of "$SBX_A")"
[[ "$state_a" == "running" ]] || fail "Kill point (a): sandbox ${SBX_A} converged to '${state_a}', expected 'running' — the pending row was never created."

# --- 8. Kill point (b): container created, sandbox running --------------------
SBX_B="$(create_sandbox)"
wait_for_state "$SBX_B" running "Kill point (b)"
[[ "$(container_state_of "$SBX_B")" == "running" ]] || fail "Kill point (b): sandbox ${SBX_B} reads running but has no running container."

crash_daemon
pass "Kill point (b): crashed with ${SBX_B} running and its container up."

start_daemon "$RECONCILE_INTERVAL"
wait_converged "Kill point (b): converged after restart"

state_b="$(db_state_of "$SBX_B")"
[[ "$state_b" == "running" ]] || fail "Kill point (b): sandbox ${SBX_B} is '${state_b}' after the restart, expected it to stay 'running' — a crash must not disturb a converged sandbox."

# --- 9. Kill point (c): DELETE accepted, row stopping -------------------------
#
# Destroy is asynchronous: the request records the intent and returns, and the
# reconciler is what removes the container. The crash lands in that window, so
# the daemon is restarted on the long interval first — on the short one the
# reconciler would finish the destroy before the kill arrived.
stop_daemon
start_daemon "$CRASH_WINDOW_INTERVAL"

"$BIN" sandbox destroy "$SBX_B" >/dev/null
crash_daemon

state_b="$(db_state_of "$SBX_B")"
[[ "$state_b" == "stopping" ]] || fail "Kill point (c): sandbox ${SBX_B} is '${state_b}' after the crash, expected 'stopping'."

if [[ -z "$(container_state_of "$SBX_B")" ]]; then
    fail "Kill point (c): the container for ${SBX_B} was already gone at the crash, so a reconcile pass ran despite --reconcile-interval ${CRASH_WINDOW_INTERVAL}."
fi

pass "Kill point (c): crashed mid-destroy with the row stopping and the container still up."

start_daemon "$RECONCILE_INTERVAL"
wait_converged "Kill point (c): converged after restart"

state_b="$(db_state_of "$SBX_B")"
[[ "$state_b" == "stopped" ]] || fail "Kill point (c): sandbox ${SBX_B} is '${state_b}', expected 'stopped' — the interrupted destroy never completed."
[[ -z "$(container_state_of "$SBX_B")" ]] || fail "Kill point (c): sandbox ${SBX_B} reads stopped but its container survived."

# --- 10. The other direction: a container with no row -------------------------
#
# The id is well-formed on purpose: runtime.List skips a managed container whose
# id label does not parse, so a bad id would test the wrong thing — it would
# pass while the orphan sweep never saw the container at all.
ORPHAN_ID="sbx_$(uuidgen | tr '[:upper:]' '[:lower:]')"
docker run -d --rm=false --runtime="$QUICKSPIN_DOCKER_RUNTIME" \
    --label "${MANAGED_LABEL}" \
    --label "${SANDBOX_ID_LABEL}=${ORPHAN_ID}" \
    "$SANDBOX_IMAGE" >/dev/null

[[ -n "$(container_state_of "$ORPHAN_ID")" ]] || fail "Could not create the hand-labelled orphan container ${ORPHAN_ID}."

orphan_deadline=$((SECONDS + CONVERGE_TIMEOUT))
while [[ -n "$(container_state_of "$ORPHAN_ID")" ]]; do
    if (( SECONDS >= orphan_deadline )); then
        report_worlds
        fail "Orphan sweep: the hand-labelled container ${ORPHAN_ID} survived ${CONVERGE_TIMEOUT}s with no DB row behind it."
    fi
    sleep 1
done

pass "Orphan sweep: a managed container with no row is destroyed within one interval."

# --- 11. Zero leaks at rest ---------------------------------------------------
"$BIN" sandbox destroy "$SBX_A" >/dev/null
wait_converged "Teardown: converged after destroying every sandbox"

survivors="$(managed_containers)"
[[ -z "$survivors" ]] || {
    printf '%s\n' "$survivors" >&2
    fail "Teardown: managed containers remain after every sandbox reached a terminal state."
}

pass "Teardown: no managed containers remain."

printf '\nAll crash-convergence checks passed.\n'
