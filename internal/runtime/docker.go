package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// execPollInterval paces the wait for an exec's exit code; each poll is a daemon
// round trip. It lives here rather than in runtime.go because polling is a
// Docker mechanism — the Engine API has no wait-for-exec endpoint — not part of
// the backend-neutral contract.
const execPollInterval = 50 * time.Millisecond

// execKillTimeout bounds the kill of a timed-out exec, which by definition runs
// after ExecOpts.Timeout is already spent.
const execKillTimeout = 10 * time.Second

type DockerRuntime struct {
	Client *client.Client
	logger *slog.Logger
}

var _ Runtime = (*DockerRuntime)(nil)

func NewDockerRuntime(c *client.Client, logger *slog.Logger) (*DockerRuntime, error) {
	if logger == nil {
		return nil, E("runtime.NewDockerRuntime", "logger is required", nil)
	}

	if c == nil {
		fromEnv, err := client.New(client.FromEnv)
		if err != nil {
			return nil, E("runtime.NewDockerRuntime", "creating docker client", err)
		}
		c = fromEnv
	}

	return &DockerRuntime{
		Client: c,
		logger: logger,
	}, nil
}

// newContainerConfigs is the one place Spec becomes Docker's vocabulary. It is
// receiver-free so the field-copy can be tested directly: a config block built
// inline behind ContainerCreate can only be checked against a live daemon, and
// a forgotten field there is invisible.
func newContainerConfigs(spec Spec, platformID string) (container.Config, container.HostConfig, error) {
	const op = "runtime.newContainerConfigs"

	if err := spec.Validate(); err != nil {
		return container.Config{}, container.HostConfig{}, Wrap(op, "", err)
	}

	networkMode := container.NetworkMode("none")
	if spec.AllowNetwork {
		networkMode = container.NetworkMode("bridge")
	}

	return container.Config{
			Image:  spec.Image,
			Env:    envToArgs(spec.Env),
			Labels: managedLabels(platformID),
		}, container.HostConfig{
			RestartPolicy:   container.RestartPolicy{Name: container.RestartPolicyAlways},
			PublishAllPorts: true,
			NetworkMode:     networkMode,
			Resources: container.Resources{
				// Memory is bytes and NanoCPUs is cores × 10⁹ (0.5 cores =
				// 500_000_000); both land in the container's cgroup v2 files as
				// memory.max and cpu.max, so a units mistake here is a wrong kernel
				// limit rather than an error.
				Memory:   spec.MemoryLimit,
				NanoCPUs: int64(spec.CPULimit * 1e9),
				// PidsLimit is *int64 in the SDK, where nil means "leave unset" — the
				// daemon then imposes no pids.max at all. spec is a value parameter,
				// so this address is a private copy, not a caller's field.
				PidsLimit: &spec.PidsLimit,
			},
		}, nil
}

// labelFilter builds the daemon's label filter. The term is the literal string
// "label" and the value is "key=value"; a label key is not itself a filter name,
// and passing one matches nothing rather than erroring.
func labelFilter(labels map[string]string) client.Filters {
	f := client.Filters{}
	for k, v := range labels {
		f.Add("label", k+"="+v)
	}
	return f
}

// classifyNotFound substitutes sentinel when the daemon reported a not-found,
// folding the SDK's own text into Message so nothing is lost. errdefs is
// consulted here and in removeContainer only: if a caller ever has to import it,
// the backend has escaped this file.
func classifyNotFound(op, message string, sentinel, err error) error {
	if errdefs.IsNotFound(err) {
		return E(op, fmt.Sprintf("%s: %v", message, err), sentinel)
	}
	return E(op, message, err)
}

// classifyExecDone names the reason an exec stopped early, given the caller's
// context error and that of the timeout-bearing context derived from it. It is
// receiver-free so the ordering below can be table-tested: which of the two
// errors is consulted first decides what the caller can distinguish, and getting
// it wrong is invisible at the call site.
//
// parentErr wins because deriving the timeout context makes both Done at once —
// a caller who cancelled should hear that, not "deadline". Only a healthy parent
// makes a finished derived context attributable to opts.Timeout.
//
// The failure this shape exists to prevent: reading the parent's error looking
// for DeadlineExceeded. An opts.Timeout expiry leaves the parent with no error
// at all, so every deadline reports as a plain cancel and ErrExecTimeout becomes
// unreachable — the sentinel exists, and nothing can ever be it.
func classifyExecDone(op, timeoutMsg, cancelMsg string, parentErr, derivedErr error) error {
	// The parent's own error is returned rather than a fresh one, so a caller can
	// recover context.Canceled with errors.Is as the Runtime contract promises.
	if parentErr != nil {
		return E(op, cancelMsg, parentErr)
	}
	if errors.Is(derivedErr, context.DeadlineExceeded) {
		return E(op, timeoutMsg, ErrExecTimeout)
	}
	return E(op, cancelMsg, errors.New("exec command cancelled"))
}

