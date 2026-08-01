// The live exec and limit suite for plan 03. Like docker_live_test.go it is an
// external test package with no build tag, so it compiles during an ordinary
// `make test` and skips at run time — a tagged file would rot unnoticed.
//
// Every test here answers a question the pure tests structurally cannot: whether
// the kernel actually enforces what Spec asked for. TestSpecToHostConfigMapsEveryLimit
// proves quickspin sent the right numbers; only these prove anything received them.
package docker_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/nickstrad/quickspin/internal/runtime"
	"github.com/nickstrad/quickspin/internal/runtime/docker"
)

const (
	// Small enough that the allocator trips it in about a second, comfortably
	// above Spec's 6MiB floor.
	oomMemoryLimit = 32 * 1024 * 1024

	// Deliberately tiny: the point of the fork-bomb test is that the blast radius
	// is a failed fork, not a stressed VM. A generous limit would make the test
	// itself the denial of service it is checking for.
	forkBombPidsLimit = 64

	execTimeout = 30 * time.Second
)

// liveSpec is the shared valid baseline. Tests that are about a specific limit
// override the one field they are about, so a test never silently depends on a
// limit it did not mean to set.
func liveSpec(t *testing.T) runtime.Spec {
	t.Helper()
	return runtime.NewSpec(longRunningImage(), nil, liveCPULimit, liveMemoryLimit, livePidsLimit, false)
}

// newSandbox creates a sandbox and registers its teardown. The cleanup uses a
// fresh context rather than t.Context(): the testing package cancels that just
// before cleanups run, so a cleanup reaching for it would fail exactly when
// there was something to destroy.
func newSandbox(t *testing.T, rt *docker.Runtime, spec runtime.Spec) string {
	t.Helper()

	info, err := rt.Create(t.Context(), runtime.NewSandboxID(), spec)
	if err != nil {
		t.Fatalf("Create(%s) error = %v, want nil", spec.Image, err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), teardownTimeout)
		defer cancel()
		if err := rt.Destroy(ctx, info.ID); err != nil {
			t.Errorf("cleanup Destroy(%s) error = %v, want nil", info.ID, err)
		}
	})

	return info.ID
}

func sh(script string) []string { return []string{"sh", "-c", script} }

// --- Exec: streams, exit codes, cancellation ---------------------------------

func TestExecSeparatesStreamsAndExitCode(t *testing.T) {
	// The three things agent harnesses branch on, and the three a naive
	// implementation destroys at once: an io.Copy of the hijacked connection
	// merges the streams and embeds the 8-byte frame headers in the output, and
	// reading ExecInspect before EOF reports a meaningless zero exit code.
	rt := liveDocker(t)
	id := newSandbox(t, rt, liveSpec(t))

	result, err := rt.Exec(t.Context(), id,
		sh("echo out; echo err >&2; exit 17"),
		runtime.ExecOpts{Timeout: execTimeout})
	if err != nil {
		t.Fatalf("Exec error = %v, want nil: a non-zero exit is a result, not an error", err)
	}

	if got := string(result.Stdout); got != "out\n" {
		t.Errorf("Stdout = %q, want %q — stray bytes mean the frame headers were not demuxed", got, "out\n")
	}
	if got := string(result.Stderr); got != "err\n" {
		t.Errorf("Stderr = %q, want %q", got, "err\n")
	}
	if result.ExitCode != 17 {
		t.Errorf("ExitCode = %d, want 17", result.ExitCode)
	}
}

func TestExecKillsProcessOnContextCancel(t *testing.T) {
	// Both assertions are required and they fail independently. Closing the
	// hijacked connection unblocks the Go read for free, so a broken
	// implementation passes the first assertion while leaving a `sleep 300`
	// burning in the container — which is the bug that costs money on a real
	// platform.
	rt := liveDocker(t)
	id := newSandbox(t, rt, liveSpec(t))

	// sleep is exec'd directly rather than under `sh -c`: with a shell in front,
	// signalling the parent leaves the sleep running as an orphan, and the test
	// would be measuring the harness's process tree instead of the kill path.
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := rt.Exec(ctx, id, []string{"sleep", "300"}, runtime.ExecOpts{Timeout: execTimeout})
		done <- err
	}()

	// Long enough that the exec is genuinely attached and running; cancelling
	// before it starts would test nothing.
	time.Sleep(time.Second)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Exec error = nil, want a cancellation error")
		}
		if errors.Is(err, runtime.ErrExecTimeout) {
			t.Errorf("Exec error = %v, want a plain cancel to be distinguishable from a deadline", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Exec did not return within 30s of cancel")
	}

	// The second assertion. Polled rather than checked once, because kill-then-
	// observe races the reap and a single immediate check is flaky in the
	// direction of a false pass.
	assertEventually(t, time.Minute, func() (bool, string) {
		out := execOrFatal(t, rt, id, []string{"ps", "-eo", "args"})
		return !strings.Contains(out, "sleep 300"), out
	}, "sleep 300 still running in the container after cancel")
}

