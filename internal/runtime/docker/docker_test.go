package docker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/nickstrad/quickspin/internal/runtime"
)

// The sandbox ids live in labels_test.go; these are what the fake daemon
// answers with.
const (
	testContainerID = "container-good"
	testImage       = "alpine:3.20"

	// Limits chosen so each one's translation is distinguishable in an assertion:
	// a half core exercises the fractional half of the NanoCPUs conversion, and no
	// two values share a magnitude.
	testCPULimit    = 0.5
	testMemoryLimit = 64 * 1024 * 1024
	testPidsLimit   = 128
)

// testSpec is the valid baseline every Docker test that is not about validation
// starts from, so adding a required Spec field breaks one line rather than ten.
func testSpec(image string, env map[string]string) runtime.Spec {
	return runtime.NewSpec(image, env, testCPULimit, testMemoryLimit, testPidsLimit, false)
}

func TestNewRequiresLogger(t *testing.T) {
	_, err := New(&client.Client{}, nil)
	if err == nil || !strings.Contains(err.Error(), "logger is required") {
		t.Fatalf("New error = %v, want required logger error", err)
	}
}

// --- Pure decisions ----------------------------------------------------------

func TestNewContainerConfigsCarriesEveryQuickspinDecision(t *testing.T) {
	spec := testSpec(testImage, map[string]string{"FOO_A": "3", "FOO": "1", "FOO2": "2"})

	cfg, host, err := newContainerConfigs(spec, testSandboxID)
	if err != nil {
		t.Fatalf("newContainerConfigs error = %v, want nil", err)
	}

	if cfg.Image != testImage {
		t.Errorf("Image = %q, want %q", cfg.Image, testImage)
	}
	wantEntrypoint := []string{"sleep", "infinity"}
	if !slices.Equal(cfg.Entrypoint, wantEntrypoint) {
		t.Errorf("Entrypoint = %v, want %v", cfg.Entrypoint, wantEntrypoint)
	}
	// Docker clears the image's command when Entrypoint is overridden and Cmd is empty.
	if len(cfg.Cmd) != 0 {
		t.Errorf("Cmd = %v, want empty", cfg.Cmd)
	}
	// Sorting itself is envToArgs' contract, pinned by TestEnvToArgs. What this
	// asserts is that the config carries the sorted form rather than map order.
	wantEnv := []string{"FOO=1", "FOO2=2", "FOO_A=3"}
	if !slices.Equal(cfg.Env, wantEnv) {
		t.Errorf("Env = %v, want %v", cfg.Env, wantEnv)
	}
	if cfg.Labels[labelSandboxID] != testSandboxID || cfg.Labels[labelManaged] != labelManagedValue {
		t.Errorf("Labels = %v, want both the id and the managed marker", cfg.Labels)
	}
	if host.RestartPolicy.Name != container.RestartPolicyAlways {
		t.Errorf("RestartPolicy = %q, want %q", host.RestartPolicy.Name, container.RestartPolicyAlways)
	}
	if !host.PublishAllPorts {
		t.Error("PublishAllPorts = false, want true")
	}
}

// TestSpecToHostConfigMapsEveryLimit is the units test for the three
// cgroup v2 knobs. Each field lands in a different unit — Memory is bytes,
// NanoCPUs is cores × 10⁹, PidsLimit is a plain count — and a mistake produces a
// wrong kernel ceiling rather than an error, so nothing downstream catches it.
func TestSpecToHostConfigMapsEveryLimit(t *testing.T) {
	_, host, err := newContainerConfigs(testSpec(testImage, nil), testSandboxID)
	if err != nil {
		t.Fatalf("newContainerConfigs error = %v, want nil", err)
	}

	if host.Memory != testMemoryLimit {
		t.Errorf("Memory = %d, want %d bytes verbatim", host.Memory, int64(testMemoryLimit))
	}
	// 0.5 cores is 500_000_000 nano-CPUs. Writing the expectation as a literal
	// rather than as the same multiplication the code performs is the point: a
	// computed want would agree with a wrong conversion.
	if want := int64(500_000_000); host.NanoCPUs != want {
		t.Errorf("NanoCPUs = %d, want %d for %g cores", host.NanoCPUs, want, testCPULimit)
	}
	// A nil PidsLimit is the daemon's "set no pids.max", which is how a dropped
	// field becomes an unbounded fork bomb rather than a visible failure.
	if host.PidsLimit == nil {
		t.Fatal("PidsLimit = nil, want a pointer to the limit: nil means unlimited")
	}
	if *host.PidsLimit != testPidsLimit {
		t.Errorf("PidsLimit = %d, want the plain count %d", *host.PidsLimit, int64(testPidsLimit))
	}
}

func TestNewContainerConfigsMapsAllowNetworkToNetworkMode(t *testing.T) {
	// Default-deny: a spec that never mentions the network gets none, rather than
	// inheriting Docker's default bridge.
	for _, tt := range []struct {
		allow bool
		want  container.NetworkMode
	}{
		{allow: false, want: "none"},
		{allow: true, want: "bridge"},
	} {
		spec := runtime.NewSpec(testImage, nil, testCPULimit, testMemoryLimit, testPidsLimit, tt.allow)

		_, host, err := newContainerConfigs(spec, testSandboxID)
		if err != nil {
			t.Fatalf("newContainerConfigs(AllowNetwork=%v) error = %v, want nil", tt.allow, err)
		}
		if host.NetworkMode != tt.want {
			t.Errorf("NetworkMode = %q for AllowNetwork=%v, want %q", host.NetworkMode, tt.allow, tt.want)
		}
	}
}