func (d *DockerRuntime) List(ctx context.Context) ([]Info, error) {
	const op = "runtime.DockerRuntime.List"

	d.logger.DebugContext(ctx, "listing managed sandboxes")

	result, err := d.Client.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: labelFilter(managedMarkerLabels()),
	})
	if err != nil {
		return nil, E(op, "listing managed containers", err)
	}

	infos := make([]Info, 0, len(result.Items))
	for _, c := range result.Items {
		// A container we own but cannot name is skipped rather than fatal: one
		// corrupt id label would otherwise hide every healthy sandbox, and the
		// bad container is unreachable through the CLI precisely because it
		// cannot be listed. The warning keeps that otherwise silent skip visible.
		id, ok := managedSandboxID(c.Labels)
		if !ok {
			d.logger.WarnContext(ctx, "skipping managed container with invalid sandbox label", "containerID", c.ID)
			continue
		}
		infos = append(infos, NewInfo(id, stateFromContainerState(string(c.State)), createdAtFromUnix(c.Created)))
	}

	d.logger.DebugContext(ctx, "listed managed sandboxes", "count", len(infos))
	return infos, nil
}

func (d *DockerRuntime) Create(ctx context.Context, spec Spec) (Info, error) {
	const op = "runtime.DockerRuntime.Create"

	d.logger.DebugContext(ctx, "creating sandbox", "image", spec.Image)

	// The configs are built before the pull so an invalid spec fails without
	// spending a registry round trip on an image the caller can never run.
	platformID := newSandboxID()
	containerConfig, hostConfig, err := newContainerConfigs(spec, platformID)
	if err != nil {
		return Info{}, Wrap(op, "", err)
	}

	if err := d.pullImage(ctx, spec.Image); err != nil {
		return Info{}, Wrap(op, "", err)
	}

	created, err := d.Client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &containerConfig,
		HostConfig: &hostConfig,
	})
	if err != nil {
		// ContainerCreate does not pull, so an image absent from the daemon
		// surfaces here as a not-found rather than during pullImage.
		return Info{}, classifyNotFound(op, fmt.Sprintf("creating container from image %s", spec.Image), ErrImageMissing, err)
	}

	logger := d.logger.With(
		"sandboxID", platformID,
		"containerID", created.ID,
		"image", spec.Image,
	)
	logger.DebugContext(ctx, "created container")

	if _, err := d.Client.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return Info{}, d.cleanupFailed(ctx, op, platformID, created.ID,
			E(op, fmt.Sprintf("starting container %s for sandbox %s", created.ID, platformID), err))
	}

	// ContainerStart returning success is what "running" means here. Reading the
	// state back would cost a label lookup plus an inspect to learn something
	// already stale on arrival; callers who need the current state call Inspect.
	//
	// CreatedAt is the local clock rather than the daemon's: the only handle in
	// hand is the container id, and ContainerCreateResult carries no timestamp.
	// The two differ by one round trip, and List and Inspect report the daemon's.
	info := NewInfo(platformID, StateRunning, time.Now().UTC())
	logger.InfoContext(ctx, "sandbox created", "state", info.State)
	return info, nil
}

