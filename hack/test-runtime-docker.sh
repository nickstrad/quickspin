#!/usr/bin/env bash
#
# Runs the live Docker runtime suite and the CLI smoke against a dedicated,
# test-owned Lima VM.
#
# The VM stays running between invocations: the feedback loop matters more than
# reproducibility on a machine that isn't yours, and the dirty-baseline refusal
# below carries the leak signal either way. A fresh disposable VM per run is
# deferred until CI exists — see docs/reference/runtime-backend-testing.mdx.
#
# This script never switches the developer's Docker context. It exports
# DOCKER_HOST for its own children, so connection configuration lives in the test
# processes rather than in developer-wide mutable state.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=hack/common.sh
source "${REPO_ROOT}/hack/common.sh"

TEST_VM_NAME="${TEST_VM_NAME:-quickspin-runtime-test}"
TEMPLATE="${REPO_ROOT}/lima/quickspin.yaml"
# Spelled here because shell cannot import internal/runtime/labels.go. A rename
# there does not break this check — it makes it pass while finding nothing, which
# is the worst failure mode a leak check has, so labels.go says the names are a
# wire format.
MANAGED_LABEL="quickspin.managed=true"
SANDBOX_ID_LABEL="quickspin.id"
DAEMON_READY_TIMEOUT="${DAEMON_READY_TIMEOUT:-90}"

# Sized here rather than in lima/quickspin.yaml, which the development VM also
# uses: this VM runs one nginx container at a time, and Lima's defaults for an
# unconfigured template are 4 CPUs / 4GiB / a 100GiB disk. The disk is sparse, so
# the ceiling costs nothing until it is used — it is a bound on a runaway pull,
# not an allocation. Below roughly 8GiB the Ubuntu rootfs plus Docker leaves no
# room for an image at all.
TEST_VM_CPUS="${TEST_VM_CPUS:-2}"
TEST_VM_MEMORY="${TEST_VM_MEMORY:-2}"
TEST_VM_DISK="${TEST_VM_DISK:-20}"

# The one place the fixture image is pinned. Exported so the live Go suite and the
# CLI smoke cannot drift onto different images; each keeps its own default only
# for a standalone run. A default process that stays up is the requirement — see
# internal/runtime/docker_live_test.go.
export QUICKSPIN_TEST_IMAGE="${QUICKSPIN_TEST_IMAGE:-docker.io/library/nginx:1.27-alpine}"
export QUICKSPIN_SMOKE_IMAGE="$QUICKSPIN_TEST_IMAGE"

# MODE selects between the full run and the two maintenance paths. CLEAN_ONLY is
# the one path allowed to remove managed containers without failing the run.
MODE="run"
[[ "${CLEAN_ONLY:-0}" == "1" ]] && MODE="clean"
[[ "${SETUP_ONLY:-0}" == "1" ]] && MODE="setup"
[[ "${TEARDOWN_ONLY:-0}" == "1" ]] && MODE="teardown"

require_command limactl
require_command docker
require_command go

# --- 1. The VM is not the development VM --------------------------------------
#
# Every mode below either sweeps Quickspin containers or deletes the instance
# outright, so the first check in all of them is that none can be aimed at the VM
# the developer works in.
if [[ "$TEST_VM_NAME" == "${VM_NAME:-quickspin}" ]]; then
    fail "TEST_VM_NAME is '${TEST_VM_NAME}', the same as VM_NAME. This script sweeps Quickspin containers and deletes instances, and must never do either to the development VM."
fi

# --- 2. The VM exists, is running, and is ours --------------------------------
#
# One `limactl list` for both fields: each invocation spawns a process that stats
# the instance directory. It prints nothing (exit 0) for an unknown instance, so
# the empty case is separate from the stopped case.
# `|| true` on the read, not just on limactl: read reports EOF as a nonzero exit,
# and an unknown instance produces no output at all, which set -e would otherwise
# treat as a fatal error instead of the "no such VM" case handled just below.
vm_status=""
vm_dir=""
IFS=$'\t' read -r vm_status vm_dir < <(
    limactl list "$TEST_VM_NAME" --format '{{.Status}}{{"\t"}}{{.Dir}}' 2>/dev/null || true
) || true

# A missing instance means there is nothing to clean or tear down. Provisioning
# one just to report it empty would make a mop that creates infrastructure.
if [[ -z "${vm_status:-}" && ( "$MODE" == "clean" || "$MODE" == "teardown" ) ]]; then
    pass "No Lima instance '${TEST_VM_NAME}'; nothing to do."
    exit 0
fi