func TestExecTimeout(t *testing.T) {
	// The deadline must be distinguishable from a plain cancel via errors.Is —
	// a caller retries a timeout and does not retry a cancel.
	rt := liveDocker(t)
	id := newSandbox(t, rt, liveSpec(t))

	start := time.Now()
	_, err := rt.Exec(t.Context(), id, []string{"sleep", "300"},
		runtime.ExecOpts{Timeout: time.Second})

	if !errors.Is(err, runtime.ErrExecTimeout) {
		t.Fatalf("Exec error = %v, want errors.Is(..., ErrExecTimeout)", err)
	}
	// The deadline has to bound the call, not merely be reported after the
	// command's natural length. Generous so a loaded CI machine does not fail it.
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Errorf("Exec took %v for a 1s timeout, want the deadline to bound the call", elapsed)
	}

	// A timeout is a cancel with a different label, so it owes the same reap.
	assertEventually(t, time.Minute, func() (bool, string) {
		out := execOrFatal(t, rt, id, []string{"ps", "-eo", "args"})
		return !strings.Contains(out, "sleep 300"), out
	}, "sleep 300 survived the deadline")
}

func TestOutputTruncation(t *testing.T) {
	// Buffered output with a cap is plan 03's committed tradeoff over streaming.
	// The cap alone is not enough: silently returning a short buffer makes a
	// truncated JSON document indistinguishable from a malformed one, so the
	// flag is the actual contract being tested.
	rt := liveDocker(t)
	id := newSandbox(t, rt, liveSpec(t))

	// yes writes without bound; head bounds it well past the cap so the test does
	// not depend on the exact moment the writer stops draining.
	overflow := runtime.MaxStreamBytes * 2
	result, err := rt.Exec(t.Context(), id,
		sh(fmt.Sprintf("yes 0123456789 | head -c %d", overflow)),
		runtime.ExecOpts{Timeout: execTimeout})
	if err != nil {
		t.Fatalf("Exec error = %v, want nil: overflowing the cap is truncation, not failure", err)
	}

	if !result.StdoutTruncated {
		t.Error("StdoutTruncated = false, want true: a silent short read is the bug")
	}
	if len(result.Stdout) > runtime.MaxStreamBytes {
		t.Errorf("len(Stdout) = %d, want at most the %d-byte cap", len(result.Stdout), runtime.MaxStreamBytes)
	}
	// Truncation is per stream: a stdout that overflowed says nothing about a
	// stderr that stayed silent, and conflating them would mislead a caller
	// parsing the other stream.
	if result.StderrTruncated {
		t.Error("StderrTruncated = true, want false: only stdout overflowed")
	}
}

// --- Limits: what the kernel actually enforces -------------------------------

