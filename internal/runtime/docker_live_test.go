// The external test package is deliberate: this file needs both runtime and
// runtimetest, and runtimetest imports runtime. It compiles during an ordinary
// `make test` and skips at run time, so a machine with no Docker still builds
// every assertion here — a build tag would let this file rot unnoticed.
package runtime_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/nickstrad/quickspin/internal/runtime"
	"github.com/nickstrad/quickspin/internal/runtime/runtimetest"
)

const (
	dockerGate = "QUICKSPIN_TEST_DOCKER"
	// imageEnv lets hack/test-runtime-docker.sh pin one image for both this
	// suite and the CLI smoke, so the two cannot drift apart. The default below
	// is only for a bare `go test`.
	imageEnv = "QUICKSPIN_TEST_IMAGE"

	// A pinned, multi-architecture image whose default process stays up, so the
	// happy lifecycle is not racing the container's own exit. See
	// TestDockerCreateOnlyPromisesThatStartWasAccepted for what changes when it
	// does not.
	defaultLongRunningImage = "docker.io/library/nginx:1.27-alpine"

	// A pinned image whose default process exits immediately. Used only to
	// record what Create promises, never for core conformance.
	shortLivedImage = "docker.io/library/alpine:3.20"

	// Limits every live spec shares. They are well clear of Spec's minimums so a
	// conformance failure is never the daemon refusing an under-resourced
	// container; what the limits translate to is pinned by the unit tests.
	liveCPULimit    = 0.5
	liveMemoryLimit = 128 * 1024 * 1024
	livePidsLimit   = 256

	// Generous because the first run on a fresh daemon pays for a cold pull. It
	// bounds every call and every convergence poll in the shared suite.
	observeTimeout = 3 * time.Minute

	// The ownership marker and id label, spelled literally. These queries have
	// to keep working when the implementation under test is the broken thing, so
	// they must not route through runtime's own label helpers or Runtime.List.
	managedFilter  = "quickspin.managed=true"
	sandboxIDLabel = "quickspin.id"

	// teardownTimeout bounds work done outside a test body — from t.Cleanup or
	// TestMain — which cannot use t.Context(): the testing package cancels that
	// context just before cleanups run, so a cleanup reaching for it would fail
	// every time there was something to clean up.
	teardownTimeout = time.Minute
)

// liveClient is the one Docker client the daemon-level checks and the native
// queries share. TestMain owns its lifetime, which is why it is a package var:
// a client per test would cost an extra API-version negotiation each, and the
// runtime under test already constructs its own from the environment.
var liveClient *client.Client

// TestMain owns the daemon-level checks so they bracket every live test in this
// package rather than one of them. Inside a single test they would be wrong in
// both directions: the baseline would see a sibling test's sandbox, and the mop
// would run while later tests still had resources in flight.
//
// hack/test-runtime-docker.sh performs the same two checks in shell. That is not
// redundant: these make a bare `go test` honest, and the script's baseline check
// is what licenses its own trap to sweep — anything present at the end of a run
// that started clean is definitionally new.
func TestMain(m *testing.M) {
	if os.Getenv(dockerGate) != "1" {
		os.Exit(m.Run())
	}

	var err error
	if liveClient, err = client.New(client.FromEnv); err != nil {
		bail("new Docker client for the daemon-level checks: %v", err)
	}

	// A dirty test-owned daemon can only mean a previous run leaked. Cleaning it
	// silently would erase the one signal this harness exists to produce, so the
	// run is refused and make test-docker-clean is the explicit, separate mop.
	if survivors := listManagedOrBail(); len(survivors) != 0 {
		bail("refusing to run: the daemon already holds %d managed container(s):\n  %s\n"+
			"a previous run leaked. Clear them deliberately with `make test-docker-clean`.",
			len(survivors), describe(survivors))
	}

	code := m.Run()

	// Reported and recorded as a failure before removal, so a product leak never
	// looks like successful teardown.
	if survivors := listManagedOrBail(); len(survivors) != 0 {
		fmt.Fprintf(os.Stderr, "LEAK: %d managed container(s) survived the run:\n  %s\n",
			len(survivors), describe(survivors))
		code = 1

		ctx, cancel := context.WithTimeout(context.Background(), teardownTimeout)
		for _, c := range survivors {
			if _, err := liveClient.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
				fmt.Fprintf(os.Stderr, "removing survivor %s: %v\n", c.ID, err)
			}
		}
		cancel()
	}

	_ = liveClient.Close()
	os.Exit(code)
}

func longRunningImage() string {
	if image := os.Getenv(imageEnv); image != "" {
		return image
	}
	return defaultLongRunningImage
}