func (d *DockerRuntime) Exec(ctx context.Context, platformID string, cmd []string, opts ExecOpts) (ExecResult, error) {
	const op = "runtime.DockerRuntime.Exec"

	to := opts.Timeout
	if to <= 0 {
		to = DefaultExecTimeout
	}
	finalCtx, cancel := context.WithTimeout(ctx, to)
	defer cancel()

	// The cap is applied per destination stream rather than to the multiplexed
	// source: the source carries both payloads plus 8-byte frame headers under a
	// single budget, so a limit there bounds neither stream at MaxStreamBytes.
	// The buffers are declared up front so every failure path can hand back what
	// the command had written, and an ExitCode of -1 rather than 0 — a caller
	// reading only the code cannot mistake an abandoned command for a successful
	// one.
	stdout := newCappedWriter(MaxStreamBytes)
	stderr := newCappedWriter(MaxStreamBytes)
	result := func(exitCode int) ExecResult {
		return ExecResult{
			ExitCode:        exitCode,
			Stdout:          stdout.Bytes(),
			Stderr:          stderr.Bytes(),
			StdoutTruncated: stdout.Truncated(),
			StderrTruncated: stderr.Truncated(),
		}
	}
	partial := func() ExecResult { return result(-1) }

	// doneErr names why finalCtx finished. It is only meaningful once finalCtx is
	// Done. Classification happens where an operation failed rather than in
	// pre-flight checks before each daemon call: a pre-check can pass and the very
	// next call still die on the deadline, so checking at the failure site is the
	// only placement that catches every expiry.
	doneErr := func(stage string) error {
		return classifyExecDone(op,
			fmt.Sprintf("exec command timed out %s for sandbox %s", stage, platformID),
			fmt.Sprintf("exec command cancelled %s for sandbox %s", stage, platformID),
			ctx.Err(), finalCtx.Err())
	}

	// finalCtx, not ctx: the lookup is a daemon round trip, and on ctx it would
	// sit outside the only deadline the caller asked for.
	c, err := d.getContainerByPlatformID(finalCtx, platformID)
	if err != nil {
		if finalCtx.Err() != nil {
			return partial(), doneErr("while resolving the container")
		}
		return partial(), Wrap(op, "", err)
	}

	execConfig := client.ExecCreateOptions{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          cmd,
		Env:          envToArgs(opts.Env),
		WorkingDir:   opts.WorkDir,
	}

	execCreateResult, err := d.Client.ExecCreate(finalCtx, c.ID, execConfig)
	if err != nil {
		if finalCtx.Err() != nil {
			return partial(), doneErr("while creating the exec")
		}
		return partial(), E(op, fmt.Sprintf("executing command in container %s for sandbox %s", c.ID, platformID), err)
	}

	execAttachOpts := client.ExecAttachOptions{
		TTY: false,
	}

	// ExecAttach is what starts the exec: it posts /exec/{id}/start with
	// Detach=false and hijacks the connection, so there is no separate ExecStart
	// on this path.
	attachResult, err := d.Client.ExecAttach(finalCtx, execCreateResult.ID, execAttachOpts)
	if err != nil {
		if finalCtx.Err() != nil {
			return partial(), doneErr("while attaching to the exec")
		}
		return partial(), E(op, fmt.Sprintf("attached to exec command in container %s for sandbox %s", c.ID, platformID), err)
	}
	defer attachResult.Close()

	// abandon reaps the command the caller is giving up on and returns what it had
	// written. Every failure past the attach owes this: without it the process
	// keeps running in the container, which on a real platform is a bill.
	abandon := func(cause error) (ExecResult, error) {
		if killErr := d.killExec(ctx, c.ID, platformID, cmd, opts); killErr != nil {
			return partial(), errors.Join(cause, killErr)
		}
		return partial(), cause
	}

	// The copy runs on its own goroutine because the hijacked connection is a raw
	// net.Conn with no context wired into it — ExecAttach's ctx covers the request
	// that sets the connection up and nothing after. A blocked StdCopy is
	// therefore unreachable by cancellation, and running it inline makes
	// opts.Timeout unenforceable against exactly the commands it exists for: a
	// `sleep 300` produces no output and no EOF, so the call would block for the
	// full 300s and never reach the deadline check below it.
	copied := make(chan error, 1)
	go func() {
		// StdCopy reports a short frame at EOF as a clean end, not an error.
		_, e := stdcopy.StdCopy(stdout, stderr, attachResult.Reader)
		copied <- e
	}()

	var copyErr error
	select {
	case copyErr = <-copied:
	case <-finalCtx.Done():
		// Closing the connection is the only interrupt available; it fails the
		// blocked read and ends the goroutine. The result is then waited for
		// rather than abandoned, because that goroutine writes the very buffers
		// partial() reads — returning without it is a data race, not just a leak.
		attachResult.Close()
		<-copied
		return abandon(doneErr("while streaming output"))
	}

	if copyErr != nil {
		return abandon(E(op, fmt.Sprintf("streaming container stdout and stderr in container %s for sandbox %s", c.ID, platformID), copyErr))
	}

	exitCode, err := d.awaitExecExit(finalCtx, execCreateResult.ID)
	if err != nil {
		if finalCtx.Err() != nil {
			return abandon(doneErr("while awaiting the exit code"))
		}
		return abandon(E(op, fmt.Sprintf("inspecting exec status in container %s for sandbox %s", c.ID, platformID), err))
	}

	return result(exitCode), nil
}