created_vm=0
case "${vm_status:-}" in
    "")
        printf 'Creating the dedicated test VM %s from %s (%s CPUs, %sGiB memory, %sGiB disk). The first boot pulls an image and installs Docker.\n' \
            "$TEST_VM_NAME" "$TEMPLATE" "$TEST_VM_CPUS" "$TEST_VM_MEMORY" "$TEST_VM_DISK"
        # The sizes apply at creation only; limactl ignores them when starting an
        # instance that already exists, and Lima cannot shrink a disk afterwards.
        limactl start "$TEMPLATE" --name="$TEST_VM_NAME" --tty=false \
            --cpus="$TEST_VM_CPUS" --memory="$TEST_VM_MEMORY" --disk="$TEST_VM_DISK"
        created_vm=1
        vm_dir="$(limactl list "$TEST_VM_NAME" --format '{{.Dir}}')"
        ;;
    Running) ;;
    *)
        printf 'The test VM %s is %s.\n' "$TEST_VM_NAME" "$vm_status"
        # Teardown is about to delete the instance, so booting it first pays a
        # full boot for something that will not exist afterwards. Every other
        # mode needs the daemon inside it.
        [[ "$MODE" == "teardown" ]] || limactl start "$TEST_VM_NAME" --tty=false
        ;;
esac

[[ -n "$vm_dir" ]] || fail "Could not determine the Lima directory for ${TEST_VM_NAME}."

# The ownership proof is a marker this script wrote when it created the instance,
# not the instance's name. A name prefix is a guess; a file this script placed is
# knowledge, and only knowledge should authorize a sweep.
OWNER_MARKER="${vm_dir}/quickspin-runtime-test.owned"

if (( created_vm )); then
    printf 'created by hack/test-runtime-docker.sh\n' >"$OWNER_MARKER"
fi

if [[ ! -f "$OWNER_MARKER" ]]; then
    # Not "remove it with make test-docker-teardown": teardown is dispatched
    # below this check, so that advice would loop back to this same message with
    # no way out through the harness. The escape has to be a command that does
    # not consult the marker, which means limactl directly.
    fail "The Lima instance '${TEST_VM_NAME}' exists but carries no ownership marker (${OWNER_MARKER}), so this script did not create it and will not sweep containers in it. If it is genuinely disposable, delete it yourself with 'limactl delete --force ${TEST_VM_NAME}' and let the harness create its own."
fi

pass "Dedicated test VM '${TEST_VM_NAME}' is test-owned."

# --- 2a. Setup and teardown ----------------------------------------------------
#
# Both live here rather than in the Makefile so the ownership marker has exactly
# one writer and the guards above cover the only operation that destroys an
# instance. A recipe that deleted the VM itself would be the one path with no
# ownership proof at all.
if [[ "$MODE" == "setup" ]]; then
    pass "Ready. Run 'make test-docker' to use it."
    exit 0
fi

if [[ "$MODE" == "teardown" ]]; then
    # Only a running instance needs stopping, and teardown no longer starts one:
    # an unconditional stop would spawn limactl to print an error on the common
    # path.
    if [[ "$vm_status" == "Running" ]]; then
        limactl stop "$TEST_VM_NAME" || true
    fi
    limactl delete "$TEST_VM_NAME"
    pass "Deleted the Lima instance '${TEST_VM_NAME}'."
    exit 0
fi

# --- 3. The forwarded socket ---------------------------------------------------
export DOCKER_HOST="unix://${vm_dir}/sock/docker.sock"

deadline=$((SECONDS + DAEMON_READY_TIMEOUT))
until docker version --format '{{.Server.Os}}' >/dev/null 2>&1; do
    (( SECONDS < deadline )) || fail "The Docker daemon at ${DOCKER_HOST} was not ready within ${DAEMON_READY_TIMEOUT}s."
    sleep 2
done

pass "Docker daemon reachable at ${DOCKER_HOST}."

# The backend-native ownership queries. They must not go through quickspin,
# because quickspin is the thing under test: a container whose id label is
# malformed is invisible to `quickspin sandbox list` and still very much a leak.
#
# managed_ids is separate from managed_containers so removal never has to parse
# the human-readable report back apart — otherwise the display columns become
# load-bearing and reformatting the report breaks cleanup.
managed_ids() {
    docker ps -aq --filter "label=${MANAGED_LABEL}"
}

managed_containers() {
    docker ps -a --filter "label=${MANAGED_LABEL}" \
        --format "{{.ID}}  {{.State}}  {{.Label \"${SANDBOX_ID_LABEL}\"}}"
}

# One docker invocation for all survivors: each is a process spawn plus a round
# trip over the forwarded socket, and the mop is exactly the case where the list
# can be long.
remove_managed() {
    local ids
    ids="$(managed_ids)"
    [[ -z "$ids" ]] && return 0
    # shellcheck disable=SC2086 # deliberate word splitting: one call, many ids
    docker rm -f $ids >/dev/null
}