func TestNewContainerConfigsRefusesAnInvalidSpec(t *testing.T) {
	// newContainerConfigs is the last place a Spec can be rejected before its
	// limits become kernel state, so it must not hand back a usable config for a
	// spec Validate rejects.
	spec := runtime.NewSpec(testImage, nil, testCPULimit, testMemoryLimit, 0, false)

	cfg, host, err := newContainerConfigs(spec, testSandboxID)
	if !errors.Is(err, runtime.ErrInvalidSpec) {
		t.Fatalf("newContainerConfigs error = %v, want errors.Is(..., ErrInvalidSpec)", err)
	}
	if cfg.Image != "" || host.PidsLimit != nil {
		t.Errorf("configs = %+v / %+v, want both zero on a rejected spec", cfg, host)
	}
}

func TestLabelFilterUsesLabelAsTheTermAndKeyValueAsTheValue(t *testing.T) {
	// The daemon's filter name is the literal "label"; a label key is not itself
	// a filter name, and passing one matches nothing rather than erroring.
	got := labelFilter(managedLabels(testSandboxID))

	want := client.Filters{"label": map[string]bool{
		labelSandboxID + "=" + testSandboxID:   true,
		labelManaged + "=" + labelManagedValue: true,
	}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("labelFilter = %v, want %v", got, want)
	}
}

func TestClassifyNotFoundSubstitutesTheSentinelAndKeepsTheCauseText(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantErr     error
		wantMessage string
	}{
		{
			name:        "not found becomes the sentinel",
			err:         fmt.Errorf("no such image: %w", errdefs.ErrNotFound),
			wantErr:     runtime.ErrImageMissing,
			wantMessage: "no such image",
		},
		{
			name:    "anything else keeps its cause",
			err:     errdefs.ErrUnavailable,
			wantErr: errdefs.ErrUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := classifyNotFound("op", "pulling", runtime.ErrImageMissing, tt.err)

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("classifyNotFound = %v, want errors.Is(..., %v)", err, tt.wantErr)
			}
			// The sentinel replaces the cause in the chain, so the daemon's own
			// wording has to survive somewhere or the diagnosis is lost.
			if tt.wantMessage != "" && !strings.Contains(err.Error(), tt.wantMessage) {
				t.Errorf("classifyNotFound = %q, want it to retain %q", err, tt.wantMessage)
			}
		})
	}
}

func TestClassifyExecDoneDistinguishesADeadlineFromACancel(t *testing.T) {
	// A caller retries a timeout and does not retry a cancel, so collapsing the
	// two is a behavior bug even though both stop the same exec.
	tests := []struct {
		name      string
		parentErr error
		derived   error
		wantErr   error
		wantMsg   string
	}{
		{
			// The case the previous shape could not produce. opts.Timeout expiring
			// leaves the parent untouched, so a check that looked for
			// DeadlineExceeded on the parent found nothing and fell through to the
			// cancel branch — making ErrExecTimeout unreachable.
			name:      "the exec timeout expired",
			parentErr: nil,
			derived:   context.DeadlineExceeded,
			wantErr:   runtime.ErrExecTimeout,
			wantMsg:   "timed out",
		},
		{
			// Deriving the timeout context means a parent cancel finishes both. The
			// parent is consulted first so this reports what actually happened.
			name:      "the caller cancelled",
			parentErr: context.Canceled,
			derived:   context.Canceled,
			wantErr:   context.Canceled,
			wantMsg:   "cancelled",
		},
		{
			// A deadline on the caller's own context is theirs, not opts.Timeout's,
			// so it surfaces as itself. Challenge this if ErrExecTimeout should mean
			// "any deadline" rather than "the one Exec applied".
			name:      "the caller's own deadline passed",
			parentErr: context.DeadlineExceeded,
			derived:   context.DeadlineExceeded,
			wantErr:   context.DeadlineExceeded,
			wantMsg:   "cancelled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := classifyExecDone("op", "timed out", "cancelled", tt.parentErr, tt.derived)

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("classifyExecDone = %v, want errors.Is(..., %v)", err, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("classifyExecDone = %q, want it to contain %q", err, tt.wantMsg)
			}
		})
	}
}

// TestClassifyExecDoneAgainstRealContexts pairs the classifier with the context
// derivation it actually receives. The table above pins the decision given two
// errors; this pins that context.WithTimeout produces those errors — the half
// that was wrong, and that no table of hand-written arguments can catch.
func TestClassifyExecDoneAgainstRealContexts(t *testing.T) {
	t.Run("a timeout on a healthy parent is ErrExecTimeout", func(t *testing.T) {
		parent := t.Context()
		derived, cancel := context.WithTimeout(parent, time.Nanosecond)
		defer cancel()
		<-derived.Done()

		err := classifyExecDone("op", "timed out", "cancelled", parent.Err(), derived.Err())
		if !errors.Is(err, runtime.ErrExecTimeout) {
			t.Fatalf("classifyExecDone = %v, want errors.Is(..., ErrExecTimeout)", err)
		}
	})

	t.Run("a cancelled parent is not a timeout", func(t *testing.T) {
		parent, cancelParent := context.WithCancel(t.Context())
		// A generous timeout that cannot have fired: the derived context is Done
		// only because cancellation propagates, which is exactly the ambiguity the
		// parent-first ordering resolves.
		derived, cancel := context.WithTimeout(parent, time.Hour)
		defer cancel()
		cancelParent()
		<-derived.Done()

		err := classifyExecDone("op", "timed out", "cancelled", parent.Err(), derived.Err())
		if errors.Is(err, runtime.ErrExecTimeout) {
			t.Fatalf("classifyExecDone = %v, want a cancel, not a deadline", err)
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("classifyExecDone = %v, want errors.Is(..., context.Canceled)", err)
		}
	})
}