// TestLimitsReachTheContainersCgroupFiles is the check the plan calls for as a
// manual step. It is automated here because "read the cgroup files once by hand"
// is exactly the verification that stops happening: the invariant is that a
// limit present in Spec but absent from the container's cgroup is worse than no
// limit, since callers will trust it.
//
// The files are read on the VM host rather than inside the sandbox: under gVisor
// the sandbox sees the sentry's synthesized cgroup v1 hierarchy, so the v2 paths
// asserted on here exist only on the host.
func TestLimitsReachTheContainersCgroupFiles(t *testing.T) {
	rt := liveDocker(t)

	const (
		wantMemory = 96 * 1024 * 1024
		wantCPU    = 0.5
		wantPids   = 100
	)
	spec := runtime.NewSpec(longRunningImage(), nil, wantCPU, wantMemory, wantPids, false)
	id := newSandbox(t, rt, spec)
	cgroup := hostCgroupOf(t, id, "memory.max", "cpu.max", "pids.max")

	t.Run("memory.max", func(t *testing.T) {
		if got := cgroup["memory.max"]; got != strconv.Itoa(wantMemory) {
			t.Errorf("memory.max = %q, want %d — Memory is bytes verbatim", got, wantMemory)
		}
	})

	t.Run("cpu.max", func(t *testing.T) {
		// cpu.max is "<quota> <period>", both in microseconds — not nano-CPUs.
		// 0.5 cores at the default 100000µs period is "50000 100000". The daemon
		// derives this from NanoCPUs, so this pins the second translation hop.
		got := cgroup["cpu.max"]
		quota, period, ok := strings.Cut(got, " ")
		if !ok {
			t.Fatalf("cpu.max = %q, want \"<quota> <period>\"", got)
		}
		q, err := strconv.ParseFloat(quota, 64)
		if err != nil {
			t.Fatalf("cpu.max quota %q: %v", quota, err)
		}
		p, err := strconv.ParseFloat(period, 64)
		if err != nil {
			t.Fatalf("cpu.max period %q: %v", period, err)
		}
		if cores := q / p; cores != wantCPU {
			t.Errorf("cpu.max = %q, which is %g cores, want %g", got, cores, wantCPU)
		}
	})

	t.Run("pids.max", func(t *testing.T) {
		// "max" here is the literal string the kernel writes for unlimited — the
		// exact outcome a dropped nil *int64 produces, and the reason this
		// assertion is not just a number comparison.
		got := cgroup["pids.max"]
		if got == "max" {
			t.Fatal("pids.max = \"max\": the limit was dropped and the sandbox is unbounded")
		}
		if got != strconv.Itoa(wantPids) {
			t.Errorf("pids.max = %q, want %d", got, wantPids)
		}
	})
}

// TestSandboxesLandOnTheConfiguredRuntime closes the gap between what quickspin
// asks for and what the daemon did. Everything else in this suite passes just as
// green on runc, so without this the isolation boundary is attested only by
// hack/validate-01.sh, which runs its own `docker run --runtime=runsc` rather
// than the Create path that puts sandboxes on it.
func TestSandboxesLandOnTheConfiguredRuntime(t *testing.T) {
	rt := liveDocker(t)

	want := rt.ContainerRuntime()
	if want == "" {
		t.Skip("no OCI runtime configured, so the daemon chose: set QUICKSPIN_DOCKER_RUNTIME to assert the boundary")
	}

	id := newSandbox(t, rt, liveSpec(t))

	ctx, cancel := context.WithTimeout(t.Context(), execTimeout)
	defer cancel()

	inspected, err := liveClient.ContainerInspect(ctx, containerIDOf(t, id), client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("inspecting the container for sandbox %s: %v", id, err)
	}

	if got := inspected.Container.HostConfig.Runtime; got != want {
		t.Errorf("HostConfig.Runtime = %q, want %q: the sandbox is not on the isolation boundary quickspin asked for", got, want)
	}
}

// hostCgroupOf reads the named cgroup files for a sandbox as the VM kernel holds
// them, in one throwaway container that bind-mounts the host's /sys/fs/cgroup.
// That container deliberately runs on the daemon's default runtime: under gVisor
// it would see the sentry's synthesized hierarchy instead of the host's.
func hostCgroupOf(t *testing.T, sandboxID string, files ...string) map[string]string {
	t.Helper()

	containerID := containerIDOf(t, sandboxID)

	const mount = "/hostcgroup"
	// Searching for the container id rather than assuming systemd's
	// system.slice/docker-<id>.scope keeps the cgroup driver out of the assertion.
	script := fmt.Sprintf(
		`dir=$(find %s -maxdepth 4 -type d -name "*%s*" -print -quit); `+
			`[ -n "$dir" ] || { echo "no cgroup directory for %s" >&2; exit 1; }; `+
			`for f in %s; do printf '%%s\t%%s\n' "$f" "$(cat "$dir/$f")"; done`,
		mount, containerID, containerID, strings.Join(files, " "))

	ctx, cancel := context.WithTimeout(t.Context(), execTimeout)
	defer cancel()

	created, err := liveClient.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image: shortLivedImage,
			Cmd:   sh(script),
		},
		HostConfig: &container.HostConfig{
			// Read-only: this helper must not be able to change the limits it is
			// checking.
			Binds:      []string{"/sys/fs/cgroup:" + mount + ":ro"},
			AutoRemove: true,
		},
	})
	if err != nil {
		t.Fatalf("create cgroup reader: %v", err)
	}

	attached, err := liveClient.ContainerAttach(ctx, created.ID, client.ContainerAttachOptions{
		Stream: true, Stdout: true, Stderr: true,
	})
	if err != nil {
		t.Fatalf("attach cgroup reader: %v", err)
	}
	defer attached.Close()

	if _, err := liveClient.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start cgroup reader: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attached.Reader); err != nil {
		t.Fatalf("read cgroup reader output: %v", err)
	}

	values := make(map[string]string, len(files))
	for line := range strings.SplitSeq(strings.TrimSpace(stdout.String()), "\n") {
		if name, value, ok := strings.Cut(strings.TrimSpace(line), "\t"); ok {
			values[name] = value
		}
	}
	if len(values) != len(files) {
		t.Fatalf("reading %v for sandbox %s produced %v: %s",
			files, sandboxID, values, strings.TrimSpace(stderr.String()))
	}
	return values
}