# --- 3a. The explicit mop -----------------------------------------------------
if [[ "$MODE" == "clean" ]]; then
    survivors="$(managed_containers)"
    if [[ -z "$survivors" ]]; then
        pass "No managed containers to remove."
        exit 0
    fi
    printf 'Removing managed containers:\n%s\n' "$survivors"
    remove_managed
    pass "Removed them. The VM '${TEST_VM_NAME}' is left running."
    exit 0
fi

# --- 4. Refuse a dirty baseline ----------------------------------------------
#
# Dirt on a test-owned daemon can only mean a previous run leaked. Auto-cleaning
# would normalize away the one signal this harness exists to produce.
#
# The live Go suite's TestMain refuses the same way, which is what makes a bare
# `go test` honest. This copy is not redundant: it is what licenses the trap below
# to sweep. Anything present at the end of a run that started clean is
# definitionally new, so the trap can remove it without destroying evidence.
baseline="$(managed_containers)"
if [[ -n "$baseline" ]]; then
    printf '%s\n' "$baseline" >&2
    fail "The test daemon already holds the managed containers listed above. A previous run leaked; clear them deliberately with 'make test-docker-clean'."
fi

pass "Baseline is clean: no managed containers before the run."

# --- 5. The leak check --------------------------------------------------------
#
# Runs on success, on ordinary failure, and on interrupt. The VM is always left
# running: deleting it would make a product leak indistinguishable from
# successful infrastructure teardown, besides costing the next loop a full boot.
leak_check() {
    local code=$?
    local survivors
    survivors="$(managed_containers || true)"

    if [[ -n "$survivors" ]]; then
        printf '\n%sLEAK%s Managed containers survived the run:\n%s\n' "$RED" "$RESET" "$survivors" >&2
        # Recorded as a failure before removal, so the next run starts clean but
        # this run is still on record as having leaked.
        code=1
        remove_managed
        printf 'Removed the survivors listed above.\n' >&2
    fi

    if (( code == 0 )); then
        printf '\nAll live checks passed. The VM %s is left running for the next loop.\n' "$TEST_VM_NAME"
    else
        printf '\n%sFailed%s (exit %d). The VM %s is left running for inspection.\n' \
            "$RED" "$RESET" "$code" "$TEST_VM_NAME" >&2
    fi

    exit "$code"
}

# INT and TERM exit rather than clean up directly, so the cleanup path is only
# ever the EXIT trap and cannot re-enter itself.
trap leak_check EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# --- 6. Warm the image cache --------------------------------------------------
#
# The first Create pays for a cold pull, and it pays for it inside a test whose
# only visible output is `=== RUN`. Pulling here puts the longest silent stretch
# of the run behind Docker's own progress bars instead. It is a warm-up, not a
# precondition: the runtime pulls for itself, so a failure here is reported and
# stepped over rather than aborting a suite that would have worked.
printf '\nPre-pulling %s so the first Create does not stall silently.\n' "${QUICKSPIN_TEST_IMAGE}"
if ! docker pull "${QUICKSPIN_TEST_IMAGE}"; then
	printf 'Pre-pull failed; continuing — the runtime pulls for itself.\n'
fi

# --- 7. The live Go suite -----------------------------------------------------
#
# -count=1 because Go's test cache keys on the test binary and its inputs, and
# cannot know the daemon's state changed between runs.
#
# -timeout raises Go's 10-minute default. Several clauses are deliberately slow —
# a 3-minute convergence budget, an OOM that must actually be provoked, and reap
# polls generous enough that a loaded machine does not fail a passing
# implementation. Blowing the default produces a goroutine dump that looks like a
# crash rather than the "this suite is just long" that it is.
#
# -v plus one invocation per package is what makes the run legible. -v alone does
# not stream: given a pattern matching several packages, go test buffers each
# package's output and prints it only on completion, so a long suite shows
# nothing until it is over — indistinguishable from a wedged run. go test streams
# line by line only when handed exactly one package, which is what the loop does.
#
# `go list` rather than a hard-coded list: a package added under internal/runtime
# should be run, and a loop that silently skipped it would be worse than none.
LIVE_TEST_TIMEOUT="${LIVE_TEST_TIMEOUT:-30m}"
printf '\nRunning the live Docker runtime suite (timeout %s).\n' "${LIVE_TEST_TIMEOUT}"

for pkg in $(go list ./internal/runtime/...); do
	printf '\n--- %s\n' "${pkg}"
	QUICKSPIN_TEST_DOCKER=1 go test -count=1 -v -timeout "${LIVE_TEST_TIMEOUT}" "${pkg}"
done

# --- 8. The CLI smoke ---------------------------------------------------------
printf '\nRunning the compiled-CLI lifecycle smoke.\n'
"${REPO_ROOT}/hack/validate-runtime-cli.sh"