// killExec reaps a command its caller is abandoning. The context is the caller's
// original, not the exec's timeout-bearing one, and is detached for the reason
// cleanupFailed documents: every call happens because that context just died, so
// a kill issued on it would never reach the daemon.
func (d *DockerRuntime) killExec(ctx context.Context, containerID, platformID string, cmd []string, opts ExecOpts) error {
	const op = "runtime.DockerRuntime.killExec"

	killCtx, cancelKill := context.WithTimeout(context.WithoutCancel(ctx), execKillTimeout)
	defer cancelKill()

	// No attach flags: nothing reads the kill's streams, and asking the daemon
	// to hold them open makes ExecStart wait on output that is immediately
	// discarded. The exit code, which is the part that matters, comes from
	// awaitExecExit.
	killExecConfig := client.ExecCreateOptions{
		Cmd:        pkillCommand(cmd),
		Env:        envToArgs(opts.Env),
		WorkingDir: opts.WorkDir,
	}

	killExecCreate, err := d.Client.ExecCreate(killCtx, containerID, killExecConfig)
	if err != nil {
		return E(op, fmt.Sprintf("creating kill command to stop exec command in container %s for sandbox %s", containerID, platformID), err)
	}

	if _, err := d.Client.ExecStart(killCtx, killExecCreate.ID, client.ExecStartOptions{}); err != nil {
		return E(op, fmt.Sprintf("starting kill command to stop exec command in container %s for sandbox %s", containerID, platformID), err)
	}

	exitCode, err := d.awaitExecExit(killCtx, killExecCreate.ID)
	if err != nil {
		return E(op, fmt.Sprintf("inspecting kill command results to stop exec command in container %s for sandbox %s", containerID, platformID), err)
	}
	if msg, ok := classifyPkillExit(exitCode); !ok {
		return E(op, fmt.Sprintf("stopping exec command in container %s for sandbox %s: %s", containerID, platformID, msg), ErrExecNotKilled)
	}

	return nil
}

// awaitExecExit polls because the Engine API has no wait-for-exec endpoint.
func (d *DockerRuntime) awaitExecExit(ctx context.Context, execID string) (int, error) {
	ticker := time.NewTicker(execPollInterval)
	defer ticker.Stop()

	for {
		result, err := d.Client.ExecInspect(ctx, execID, client.ExecInspectOptions{})
		if err != nil {
			return -1, err
		}
		if !result.Running {
			return result.ExitCode, nil
		}

		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		case <-ticker.C:
		}
	}
}

// pkillCommand builds the kill the Engine API cannot provide; a second exec
// signalling the first is the only lever left.
//
// pkill takes exactly one pattern and rejects extra operands, so the argv must
// be joined rather than spliced in. -f matches against /proc/<pid>/cmdline with
// its NULs rendered as spaces, hence the space join, and matches it as an
// extended regexp — so QuoteMeta plus -x is what keeps a command containing `.`
// or `[` from killing processes the caller never named.
func pkillCommand(cmd []string) []string {
	return []string{"pkill", "-f", "-x", "--", regexp.QuoteMeta(strings.Join(cmd, " "))}
}

// classifyPkillExit is receiver-free so the codes can be table-tested.
func classifyPkillExit(exitCode int) (string, bool) {
	switch exitCode {
	// 1 is "nothing matched" — the command exited on its own between the deadline
	// and the kill. Reporting that as a failure would taint nearly every timeout.
	case 0, 1:
		return "", true
	case 126, 127:
		// Distroless and scratch images have no pkill; the message has to say so
		// or the user goes looking for a bug in quickspin.
		return "pkill is not available in this image", false
	default:
		return fmt.Sprintf("pkill exited %d", exitCode), false
	}
}

// cleanupTimeout bounds the detached rollback in cleanupFailed. It is generous
// because a force-remove of a container that is starting can take a moment, and
// short enough that a wedged daemon does not hang the caller indefinitely.
const cleanupTimeout = 30 * time.Second

// cleanupFailed removes what an operation already made and returns the error to
// hand back. It addresses the container by id rather than by label because the
// caller never received a sandbox id, so the container id is the only handle
// anyone still holds. A cleanup failure is joined onto cause rather than
// replacing it: cause explains the failure, the join reports the leak.
//
// The removal runs on a context detached from the caller's. WithoutCancel keeps
// the caller's values — trace ids, log context — while shedding its
// cancellation, because rolling back with an already-cancelled context is a
// guaranteed leak: the caller cancelling mid-create is exactly when there is
// something to clean up. The fresh deadline is what keeps a detached call from
// outliving the process. This rule belongs to every backend, not just Docker;
// see docs/reference/runtime-backend-testing.mdx.
func (d *DockerRuntime) cleanupFailed(ctx context.Context, op, platformID, containerID string, cause error) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()

	if err := d.removeContainer(cleanupCtx, containerID); err != nil {
		return Wrap(op,
			fmt.Sprintf("sandbox %s leaked container %s", platformID, containerID),
			errors.Join(cause, err))
	}
	return cause
}