func TestDockerRuntimeConformance(t *testing.T) {
	rt := liveDocker(t)

	runtimetest.RunConformance(t, rt,
		runtime.NewSpec(longRunningImage(), map[string]string{"QUICKSPIN_CONFORMANCE": "1"}, liveCPULimit, liveMemoryLimit, livePidsLimit, false),
		observeTimeout)
}

func TestDockerCreateOnlyPromisesThatStartWasAccepted(t *testing.T) {
	// The question the plan left open: does Create promise the container is still
	// running when it returns? It does not — Create reports StateRunning because
	// ContainerStart was accepted, and an image whose process exits at once is
	// already gone by the time anyone looks.
	//
	// What muddies it further is the always restart policy: Docker respawns such
	// a container, so the observed status cycles through running, exited, and
	// restarting depending on when the request lands. A short-lived image
	// therefore yields a perpetually respawning sandbox rather than a visibly
	// stopped one. Core conformance uses a long-running image so no clause ever
	// depends on which phase happened to be visible.
	rt := liveDocker(t)

	info, err := rt.Create(t.Context(), runtime.NewSpec(shortLivedImage, nil, liveCPULimit, liveMemoryLimit, livePidsLimit, false))
	if err != nil {
		t.Fatalf("Create(%s) error = %v, want nil", shortLivedImage, err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), teardownTimeout)
		defer cancel()
		if err := rt.Destroy(ctx, info.ID); err != nil {
			t.Errorf("cleanup Destroy(%s) error = %v, want nil", info.ID, err)
		}
	})

	if info.State != runtime.StateRunning {
		t.Errorf("Create state = %q, want %q: Create reports what start accepted", info.State, runtime.StateRunning)
	}

	// The daemon's own status, not Runtime.Inspect: the subject is what Docker
	// does with a process that exits under an always policy, and the translation
	// of that status into State is already a pure table test.
	wantStates := []container.ContainerState{
		container.StateCreated, container.StateRunning, container.StateExited, container.StateRestarting,
	}
	native, err := listManaged(t.Context())
	if err != nil {
		t.Fatalf("native container list: %v", err)
	}
	found := false
	for _, c := range native {
		if c.Labels[sandboxIDLabel] != info.ID {
			continue
		}
		found = true
		t.Logf("Docker reports %s as %q for a process that exits immediately", info.ID, c.State)
		if !slices.Contains(wantStates, c.State) {
			t.Errorf("Docker state = %q, want one of %v", c.State, wantStates)
		}
	}
	if !found {
		t.Errorf("no Docker container labelled %s among %s", info.ID, describe(native))
	}
}

// liveDocker builds the runtime exactly the way cmd/quickspin does — a nil
// client, so the SDK reads DOCKER_HOST — because a test that constructed its own
// client would not prove the shipped construction path works.
func liveDocker(t *testing.T) *runtime.DockerRuntime {
	t.Helper()

	if os.Getenv(dockerGate) != "1" {
		t.Skipf("set %s=1 and DOCKER_HOST to a test-owned Docker daemon to run this; `make test-docker` does both",
			dockerGate)
	}

	logger := slog.New(slog.NewTextHandler(testLogWriter{t}, &slog.HandlerOptions{Level: slog.LevelInfo}))
	rt, err := runtime.NewDockerRuntime(nil, logger)
	if err != nil {
		t.Fatalf("NewDockerRuntime from the environment: %v", err)
	}

	return rt
}

// listManaged is the native ownership query. It finds every container Quickspin
// owns, including one whose id label is malformed — which Runtime.List
// deliberately skips and which would therefore be invisible to a leak check
// built on the interface under test.
func listManaged(ctx context.Context) ([]container.Summary, error) {
	result, err := liveClient.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: client.Filters{}.Add("label", managedFilter),
	})
	if err != nil {
		return nil, err
	}
	return result.Items, nil
}

func listManagedOrBail() []container.Summary {
	ctx, cancel := context.WithTimeout(context.Background(), teardownTimeout)
	defer cancel()

	summaries, err := listManaged(ctx)
	if err != nil {
		bail("native container list: %v", err)
	}
	return summaries
}

// bail is TestMain's only failure path: there is no *testing.T there, and a
// harness that cannot prove the daemon's state must not let tests run.
func bail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func describe(summaries []container.Summary) string {
	lines := make([]string, 0, len(summaries))
	for _, c := range summaries {
		lines = append(lines, c.ID[:min(12, len(c.ID))]+" "+string(c.State)+" "+c.Labels[sandboxIDLabel])
	}
	return strings.Join(lines, "\n  ")
}

// testLogWriter routes the runtime's structured logs into the test log, so they
// surface for a failing test rather than on every run.
type testLogWriter struct{ t *testing.T }

func (w testLogWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}