func TestPkillCommandBuildsOnePatternArgument(t *testing.T) {
	tests := []struct {
		name string
		cmd  []string
		want []string
	}{
		{
			// The bug this pins: splicing argv in produced `pkill -f sh -c sleep 300`,
			// which pkill rejects as extra operands, so nothing was ever killed.
			name: "a multi-word command is one joined pattern",
			cmd:  []string{"sh", "-c", "sleep 300"},
			// `-` is not a regexp metacharacter outside a character class, so
			// QuoteMeta leaves it alone; the `--` separator is what keeps a flag-like
			// pattern from being read as a pkill option.
			want: []string{"pkill", "-f", "-x", "--", "sh -c sleep 300"},
		},
		{
			name: "a single-word command",
			cmd:  []string{"sleep"},
			want: []string{"pkill", "-f", "-x", "--", "sleep"},
		},
		{
			// Unescaped, `.` and `[0-9]` are regexp and would match — and kill —
			// processes the caller never named.
			name: "regexp metacharacters are quoted",
			cmd:  []string{"python3", "-m", "http.server[0-9]"},
			want: []string{"pkill", "-f", "-x", "--", `python3 -m http\.server\[0-9\]`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pkillCommand(tt.cmd)

			if !slices.Equal(got, tt.want) {
				t.Fatalf("pkillCommand = %q, want %q", got, tt.want)
			}
			// The count is asserted separately because the failure it guards is a
			// usage error inside the container, invisible from here: pkill takes
			// exactly one pattern however many words the command had.
			if len(got) != 5 {
				t.Errorf("pkillCommand produced %d args, want pkill plus exactly one pattern", len(got))
			}
		})
	}
}

func TestClassifyPkillExitTreatsNoMatchAsSuccess(t *testing.T) {
	tests := []struct {
		name     string
		exitCode int
		wantOK   bool
		wantMsg  string
	}{
		{
			name:     "the process was signalled",
			exitCode: 0,
			wantOK:   true,
		},
		{
			// The case that matters most: a command that exited on its own between
			// the deadline and the kill leaves nothing to match. Reporting that as a
			// failure would join a spurious error onto nearly every timeout.
			name:     "nothing matched because it already exited",
			exitCode: 1,
			wantOK:   true,
		},
		{
			// Distroless and scratch images have no pkill. The message has to name
			// the image or the user goes looking for a bug in quickspin.
			name:     "pkill is not in the image",
			exitCode: 127,
			wantOK:   false,
			wantMsg:  "not available in this image",
		},
		{
			name:     "pkill is present but not executable",
			exitCode: 126,
			wantOK:   false,
			wantMsg:  "not available in this image",
		},
		{
			name:     "pkill failed for its own reasons",
			exitCode: 2,
			wantOK:   false,
			wantMsg:  "pkill exited 2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, ok := classifyPkillExit(tt.exitCode)

			if ok != tt.wantOK {
				t.Fatalf("classifyPkillExit(%d) ok = %v, want %v", tt.exitCode, ok, tt.wantOK)
			}
			if tt.wantMsg == "" {
				if msg != "" {
					t.Errorf("classifyPkillExit(%d) message = %q, want it empty on success", tt.exitCode, msg)
				}
				return
			}
			if !strings.Contains(msg, tt.wantMsg) {
				t.Errorf("classifyPkillExit(%d) message = %q, want it to contain %q", tt.exitCode, msg, tt.wantMsg)
			}
		})
	}
}

// --- Create: request shape, ordering, and rollback ---------------------------

