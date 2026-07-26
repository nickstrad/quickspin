package runtime

import (
	"bytes"
	"context"
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
)

// The sandbox ids live in labels_test.go; these are what the fake daemon
// answers with.
const (
	testContainerID = "container-good"
	testImage       = "alpine:3.20"
)

func TestNewDockerRuntimeRequiresLogger(t *testing.T) {
	_, err := NewDockerRuntime(&client.Client{}, nil)
	if err == nil || !strings.Contains(err.Error(), "logger is required") {
		t.Fatalf("NewDockerRuntime error = %v, want required logger error", err)
	}
}

// --- Pure decisions ----------------------------------------------------------

func TestNewContainerConfigsCarriesEveryQuickspinDecision(t *testing.T) {
	spec := NewSpec(testImage, map[string]string{"FOO_A": "3", "FOO": "1", "FOO2": "2"})

	cfg, host := newContainerConfigs(spec, testSandboxID)

	if cfg.Image != testImage {
		t.Errorf("Image = %q, want %q", cfg.Image, testImage)
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
			wantErr:     ErrImageMissing,
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
			err := classifyNotFound("op", "pulling", ErrImageMissing, tt.err)

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

// --- Create: request shape, ordering, and rollback ---------------------------

func TestCreateOrdersItsRequestsAndSendsQuickspinsConfig(t *testing.T) {
	daemon := newFakeDaemon(t)
	rt, _ := newDockerTestRuntime(t, slog.LevelInfo, daemon)

	info, err := rt.Create(t.Context(), NewSpec(testImage, map[string]string{"B": "2", "A": "1"}))
	if err != nil {
		t.Fatalf("Create error = %v, want nil", err)
	}
	if err := validateSandboxID(info.ID); err != nil {
		t.Errorf("Create id %q is not a well-formed sandbox id: %v", info.ID, err)
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
	if body.Labels[labelSandboxID] != info.ID || body.Labels[labelManaged] != labelManagedValue {
		t.Errorf("create body Labels = %v, want the returned id %q and the managed marker", body.Labels, info.ID)
	}
	if body.HostConfig.RestartPolicy.Name != container.RestartPolicyAlways {
		t.Errorf("create body RestartPolicy = %q, want always", body.HostConfig.RestartPolicy.Name)
	}
	if !body.HostConfig.PublishAllPorts {
		t.Error("create body PublishAllPorts = false, want true")
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

			_, err := rt.Create(t.Context(), NewSpec("nope:latest", nil))
			if !errors.Is(err, ErrImageMissing) {
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

	_, err := rt.Create(t.Context(), NewSpec(testImage, nil))
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

	if _, err := rt.Create(t.Context(), NewSpec(testImage, nil)); err == nil {
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

	_, err := rt.Create(t.Context(), NewSpec(testImage, nil))
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

	if _, err := rt.Create(ctx, NewSpec(testImage, nil)); err == nil {
		t.Fatal("Create error = nil, want a failure after cancellation")
	}

	remove := daemon.lastMatching(http.MethodDelete)
	if !strings.HasSuffix(remove.path, "/containers/"+testContainerID) {
		t.Errorf("removed %q, want the container created before the cancel", remove.path)
	}
}

// --- Lookup ------------------------------------------------------------------

func TestLookupRejectsAMalformedIDBeforeReachingTheDaemon(t *testing.T) {
	// A malformed id is a caller bug and must stay distinguishable from an
	// absent one, so it cannot be allowed to become a 404 from the daemon.
	const malformed = "not-a-sandbox-id"

	tests := map[string]func(*testing.T, *DockerRuntime) error{
		"Inspect": func(t *testing.T, rt *DockerRuntime) error {
			_, err := rt.Inspect(t.Context(), malformed)
			return err
		},
		"Destroy": func(t *testing.T, rt *DockerRuntime) error {
			return rt.Destroy(t.Context(), malformed)
		},
	}

	for name, call := range tests {
		t.Run(name, func(t *testing.T) {
			daemon := newFakeDaemon(t)
			rt, _ := newDockerTestRuntime(t, slog.LevelInfo, daemon)

			if err := call(t, rt); !errors.Is(err, ErrInvalidSandboxID) {
				t.Fatalf("%s error = %v, want ErrInvalidSandboxID", name, err)
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

	if _, err := rt.Inspect(t.Context(), testSandboxID); !errors.Is(err, ErrNotFound) {
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
		wantState     State
		wantCreatedAt time.Time
	}{
		{name: "running", state: container.StateRunning, created: created, wantState: StateRunning, wantCreatedAt: createdAt},
		{name: "exited", state: container.StateExited, created: created, wantState: StateStopped, wantCreatedAt: createdAt},
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
	if errors.Is(err, ErrNotFound) {
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

	pull   http.HandlerFunc
	create http.HandlerFunc
	start  http.HandlerFunc
	list   http.HandlerFunc
	remove http.HandlerFunc
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
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/start"):
		d.start(w, r)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/containers/json"):
		d.list(w, r)
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

func listOK(summaries ...container.Summary) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, summaries) }
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func newDockerTestRuntime(
	t *testing.T,
	level slog.Level,
	handler http.Handler,
) (*DockerRuntime, *bytes.Buffer) {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	// A pinned API version rather than negotiation: negotiating would spend a
	// /version round trip per test and put the SDK's handshake into the recorded
	// request list.
	dockerClient, err := client.New(
		client.WithHost(server.URL),
		client.WithAPIVersion(client.MaxAPIVersion),
	)
	if err != nil {
		t.Fatalf("new Docker test client: %v", err)
	}
	t.Cleanup(func() { _ = dockerClient.Close() })

	logs := new(bytes.Buffer)
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: level}))
	rt, err := NewDockerRuntime(dockerClient, logger)
	if err != nil {
		t.Fatalf("NewDockerRuntime error = %v, want nil", err)
	}

	return rt, logs
}
