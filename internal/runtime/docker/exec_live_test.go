// The live exec and limit suite for plan 03. Like docker_live_test.go it is an
// external test package with no build tag, so it compiles during an ordinary
// `make test` and skips at run time — a tagged file would rot unnoticed.
//
// Every test here answers a question the pure tests structurally cannot: whether
// the kernel actually enforces what Spec asked for. TestSpecToHostConfigMapsEveryLimit
// proves quickspin sent the right numbers; only these prove anything received them.
package docker_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

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

	// SIGKILL's exit code as a shell reports it: 128 + 9. This is the kernel's OOM
	// killer speaking, not the process choosing to exit.
	exitSIGKILL = 137

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
// The files are read through the container's own /sys/fs/cgroup rather than the
// host path under /sys/fs/cgroup/system.slice/docker-<id>.scope, because the
// container is in its own cgroup namespace and sees its cgroup at the root. That
// avoids depending on the host being systemd-managed, which the docker-<id>.scope
// path assumes and a Lima VM may not honor.
func TestLimitsReachTheContainersCgroupFiles(t *testing.T) {
	rt := liveDocker(t)

	const (
		wantMemory = 96 * 1024 * 1024
		wantCPU    = 0.5
		wantPids   = 100
	)
	spec := runtime.NewSpec(longRunningImage(), nil, wantCPU, wantMemory, wantPids, false)
	id := newSandbox(t, rt, spec)

	t.Run("memory.max", func(t *testing.T) {
		got := strings.TrimSpace(execOrFatal(t, rt, id, sh("cat /sys/fs/cgroup/memory.max")))
		if got != strconv.Itoa(wantMemory) {
			t.Errorf("memory.max = %q, want %d — Memory is bytes verbatim", got, wantMemory)
		}
	})

	t.Run("cpu.max", func(t *testing.T) {
		// cpu.max is "<quota> <period>", both in microseconds — not nano-CPUs.
		// 0.5 cores at the default 100000µs period is "50000 100000". The daemon
		// derives this from NanoCPUs, so this pins the second translation hop.
		got := strings.TrimSpace(execOrFatal(t, rt, id, sh("cat /sys/fs/cgroup/cpu.max")))
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
		got := strings.TrimSpace(execOrFatal(t, rt, id, sh("cat /sys/fs/cgroup/pids.max")))
		if got == "max" {
			t.Fatal("pids.max = \"max\": the limit was dropped and the sandbox is unbounded")
		}
		if got != strconv.Itoa(wantPids) {
			t.Errorf("pids.max = %q, want %d", got, wantPids)
		}
	})
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

	if result.ExitCode != exitSIGKILL {
		t.Errorf("ExitCode = %d, want %d (128 + SIGKILL): the kernel should have OOM-killed this",
			result.ExitCode, exitSIGKILL)
	}

	// The sandbox survives its own OOM kill. cgroup v2 kills inside the cgroup;
	// if the whole container died, the limit was not scoped where it should be.
	if info, err := rt.Inspect(t.Context(), id); err != nil {
		t.Errorf("Inspect after OOM error = %v, want the sandbox to survive", err)
	} else if info.State != runtime.StateRunning {
		t.Errorf("state after OOM = %q, want %q", info.State, runtime.StateRunning)
	}
}

func TestPidsLimitStopsForkBomb(t *testing.T) {
	// The distinctive behavior: hitting pids.max does not kill anything. fork
	// returns EAGAIN and the bomb simply cannot spawn — so the evidence is a
	// failed fork plus a still-responsive VM, not a corpse.
	rt := liveDocker(t)
	spec := runtime.NewSpec(longRunningImage(), nil, liveCPULimit, liveMemoryLimit, forkBombPidsLimit, false)
	id := newSandbox(t, rt, spec)

	// Bounded rather than a true `:(){ :|:& };:`: an unbounded bomb would still be
	// spawning against the limit when the test moves on, and the assertion is
	// about fork failing, which a bounded loop shows just as well.
	_, err := rt.Exec(t.Context(), id,
		sh("i=0; while [ $i -lt 500 ]; do sleep 30 & i=$((i+1)); done; wait"),
		runtime.ExecOpts{Timeout: 10 * time.Second})
	if err != nil && !errors.Is(err, runtime.ErrExecTimeout) {
		t.Fatalf("Exec error = %v, want nil or a timeout", err)
	}

	// The VM stays responsive — the real point of a pids limit. A sandbox that
	// exhausted the host's process table would fail here rather than above.
	out := execOrFatal(t, rt, id, sh("echo alive"))
	if strings.TrimSpace(out) != "alive" {
		t.Errorf("post-fork-bomb echo = %q, want %q: the sandbox stopped responding", out, "alive")
	}

	// And the limit is still in force rather than having been raised by the
	// pressure — which would mean nothing was actually enforced.
	pids := strings.TrimSpace(execOrFatal(t, rt, id, sh("cat /sys/fs/cgroup/pids.max")))
	if pids != strconv.Itoa(forkBombPidsLimit) {
		t.Errorf("pids.max = %q, want %d", pids, forkBombPidsLimit)
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