func TestCreateOrdersItsRequestsAndSendsQuickspinsConfig(t *testing.T) {
	daemon := newFakeDaemon(t)
	rt, _ := newDockerTestRuntime(t, slog.LevelInfo, daemon)

	info, err := rt.Create(t.Context(), testSandboxID, testSpec(testImage, map[string]string{"B": "2", "A": "1"}))
	if err != nil {
		t.Fatalf("Create error = %v, want nil", err)
	}
	if info.ID != testSandboxID {
		t.Errorf("Create id = %q, want supplied id %q", info.ID, testSandboxID)
	}

	// Pull must precede create — ContainerCreate does not pull — and create must
	// precede start, since there is nothing to start before it exists.
	wantOrder := []string{
		"POST /images/create",
		"POST /containers/create",
		"POST /containers/" + testContainerID + "/start",
	}
	if got := daemon.routes(); !slices.Equal(got, wantOrder) {
		t.Fatalf("request order = %v, want %v", got, wantOrder)
	}

	// The SDK splits and normalizes the reference — "alpine:3.20" leaves as
	// fromImage=docker.io/library/alpine&tag=3.20 — so the assertion is that
	// both halves of the spec's image survive, not that the registry default
	// stays spelled a particular way.
	pull := daemon.request(0).query
	if repo := pull.Get("fromImage"); !strings.HasSuffix(repo, "alpine") {
		t.Errorf("pull fromImage = %q, want the repository from %q", repo, testImage)
	}
	if tag := pull.Get("tag"); tag != "3.20" {
		t.Errorf("pull tag = %q, want the tag from %q", tag, testImage)
	}

	var body struct {
		Image      string
		Env        []string
		Labels     map[string]string
		HostConfig struct {
			RestartPolicy   container.RestartPolicy
			PublishAllPorts bool
			NetworkMode     container.NetworkMode
			Memory          int64
			NanoCpus        int64  // the wire name; the Go field is NanoCPUs
			PidsLimit       *int64 // absent in JSON, not zero, when the field is dropped
		}
	}
	if err := json.Unmarshal(daemon.request(1).body, &body); err != nil {
		t.Fatalf("decode create body: %v", err)
	}

	if body.Image != testImage {
		t.Errorf("create body Image = %q, want %q", body.Image, testImage)
	}
	if want := []string{"A=1", "B=2"}; !slices.Equal(body.Env, want) {
		t.Errorf("create body Env = %v, want %v", body.Env, want)
	}
	if body.Labels[labelSandboxID] != testSandboxID || body.Labels[labelManaged] != labelManagedValue {
		t.Errorf("create body Labels = %v, want the supplied id %q and the managed marker", body.Labels, testSandboxID)
	}
	if body.HostConfig.RestartPolicy.Name != container.RestartPolicyAlways {
		t.Errorf("create body RestartPolicy = %q, want always", body.HostConfig.RestartPolicy.Name)
	}
	if !body.HostConfig.PublishAllPorts {
		t.Error("create body PublishAllPorts = false, want true")
	}

	// The limits are asserted on the wire rather than only on the struct because
	// the SDK renames NanoCPUs to NanoCpus and omits a nil PidsLimit entirely — a
	// dropped limit is an absent JSON key, which the daemon reads as unlimited.
	if body.HostConfig.Memory != testMemoryLimit {
		t.Errorf("create body Memory = %d, want %d", body.HostConfig.Memory, int64(testMemoryLimit))
	}
	if want := int64(500_000_000); body.HostConfig.NanoCpus != want {
		t.Errorf("create body NanoCpus = %d, want %d", body.HostConfig.NanoCpus, want)
	}
	if body.HostConfig.PidsLimit == nil {
		t.Error("create body PidsLimit absent, want the limit: the daemon reads absent as unlimited")
	} else if *body.HostConfig.PidsLimit != testPidsLimit {
		t.Errorf("create body PidsLimit = %d, want %d", *body.HostConfig.PidsLimit, int64(testPidsLimit))
	}
	if want := container.NetworkMode("none"); body.HostConfig.NetworkMode != want {
		t.Errorf("create body NetworkMode = %q, want %q", body.HostConfig.NetworkMode, want)
	}
}

func TestCreateRejectsAnInvalidSpecBeforeTouchingTheDaemon(t *testing.T) {
	// Validation runs before the pull, so an unrunnable spec costs no registry
	// round trip and leaves nothing to clean up.
	daemon := newFakeDaemon(t)
	rt, _ := newDockerTestRuntime(t, slog.LevelInfo, daemon)

	_, err := rt.Create(t.Context(), testSandboxID, runtime.NewSpec(testImage, nil, testCPULimit, 0, testPidsLimit, false))
	if !errors.Is(err, runtime.ErrInvalidSpec) {
		t.Fatalf("Create error = %v, want errors.Is(..., ErrInvalidSpec)", err)
	}
	if got := daemon.routes(); len(got) != 0 {
		t.Errorf("requests = %v, want none for a spec that never validated", got)
	}
}

func TestCreateMapsADaemonNotFoundToErrImageMissing(t *testing.T) {
	tests := []struct {
		name string
		set  func(*fakeDaemon)
	}{
		{
			name: "the pull request is refused",
			set:  func(d *fakeDaemon) { d.pull = dockerError(http.StatusNotFound, "pull access denied for nope") },
		},
		{
			// ContainerCreate does not pull, so an image absent from the daemon
			// surfaces here rather than during the pull.
			name: "the image is absent at create",
			set:  func(d *fakeDaemon) { d.create = dockerError(http.StatusNotFound, "No such image: nope") },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			daemon := newFakeDaemon(t)
			tt.set(daemon)
			rt, _ := newDockerTestRuntime(t, slog.LevelInfo, daemon)

			_, err := rt.Create(t.Context(), testSandboxID, testSpec("nope:latest", nil))
			if !errors.Is(err, runtime.ErrImageMissing) {
				t.Fatalf("Create error = %v, want errors.Is(..., ErrImageMissing)", err)
			}
		})
	}
}

func TestCreateReportsAPullStreamFailureAndNeverCreates(t *testing.T) {
	// ImagePull returns as soon as the daemon accepts the request, so a denied
	// registry or a transfer that dies arrives inside the stream. Discarding it
	// would turn that into a successful create of nothing.
	daemon := newFakeDaemon(t)
	daemon.pull = func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, `{"error":"toomanyrequests: rate limited","errorDetail":{"message":"toomanyrequests: rate limited"}}`)
	}
	rt, _ := newDockerTestRuntime(t, slog.LevelInfo, daemon)

	_, err := rt.Create(t.Context(), testSandboxID, testSpec(testImage, nil))
	if err == nil || !strings.Contains(err.Error(), "toomanyrequests") {
		t.Fatalf("Create error = %v, want the stream's own cause", err)
	}
	if got := daemon.routes(); slices.Contains(got, "POST /containers/create") {
		t.Errorf("requests = %v, want no create after a failed pull", got)
	}
}