// containerIDOf resolves the sandbox to its container through the daemon's own
// label query rather than through Runtime.List, for the same reason TestMain's
// sweep does: these tests have to keep working when the implementation under test
// is the broken thing.
func containerIDOf(t *testing.T, sandboxID string) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), execTimeout)
	defer cancel()

	result, err := liveClient.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: client.Filters{}.Add("label", sandboxIDLabel+"="+sandboxID),
	})
	if err != nil {
		t.Fatalf("listing the container for sandbox %s: %v", sandboxID, err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("found %d containers labelled %s=%s, want exactly one", len(result.Items), sandboxIDLabel, sandboxID)
	}
	return result.Items[0].ID
}

func TestMemoryLimitEnforced(t *testing.T) {
	// The kernel only OOM-kills pages that are actually written, so the payload
	// must touch what it allocates — a lazy allocation would pass under any limit.
	rt := liveDocker(t)
	spec := runtime.NewSpec(longRunningImage(), nil, liveCPULimit, oomMemoryLimit, livePidsLimit, false)
	id := newSandbox(t, rt, spec)

	result, err := rt.Exec(t.Context(), id,
		sh("s=''; while :; do s=\"$s$(head -c 1048576 /dev/zero | tr '\\0' 'x')\"; done"),
		runtime.ExecOpts{Timeout: execTimeout})
	if err != nil {
		t.Fatalf("Exec error = %v, want nil: an OOM kill is an exit code, not a transport failure", err)
	}

	// Non-zero rather than exactly 137: gVisor does not encode 128+signal through
	// the exec path, returning 128 for an OOM kill and 1 for an explicit `kill -9`
	// where runc reports 137 for both.
	if result.ExitCode == 0 {
		t.Errorf("ExitCode = 0, want non-zero: the allocation should have been killed, not completed")
	}

	// The sandbox survives its own OOM kill. cgroup v2 kills inside the cgroup;
	// if the whole container died, the limit was not scoped where it should be.
	if info, err := rt.Inspect(t.Context(), id); err != nil {
		t.Errorf("Inspect after OOM error = %v, want the sandbox to survive", err)
	} else if info.State != runtime.StateRunning {
		t.Errorf("state after OOM = %q, want %q", info.State, runtime.StateRunning)
	}
}