// pullImage drains the pull stream. ImagePull returns as soon as the daemon
// accepts the request, so the failures that matter — unknown repository, denied
// auth, a registry that disappears mid-transfer — arrive inside the stream and
// are reported by Wait. Discarding the response also leaks its connection.
func (d *DockerRuntime) pullImage(ctx context.Context, image string) error {
	const op = "runtime.DockerRuntime.pullImage"

	logger := d.logger.With("image", image)
	logger.DebugContext(ctx, "pulling image")

	resp, err := d.Client.ImagePull(ctx, image, client.ImagePullOptions{})
	if err != nil {
		return classifyNotFound(op, fmt.Sprintf("requesting pull of %s", image), ErrImageMissing, err)
	}
	defer resp.Close()

	if err := resp.Wait(ctx); err != nil {
		return classifyNotFound(op, fmt.Sprintf("pulling %s", image), ErrImageMissing, err)
	}

	logger.DebugContext(ctx, "pulled image")
	return nil
}

// removeContainer force-removes by container id and treats an already-absent
// container as success, so it is safe on any path that races another remove.
// Force covers the stop, so no separate ContainerStop call is needed.
func (d *DockerRuntime) removeContainer(ctx context.Context, containerID string) error {
	const op = "runtime.DockerRuntime.removeContainer"

	_, err := d.Client.ContainerRemove(ctx, containerID, client.ContainerRemoveOptions{Force: true})
	if err != nil && !errdefs.IsNotFound(err) {
		return E(op, fmt.Sprintf("removing container %s", containerID), err)
	}

	return nil
}

// Destroy of an unknown id returns nil; cleanup needs to be retry safe.
func (d *DockerRuntime) Destroy(ctx context.Context, platformID string) error {
	const op = "runtime.DockerRuntime.Destroy"

	c, err := d.getContainerByPlatformID(ctx, platformID)
	// Absent is success. A malformed id is not absent — it is a caller bug — so
	// ErrInvalidSandboxID still surfaces through the branch below.
	if errors.Is(err, ErrNotFound) {
		d.logger.DebugContext(ctx, "sandbox already absent", "sandboxID", platformID)
		return nil
	}
	if err != nil {
		return Wrap(op, "", err)
	}

	if err := d.removeContainer(ctx, c.ID); err != nil {
		return Wrap(op, fmt.Sprintf("destroying sandbox %s", platformID), err)
	}

	d.logger.InfoContext(ctx, "sandbox destroyed",
		"sandboxID", platformID,
		"containerID", c.ID,
	)
	return nil
}

func (d *DockerRuntime) Inspect(ctx context.Context, platformID string) (Info, error) {
	const op = "runtime.DockerRuntime.Inspect"

	// The listing already carries the status, so there is no ContainerInspect
	// here: it would be a second round trip decoding config, mounts and network
	// settings to re-read one string List reads straight off the summary.
	c, err := d.getContainerByPlatformID(ctx, platformID)
	if err != nil {
		return Info{}, Wrap(op, "", err)
	}

	info := NewInfo(platformID, stateFromContainerState(string(c.State)), createdAtFromUnix(c.Created))
	d.logger.DebugContext(ctx, "inspected sandbox",
		"sandboxID", platformID,
		"containerID", c.ID,
		"state", info.State,
	)
	return info, nil
}

func (d *DockerRuntime) getContainerByPlatformID(ctx context.Context, platformID string) (container.Summary, error) {
	const op = "runtime.DockerRuntime.getContainerByPlatformID"

	if err := validateSandboxID(platformID); err != nil {
		return container.Summary{}, E(op, fmt.Sprintf("resolving sandbox %q", platformID), err)
	}

	result, err := d.Client.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: labelFilter(managedLabels(platformID)),
	})
	if err != nil {
		return container.Summary{}, E(op, fmt.Sprintf("listing containers for sandbox %s", platformID), err)
	}

	// The daemon already filtered on both labels; re-checking here keeps a wrong
	// filter from silently widening the match.
	for _, c := range result.Items {
		if id, ok := managedSandboxID(c.Labels); ok && id == platformID {
			return c, nil
		}
	}

	return container.Summary{}, E(op, fmt.Sprintf("no container labelled %s=%s", labelSandboxID, platformID), ErrNotFound)
}