func TestCreateRemovesTheContainerItMadeWhenStartFails(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.start = dockerError(http.StatusInternalServerError, `exec: "nope": not found`)
	rt, _ := newDockerTestRuntime(t, slog.LevelInfo, daemon)

	if _, err := rt.Create(t.Context(), testSandboxID, testSpec(testImage, nil)); err == nil {
		t.Fatal("Create error = nil, want the start failure")
	}

	remove := daemon.lastMatching(http.MethodDelete)
	if !strings.HasSuffix(remove.path, "/containers/"+testContainerID) {
		t.Errorf("removed %q, want the container create just made", remove.path)
	}
	// Force covers the stop, which is what makes this work on a container that
	// is mid-start rather than cleanly stopped.
	if got := remove.query.Get("force"); got != "1" {
		t.Errorf("remove force = %q, want 1", got)
	}
}

func TestCreateNamesTheLeakWhenCleanupAlsoFails(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.start = dockerError(http.StatusInternalServerError, "start refused")
	daemon.remove = dockerError(http.StatusInternalServerError, "remove refused")
	rt, _ := newDockerTestRuntime(t, slog.LevelInfo, daemon)

	_, err := rt.Create(t.Context(), testSandboxID, testSpec(testImage, nil))
	if err == nil {
		t.Fatal("Create error = nil, want the joined failure")
	}

	// The cleanup failure is joined onto the cause rather than replacing it: the
	// cause explains why create failed, the join says what is now orphaned.
	got := err.Error()
	for _, want := range []string{"start refused", "remove refused", "leaked container " + testContainerID} {
		if !strings.Contains(got, want) {
			t.Errorf("Create error = %q, want it to contain %q", got, want)
		}
	}
}

func TestCreateStillRemovesTheContainerWhenTheCallerCancels(t *testing.T) {
	// The rollback runs on a context detached from the caller's. Reusing the
	// caller's would make a cancelled create a guaranteed leak — and
	// cancellation mid-create is precisely when there is something to clean up.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	daemon := newFakeDaemon(t)
	daemon.start = func(w http.ResponseWriter, r *http.Request) {
		cancel()
		dockerError(http.StatusInternalServerError, "start refused")(w, r)
	}
	rt, _ := newDockerTestRuntime(t, slog.LevelInfo, daemon)

	if _, err := rt.Create(ctx, testSandboxID, testSpec(testImage, nil)); err == nil {
		t.Fatal("Create error = nil, want a failure after cancellation")
	}

	remove := daemon.lastMatching(http.MethodDelete)
	if !strings.HasSuffix(remove.path, "/containers/"+testContainerID) {
		t.Errorf("removed %q, want the container created before the cancel", remove.path)
	}
}

// --- Identity validation and lookup ------------------------------------------

func TestOperationsRejectAMalformedIDBeforeReachingTheDaemon(t *testing.T) {
	// A malformed id is a caller bug and must stay distinguishable from an
	// absent one, so it cannot be allowed to become a 404 from the daemon.
	const malformed = "not-a-sandbox-id"

	tests := []struct {
		name string
		call func(*testing.T, *Runtime) error
	}{
		{
			name: "Create",
			call: func(t *testing.T, rt *Runtime) error {
				_, err := rt.Create(t.Context(), malformed, testSpec(testImage, nil))
				return err
			},
		},
		{
			name: "Inspect",
			call: func(t *testing.T, rt *Runtime) error {
				_, err := rt.Inspect(t.Context(), malformed)
				return err
			},
		},
		{
			name: "Destroy",
			call: func(t *testing.T, rt *Runtime) error {
				return rt.Destroy(t.Context(), malformed)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			daemon := newFakeDaemon(t)
			rt, _ := newDockerTestRuntime(t, slog.LevelInfo, daemon)

			if err := tt.call(t, rt); !errors.Is(err, runtime.ErrInvalidSandboxID) {
				t.Fatalf("%s error = %v, want ErrInvalidSandboxID", tt.name, err)
			}
			if got := daemon.routes(); len(got) != 0 {
				t.Errorf("requests = %v, want none: validation must stop before the daemon", got)
			}
		})
	}
}

func TestLookupFiltersOnBothLabelsAndIncludesStoppedContainers(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.list = listOK(container.Summary{ID: testContainerID, Labels: managedLabels(testSandboxID)})
	rt, _ := newDockerTestRuntime(t, slog.LevelInfo, daemon)

	if _, err := rt.Inspect(t.Context(), testSandboxID); err != nil {
		t.Fatalf("Inspect error = %v, want nil", err)
	}

	query := daemon.request(0).query
	// Without All, a created-but-not-started or exited sandbox is invisible, and
	// Destroy would report success while leaving the container behind.
	if got := query.Get("all"); got != "1" {
		t.Errorf("list all = %q, want 1", got)
	}

	var filters map[string]map[string]bool
	if err := json.Unmarshal([]byte(query.Get("filters")), &filters); err != nil {
		t.Fatalf("decode filters %q: %v", query.Get("filters"), err)
	}
	for _, want := range []string{
		labelSandboxID + "=" + testSandboxID,
		labelManaged + "=" + labelManagedValue,
	} {
		if !filters["label"][want] {
			t.Errorf("filters = %v, want the label term %q", filters, want)
		}
	}
}