// TestPidsLimitStopsForkBomb asserts containment, not the sandbox's survival:
// runc contains the bomb with EAGAIN and nothing dies, while under gVisor each
// guest task needs a host stub process, so the same limit kills the sentry and
// the sandbox with it. Only the blast radius is common to both.
func TestPidsLimitStopsForkBomb(t *testing.T) {
	rt := liveDocker(t)
	spec := runtime.NewSpec(longRunningImage(), nil, liveCPULimit, liveMemoryLimit, forkBombPidsLimit, false)
	id := newSandbox(t, rt, spec)

	// Read host-side and before the bomb: the limit has to be in force at the
	// moment it is tested, and under gVisor the sandbox may not be around
	// afterwards to be asked.
	if got := hostCgroupOf(t, id, "pids.max")["pids.max"]; got != strconv.Itoa(forkBombPidsLimit) {
		t.Fatalf("pids.max = %q before the bomb, want %d: nothing would be under test", got, forkBombPidsLimit)
	}

	// Bounded rather than a true `:(){ :|:& };:`: an unbounded bomb would still be
	// spawning against the limit when the test moves on.
	_, err := rt.Exec(t.Context(), id,
		sh("i=0; while [ $i -lt 500 ]; do sleep 30 & i=$((i+1)); done; wait"),
		runtime.ExecOpts{Timeout: 10 * time.Second})
	// A sandbox that dies under its own bomb takes its exec down with it, which
	// is a legitimate outcome here rather than a transport failure.
	if err != nil && !errors.Is(err, runtime.ErrExecTimeout) {
		t.Logf("exec ended with %v (the sandbox may have died under its own bomb)", err)
	}

	// The host is what must survive, and the check is the thing a bomb that
	// escaped its cgroup would have taken away: starting and running a process.
	survivor := newSandbox(t, rt, liveSpec(t))
	if out := execOrFatal(t, rt, survivor, sh("echo alive")); strings.TrimSpace(out) != "alive" {
		t.Errorf("post-fork-bomb echo in a fresh sandbox = %q, want %q", out, "alive")
	}
}

func TestNetworkDenied(t *testing.T) {
	// AllowNetwork false means no egress. This plan implements it with Docker's
	// `none` network, which happens to deliver no interface either — but the
	// contract being pinned is the egress failure, because plan 21's Kata pods
	// always get a pod IP and must satisfy the same clause with a filter.
	rt := liveDocker(t)
	denied := newSandbox(t, rt, liveSpec(t)) // AllowNetwork defaults to false

	// A raw IP, so a DNS failure cannot be mistaken for a blocked connection —
	// they are different failures and only one of them is the subject here.
	result, err := rt.Exec(t.Context(), denied,
		sh("wget -q -T 5 -O - http://1.1.1.1 >/dev/null 2>&1; echo $?"),
		runtime.ExecOpts{Timeout: execTimeout})
	if err != nil {
		t.Fatalf("Exec error = %v, want nil", err)
	}
	if got := strings.TrimSpace(string(result.Stdout)); got == "0" {
		t.Error("outbound request succeeded with AllowNetwork false, want it to fail")
	}
}

func TestNetworkAllowedWhenRequested(t *testing.T) {
	// The negative test above passes trivially if egress is broken for every
	// sandbox — a `none` network hard-coded regardless of the flag would satisfy
	// it. This is the control that makes TestNetworkDenied mean something.
	rt := liveDocker(t)
	spec := runtime.NewSpec(longRunningImage(), nil, liveCPULimit, liveMemoryLimit, livePidsLimit, true)
	allowed := newSandbox(t, rt, spec)

	result, err := rt.Exec(t.Context(), allowed,
		sh("wget -q -T 10 -O - http://1.1.1.1 >/dev/null 2>&1; echo $?"),
		runtime.ExecOpts{Timeout: execTimeout})
	if err != nil {
		t.Fatalf("Exec error = %v, want nil", err)
	}
	if got := strings.TrimSpace(string(result.Stdout)); got != "0" {
		t.Skipf("outbound request from an AllowNetwork sandbox exited %s; "+
			"skipping rather than failing because this asserts the environment has egress, not quickspin's behavior", got)
	}
}

// --- helpers -----------------------------------------------------------------

// execOrFatal runs a command whose failure means the test cannot proceed, as
// distinct from a command whose exit code is the thing under test.
func execOrFatal(t *testing.T, rt *docker.Runtime, id string, cmd []string) string {
	t.Helper()

	result, err := rt.Exec(t.Context(), id, cmd, runtime.ExecOpts{Timeout: execTimeout})
	if err != nil {
		t.Fatalf("Exec(%v) error = %v, want nil", cmd, err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("Exec(%v) exited %d, stderr = %q", cmd, result.ExitCode, result.Stderr)
	}
	return string(result.Stdout)
}

// assertEventually polls a condition that races a reap. A single immediate check
// is flaky in the direction of a false pass — it can observe the moment before
// the kernel reaps a process that was in fact killed.
func assertEventually(
	t *testing.T,
	limit time.Duration,
	cond func() (bool, string),
	msg string,
) {
	t.Helper()

	deadline := time.Now().Add(limit)
	var last string
	for time.Now().Before(deadline) {
		ok, observed := cond()
		if ok {
			return
		}
		last = observed
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("%s (waited %v); last observation:\n%s", msg, limit, last)
}