func TestInspectReportsErrNotFoundWhenNoContainerCarriesTheLabel(t *testing.T) {
	rt, _ := newDockerTestRuntime(t, slog.LevelInfo, newFakeDaemon(t))

	if _, err := rt.Inspect(t.Context(), testSandboxID); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("Inspect error = %v, want ErrNotFound", err)
	}
}

// --- List --------------------------------------------------------------------

// TestListRoutesTheSummaryThroughTheTranslators asserts routing, not mapping.
// Which Docker status becomes which State, and how a Unix second becomes a Time,
// belong to TestStateFromContainerState and TestCreatedAtFromUnix in
// translate_test.go, which cover every case exhaustively without an HTTP round
// trip. Two contrasting rows are enough to catch the failure this layer can see:
// a List that hardcodes a state or drops the timestamp.
func TestListRoutesTheSummaryThroughTheTranslators(t *testing.T) {
	const created = 1_700_000_000
	createdAt := time.Unix(created, 0).UTC()

	tests := []struct {
		name          string
		state         container.ContainerState
		created       int64
		wantState     runtime.State
		wantCreatedAt time.Time
	}{
		{name: "running", state: container.StateRunning, created: created, wantState: runtime.StateRunning, wantCreatedAt: createdAt},
		{name: "exited", state: container.StateExited, created: created, wantState: runtime.StateStopped, wantCreatedAt: createdAt},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			daemon := newFakeDaemon(t)
			daemon.list = listOK(container.Summary{
				ID:      testContainerID,
				Labels:  managedLabels(testSandboxID),
				State:   tt.state,
				Created: tt.created,
			})
			rt, _ := newDockerTestRuntime(t, slog.LevelInfo, daemon)

			infos, err := rt.List(t.Context())
			if err != nil {
				t.Fatalf("List error = %v, want nil", err)
			}
			if len(infos) != 1 {
				t.Fatalf("List = %#v, want one sandbox", infos)
			}
			if infos[0].State != tt.wantState {
				t.Errorf("State = %q, want %q", infos[0].State, tt.wantState)
			}
			if !infos[0].CreatedAt.Equal(tt.wantCreatedAt) {
				t.Errorf("CreatedAt = %v, want %v", infos[0].CreatedAt, tt.wantCreatedAt)
			}
		})
	}
}

func TestListIgnoresContainersQuickspinDoesNotOwn(t *testing.T) {
	daemon := newFakeDaemon(t)
	// The daemon's own filter should already exclude this, so what is under test
	// is List's second check: a widened filter must not widen ownership.
	daemon.list = listOK(
		container.Summary{ID: "someone-elses", Labels: map[string]string{"com.example": "yes"}},
		container.Summary{ID: testContainerID, Labels: managedLabels(testSandboxID)},
	)
	rt, _ := newDockerTestRuntime(t, slog.LevelInfo, daemon)

	infos, err := rt.List(t.Context())
	if err != nil {
		t.Fatalf("List error = %v, want nil", err)
	}
	if len(infos) != 1 || infos[0].ID != testSandboxID {
		t.Errorf("List = %#v, want only the managed sandbox", infos)
	}
}

func TestListLogsSkippedContainerAndResultCount(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.list = listOK(
		container.Summary{ID: "container-bad", Labels: managedMarkerLabels(), State: container.StateRunning},
		container.Summary{ID: testContainerID, Labels: managedLabels(testSandboxID), State: container.StateRunning},
	)
	rt, logs := newDockerTestRuntime(t, slog.LevelDebug, daemon)

	infos, err := rt.List(t.Context())
	if err != nil {
		t.Fatalf("List error = %v, want nil", err)
	}
	// One corrupt id label must not hide every healthy sandbox: the bad
	// container is unreachable through the CLI precisely because it cannot be
	// named, and the warning is what keeps that skip from being silent.
	if len(infos) != 1 {
		t.Fatalf("List infos = %#v, want one valid sandbox", infos)
	}

	gotLogs := logs.String()
	if !strings.Contains(gotLogs, `level=WARN msg="skipping managed container with invalid sandbox label" containerID=container-bad`) {
		t.Errorf("List logs = %q, want warning with skipped container identity", gotLogs)
	}
	if !strings.Contains(gotLogs, `level=DEBUG msg="listed managed sandboxes" count=1`) {
		t.Errorf("List logs = %q, want debug result count", gotLogs)
	}
}

func TestListRetainsTheDaemonsCause(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.list = dockerError(http.StatusInternalServerError, "daemon is shutting down")
	rt, _ := newDockerTestRuntime(t, slog.LevelInfo, daemon)

	_, err := rt.List(t.Context())
	if err == nil || !strings.Contains(err.Error(), "daemon is shutting down") {
		t.Fatalf("List error = %v, want the daemon's cause", err)
	}
	if errors.Is(err, runtime.ErrNotFound) {
		t.Error("List error matched ErrNotFound, want a plain failure")
	}
}

// --- Destroy -----------------------------------------------------------------

func TestDestroyTreatsAnAbsentSandboxAsSuccess(t *testing.T) {
	// Both absences return nil so every recovery path can destroy without first
	// checking whether the sandbox is still there.
	tests := []struct {
		name string
		set  func(*fakeDaemon)
	}{
		{
			name: "no container carries the label",
			set:  func(*fakeDaemon) {},
		},
		{
			name: "the removal races another removal",
			set: func(d *fakeDaemon) {
				d.list = listOK(container.Summary{ID: testContainerID, Labels: managedLabels(testSandboxID)})
				d.remove = dockerError(http.StatusNotFound, "No such container")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			daemon := newFakeDaemon(t)
			tt.set(daemon)
			rt, _ := newDockerTestRuntime(t, slog.LevelInfo, daemon)

			if err := rt.Destroy(t.Context(), testSandboxID); err != nil {
				t.Fatalf("Destroy error = %v, want nil", err)
			}
		})
	}
}

func TestDestroyRetainsTheDaemonsCauseOnARealFailure(t *testing.T) {
	tests := []struct {
		name string
		set  func(*fakeDaemon)
		want string
	}{
		{
			name: "the lookup fails",
			set:  func(d *fakeDaemon) { d.list = dockerError(http.StatusInternalServerError, "lookup exploded") },
			want: "lookup exploded",
		},
		{
			name: "the removal fails",
			set: func(d *fakeDaemon) {
				d.list = listOK(container.Summary{ID: testContainerID, Labels: managedLabels(testSandboxID)})
				d.remove = dockerError(http.StatusConflict, "removal in progress")
			},
			want: "removal in progress",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			daemon := newFakeDaemon(t)
			tt.set(daemon)
			rt, _ := newDockerTestRuntime(t, slog.LevelInfo, daemon)

			err := rt.Destroy(t.Context(), testSandboxID)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Destroy error = %v, want the cause %q", err, tt.want)
			}
		})
	}
}

func TestDestroyLogsLifecycleAtInfo(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.list = listOK(container.Summary{ID: testContainerID, Labels: managedLabels(testSandboxID)})
	rt, logs := newDockerTestRuntime(t, slog.LevelInfo, daemon)

	if err := rt.Destroy(t.Context(), testSandboxID); err != nil {
		t.Fatalf("Destroy error = %v, want nil", err)
	}

	want := `level=INFO msg="sandbox destroyed" sandboxID=` + testSandboxID + ` containerID=` + testContainerID
	if got := logs.String(); !strings.Contains(got, want) {
		t.Errorf("Destroy logs = %q, want info lifecycle event with both identities", got)
	}
}

// --- The fake daemon ---------------------------------------------------------

// fakeDaemon is the Docker Engine API as far as these tests need it. The real
// Moby client still serializes every request and decodes every response, so
// what is under test is the wire contract rather than a hand-written client
// interface. Each endpoint defaults to success, so a test states only the one
// failure it is about.
type fakeDaemon struct {
	t *testing.T

	mu       sync.Mutex
	recorded []recordedRequest

	pull        http.HandlerFunc
	create      http.HandlerFunc
	start       http.HandlerFunc
	list        http.HandlerFunc
	remove      http.HandlerFunc
	copyTo      http.HandlerFunc
	copyFrom    http.HandlerFunc
	execCreate  http.HandlerFunc
	execStart   http.HandlerFunc
	execInspect http.HandlerFunc
}

type recordedRequest struct {
	method string
	path   string
	query  url.Values
	body   []byte
}

// route is the method-and-path form used in ordering assertions, with the
// negotiated /v1.NN prefix stripped: the API version is the SDK's business, and
// pinning it here would make an SDK upgrade look like a Quickspin change.
func (r recordedRequest) route() string {
	_, rest, ok := strings.Cut(strings.TrimPrefix(r.path, "/"), "/")
	if !ok {
		return r.method + " " + r.path
	}
	return r.method + " /" + rest
}

// String keeps a failure that prints the recorded list readable: the create
// body alone is several hundred bytes of SDK defaults.
func (r recordedRequest) String() string { return r.route() }

func newFakeDaemon(t *testing.T) *fakeDaemon {
	return &fakeDaemon{
		t:      t,
		pull:   func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, `{"status":"Download complete"}`) },
		create: func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, map[string]any{"Id": testContainerID}) },
		start:  func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) },
		list:   listOK(),
		remove: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) },
		copyTo: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
		// An archive with no entries: the success answer that carries nothing, so
		// a test that has not staged content gets ErrPathNotFound rather than a
		// decode failure it did not ask about.
		copyFrom: copyFromOK(t, nil),
		execCreate: func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, map[string]string{"Id": "exec-good"})
		},
		execStart: hijackExec(t, nil, nil),
		execInspect: func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, map[string]any{"ID": "exec-good", "Running": false, "ExitCode": 0})
		},
	}
}

func (d *fakeDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		d.t.Errorf("read request body: %v", err)
	}

	d.mu.Lock()
	d.recorded = append(d.recorded, recordedRequest{
		method: r.Method,
		path:   r.URL.Path,
		query:  r.URL.Query(),
		body:   body,
	})
	d.mu.Unlock()

	path := r.URL.Path
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/images/create"):
		d.pull(w, r)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/containers/create"):
		d.create(w, r)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/exec"):
		d.execCreate(w, r)
	case r.Method == http.MethodPost && strings.Contains(path, "/exec/") && strings.HasSuffix(path, "/start"):
		d.execStart(w, r)
	case r.Method == http.MethodGet && strings.Contains(path, "/exec/") && strings.HasSuffix(path, "/json"):
		d.execInspect(w, r)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/start"):
		d.start(w, r)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/containers/json"):
		d.list(w, r)
	case r.Method == http.MethodPut && strings.HasSuffix(path, "/archive"):
		d.copyTo(w, r)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/archive"):
		d.copyFrom(w, r)
	// HEAD shares copyFrom (net/http drops the body), so one staged answer
	// covers both a stat-first and a stream-first implementation.
	case r.Method == http.MethodHead && strings.HasSuffix(path, "/archive"):
		d.copyFrom(w, r)
	case r.Method == http.MethodDelete && strings.Contains(path, "/containers/"):
		d.remove(w, r)
	default:
		d.t.Errorf("unexpected request %s %s", r.Method, path)
		dockerError(http.StatusNotImplemented, "unexpected request")(w, r)
	}
}

func (d *fakeDaemon) routes() []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	routes := make([]string, 0, len(d.recorded))
	for _, r := range d.recorded {
		routes = append(routes, r.route())
	}
	return routes
}

func (d *fakeDaemon) request(i int) recordedRequest {
	d.t.Helper()

	d.mu.Lock()
	defer d.mu.Unlock()
	if i >= len(d.recorded) {
		d.t.Fatalf("wanted request %d but only %d were recorded", i, len(d.recorded))
	}
	return d.recorded[i]
}

// lastMatching lets the rollback assertions find the removal without pinning how
// many requests preceded it.
func (d *fakeDaemon) lastMatching(method string) recordedRequest {
	d.t.Helper()

	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range slices.Backward(d.recorded) {
		if r.method == method {
			return r
		}
	}
	d.t.Fatalf("no %s request among %v", method, d.recorded)
	return recordedRequest{}
}

func (d *fakeDaemon) lastMatchingPath(method, suffix string) recordedRequest {
	d.t.Helper()

	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range slices.Backward(d.recorded) {
		if r.method == method && strings.HasSuffix(r.path, suffix) {
			return r
		}
	}
	d.t.Fatalf("no %s request ending in %q among %v", method, suffix, d.recorded)
	return recordedRequest{}
}

// dockerError answers the way the daemon does, so the Moby client's own decoder
// classifies it: a 404 becomes an errdefs not-found without this test knowing
// how that mapping works.
func dockerError(status int, message string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": message})
	}
}

func copyFromOK(t *testing.T, archive []byte) http.HandlerFunc {
	t.Helper()
	return copyFromStat(t, container.PathStat{}, archive)
}

func hijackExec(t *testing.T, stdout, stderr []byte) http.HandlerFunc {
	t.Helper()

	return func(w http.ResponseWriter, _ *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack exec connection: %v", err)
			return
		}
		defer conn.Close()

		if _, err := fmt.Fprint(conn,
			"HTTP/1.1 101 UPGRADED\r\n"+
				"Content-Type: application/vnd.docker.raw-stream\r\n"+
				"Connection: Upgrade\r\n"+
				"Upgrade: tcp\r\n\r\n"); err != nil {
			t.Errorf("write exec upgrade response: %v", err)
			return
		}
		if len(stdout) != 0 {
			if err := writeExecStream(conn, 1, stdout); err != nil {
				t.Errorf("write exec stdout: %v", err)
				return
			}
		}
		if len(stderr) != 0 {
			if err := writeExecStream(conn, 2, stderr); err != nil {
				t.Errorf("write exec stderr: %v", err)
			}
		}
	}
}

func writeExecStream(w io.Writer, stream byte, content []byte) error {
	header := make([]byte, 8)
	header[0] = stream
	binary.BigEndian.PutUint32(header[4:], uint32(len(content)))
	if _, err := w.Write(header); err != nil {
		return err
	}
	_, err := w.Write(content)
	return err
}

// copyFromStat answers the way the daemon does. The stat header is not optional
// scenery: the SDK decodes X-Docker-Container-Path-Stat before it hands back
// the stream, and a response without it fails the whole call with a decode
// error rather than an empty stat.
func copyFromStat(t *testing.T, stat container.PathStat, archive []byte) http.HandlerFunc {
	t.Helper()

	encoded, err := json.Marshal(stat)
	if err != nil {
		t.Fatalf("marshal path stat: %v", err)
	}
	if archive == nil {
		archive = tarEntries(t)
	}

	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Docker-Container-Path-Stat", base64.StdEncoding.EncodeToString(encoded))
		w.Header().Set("Content-Type", "application/x-tar")
		_, _ = w.Write(archive)
	}
}

func listOK(summaries ...container.Summary) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, summaries) }
}

func listOKManaged() http.HandlerFunc {
	return listOK(container.Summary{
		ID:     testContainerID,
		Labels: managedLabels(testSandboxID),
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func newDockerTestRuntime(
	t *testing.T,
	level slog.Level,
	handler http.Handler,
) (*Runtime, *bytes.Buffer) {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	// A pinned API version rather than negotiation: negotiating would spend a
	// /version round trip per test and put the SDK's handshake into the recorded
	// request list.
	dockerClient, err := client.New(
		// ExecAttach dials the configured scheme as a network. The ordinary
		// HTTP endpoints tolerate http:// here, but a hijacked exec connection
		// requires Docker's tcp:// host form.
		client.WithHost("tcp://"+server.Listener.Addr().String()),
		client.WithAPIVersion(client.MaxAPIVersion),
	)
	if err != nil {
		t.Fatalf("new Docker test client: %v", err)
	}
	t.Cleanup(func() { _ = dockerClient.Close() })

	logs := new(bytes.Buffer)
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: level}))
	rt, err := New(dockerClient, logger)
	if err != nil {
		t.Fatalf("New error = %v, want nil", err)
	}

	return rt, logs
}
