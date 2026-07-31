package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// The Engine API has no wait-for-exec endpoint, so exit codes are polled; each
// poll is a daemon round trip.
const execPollInterval = 50 * time.Millisecond

// execKillTimeout bounds the kill of a timed-out exec, which necessarily runs
// after ExecOpts.Timeout is already spent.
const execKillTimeout = 10 * time.Second

// fileCopyTimeout is sized for MaxFileSize over a local daemon socket, not for
// how long user commands may run — retuning DefaultExecTimeout must not retune
// file writes.
const fileCopyTimeout = 30 * time.Second

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

// newContainerConfigs is the one place Spec becomes Docker's vocabulary.
// Receiver-free so the field mapping can be tested without a live daemon.
func newContainerConfigs(spec Spec, sandboxID string) (container.Config, container.HostConfig, error) {
	const op = "runtime.newContainerConfigs"

	if err := spec.Validate(); err != nil {
		return container.Config{}, container.HostConfig{}, Wrap(op, "", err)
	}

	networkMode := container.NetworkMode("none")
	if spec.AllowNetwork {
		networkMode = container.NetworkMode("bridge")
	}

	return container.Config{
			Image: spec.Image,
			// Sandbox images must provide sleep with "infinity" support.
			Entrypoint: []string{"sleep", "infinity"},
			Env:        envToArgs(spec.Env),
			Labels:     managedLabels(sandboxID),
		}, container.HostConfig{
			RestartPolicy:   container.RestartPolicy{Name: container.RestartPolicyAlways},
			PublishAllPorts: true,
			NetworkMode:     networkMode,
			Resources: container.Resources{
				// Memory is bytes, NanoCPUs is cores × 10⁹; a units mistake here
				// becomes a wrong cgroup limit rather than an error.
				Memory:   spec.MemoryLimit,
				NanoCPUs: int64(spec.CPULimit * 1e9),
				// A nil PidsLimit means the daemon sets no pids.max at all. spec is a
				// value parameter, so this address is a private copy.
				PidsLimit: &spec.PidsLimit,
			},
		}, nil
}

// The filter term is the literal string "label" with a "key=value" value;
// passing a label key as the term matches nothing rather than erroring.
func labelFilter(labels map[string]string) client.Filters {
	f := client.Filters{}
	for k, v := range labels {
		f.Add("label", k+"="+v)
	}
	return f
}

// classifyNotFound substitutes sentinel when the daemon reported a not-found,
// folding the SDK's own text into Message. errdefs stays confined to this file;
// a caller importing it means the backend has leaked.
func classifyNotFound(op, message string, sentinel, err error) error {
	if errdefs.IsNotFound(err) {
		return E(op, fmt.Sprintf("%s: %v", message, err), sentinel)
	}
	return E(op, message, err)
}

// classifyExecDone names why an exec stopped early. parentErr wins: deriving
// the timeout context makes both Done at once, and a caller who cancelled
// should hear that, not "deadline". Checking the parent for DeadlineExceeded
// instead would make ErrExecTimeout unreachable — an opts.Timeout expiry leaves
// the parent with no error at all.
func classifyExecDone(op, timeoutMsg, cancelMsg string, parentErr, derivedErr error) error {
	// Returning the parent's own error keeps context.Canceled recoverable via
	// errors.Is, as the Runtime contract promises.
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
		// Skip rather than fail: one corrupt id label would otherwise hide
		// every healthy sandbox.
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

func (d *DockerRuntime) Create(ctx context.Context, sandboxID string, spec Spec) (Info, error) {
	const op = "runtime.DockerRuntime.Create"

	d.logger.DebugContext(ctx, "creating sandbox", "sandboxID", sandboxID, "image", spec.Image)

	if err := validateSandboxID(sandboxID); err != nil {
		return Info{}, E(op, fmt.Sprintf("creating sandbox %q", sandboxID), err)
	}

	// Configs are built before the pull so an invalid spec fails without a
	// registry round trip.
	containerConfig, hostConfig, err := newContainerConfigs(spec, sandboxID)
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
		"sandboxID", sandboxID,
		"containerID", created.ID,
		"image", spec.Image,
	)
	logger.DebugContext(ctx, "created container")

	if _, err := d.Client.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return Info{}, d.cleanupFailed(ctx, op, sandboxID, created.ID,
			E(op, fmt.Sprintf("starting container %s for sandbox %s", created.ID, sandboxID), err))
	}

	// ContainerStart succeeding is what "running" means here; callers who need
	// current state call Inspect. CreatedAt is the local clock because
	// ContainerCreateResult carries no timestamp; List and Inspect report the
	// daemon's.
	info := NewInfo(sandboxID, StateRunning, time.Now().UTC())
	logger.InfoContext(ctx, "sandbox created", "state", info.State)
	return info, nil
}

func (d *DockerRuntime) Exec(ctx context.Context, sandboxID string, cmd []string, opts ExecOpts) (ExecResult, error) {
	const op = "runtime.DockerRuntime.Exec"

	logger := d.logger.With("sandboxID", sandboxID)
	logger.DebugContext(ctx, "executing command", "cmd", strings.Join(cmd, " "))

	to := opts.Timeout
	if to <= 0 {
		to = DefaultExecTimeout
	}
	finalCtx, cancel := context.WithTimeout(ctx, to)
	defer cancel()

	// The cap is per destination stream: the multiplexed source carries both
	// payloads plus frame headers under one budget, so a limit there bounds
	// neither stream at MaxStreamBytes. Buffers are declared up front so every
	// failure path can return what the command wrote, with ExitCode -1 so an
	// abandoned command cannot read as success.
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

	// doneErr names why finalCtx finished; meaningful only once it is Done. It
	// is consulted at the failure site rather than in pre-flight checks — a
	// pre-check can pass and the very next call still die on the deadline.
	doneErr := func(stage string) error {
		return classifyExecDone(op,
			fmt.Sprintf("exec command timed out %s for sandbox %s", stage, sandboxID),
			fmt.Sprintf("exec command cancelled %s for sandbox %s", stage, sandboxID),
			ctx.Err(), finalCtx.Err())
	}

	// finalCtx, not ctx: the lookup should count against the exec deadline.
	c, err := d.getContainerBysandboxID(finalCtx, sandboxID)
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
		return partial(), E(op, fmt.Sprintf("executing command in container %s for sandbox %s", c.ID, sandboxID), err)
	}

	execAttachOpts := client.ExecAttachOptions{
		TTY: false,
	}

	// ExecAttach is what starts the exec — it posts start with Detach=false and
	// hijacks the connection — so there is no separate ExecStart on this path.
	attachResult, err := d.Client.ExecAttach(finalCtx, execCreateResult.ID, execAttachOpts)
	if err != nil {
		if finalCtx.Err() != nil {
			return partial(), doneErr("while attaching to the exec")
		}
		return partial(), E(op, fmt.Sprintf("attached to exec command in container %s for sandbox %s", c.ID, sandboxID), err)
	}
	defer attachResult.Close()

	// abandon reaps the command being given up on and returns what it had
	// written; without the kill the process keeps running in the container.
	abandon := func(cause error) (ExecResult, error) {
		if killErr := d.killExec(ctx, c.ID, sandboxID, cmd, opts); killErr != nil {
			return partial(), errors.Join(cause, killErr)
		}
		return partial(), cause
	}

	// The hijacked connection is a raw net.Conn with no context wired in, so a
	// blocked StdCopy is unreachable by cancellation. Run inline it would make
	// opts.Timeout unenforceable against exactly the commands it exists for —
	// those that produce no output and no EOF.
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
		// Closing the connection is the only interrupt for the blocked read.
		// The goroutine is then waited for because it writes the buffers
		// partial() reads — returning without it is a data race.
		attachResult.Close()
		<-copied
		return abandon(doneErr("while streaming output"))
	}

	if copyErr != nil {
		return abandon(E(op, fmt.Sprintf("streaming container stdout and stderr in container %s for sandbox %s", c.ID, sandboxID), copyErr))
	}

	exitCode, err := d.awaitExecExit(finalCtx, execCreateResult.ID)
	if err != nil {
		if finalCtx.Err() != nil {
			return abandon(doneErr("while awaiting the exit code"))
		}
		return abandon(E(op, fmt.Sprintf("inspecting exec status in container %s for sandbox %s", c.ID, sandboxID), err))
	}

	logger.DebugContext(ctx, "executed command",
		"containerID", c.ID,
		"exitCode", exitCode,
		"stdoutTruncated", stdout.Truncated(),
		"stderrTruncated", stderr.Truncated(),
	)
	return result(exitCode), nil
}

func (d *DockerRuntime) WriteFile(ctx context.Context, sandboxID string, filePath string, content []byte, mode fs.FileMode) error {
	const op = "runtime.DockerRuntime.WriteFile"

	logger := d.logger.With("sandboxID", sandboxID, "path", filePath)
	logger.DebugContext(ctx, "writing file", "size", len(content))

	if err := validateWrite(filePath, content); err != nil {
		return E(op, fmt.Sprintf("writing file %s for sandbox %s", filePath, sandboxID), err)
	}

	finalCtx, cancel := context.WithTimeout(ctx, fileCopyTimeout)
	defer cancel()

	// Lookup before archiving: a missing sandbox should not pay for a
	// MaxFileSize-scale allocation it can never use.
	c, err := d.getContainerBysandboxID(finalCtx, sandboxID)
	if err != nil {
		return Wrap(op, "", err)
	}

	archiveBytes, err := fileArchive(filePath, content, mode)
	if err != nil {
		return E(op, fmt.Sprintf("archiving file %s", filePath), err)
	}

	copyToContainerOpts := client.CopyToContainerOptions{
		DestinationPath:           "/",
		Content:                   bytes.NewReader(archiveBytes),
		AllowOverwriteDirWithFile: true,
		CopyUIDGID:                true,
	}

	if _, err := d.Client.CopyToContainer(finalCtx, c.ID, copyToContainerOpts); err != nil {
		return E(op, fmt.Sprintf("copying to container %s for sandbox %s", c.ID, sandboxID), err)
	}

	logger.DebugContext(ctx, "wrote file", "containerID", c.ID)
	return nil
}

func (d *DockerRuntime) ReadFile(ctx context.Context, sandboxID, filePath string) ([]byte, error) {
	const op = "runtime.DockerRuntime.ReadFile"

	logger := d.logger.With("sandboxID", sandboxID, "path", filePath)
	logger.DebugContext(ctx, "reading file")

	if err := validateRead(filePath); err != nil {
		return nil, E(op, fmt.Sprintf("reading file %s for sandbox %s", filePath, sandboxID), err)
	}

	finalCtx, cancel := context.WithTimeout(ctx, fileCopyTimeout)
	defer cancel()

	c, err := d.getContainerBysandboxID(finalCtx, sandboxID)
	if err != nil {
		return nil, Wrap(op, "", err)
	}

	res, err := d.Client.CopyFromContainer(finalCtx, c.ID, client.CopyFromContainerOptions{SourcePath: filePath})
	if err != nil {
		return nil, classifyNotFound(op, fmt.Sprintf("loading tar reader from container %s for sandbox %s", c.ID, sandboxID), ErrPathNotFound, err)
	}
	defer res.Content.Close()

	// Stat check bails before parsing the stream; fileUnarchive re-checks the
	// header to guard the allocation if the two disagree.
	if res.Stat.Size > MaxFileSize {
		return nil, Wrap(op, "", ErrFileTooLarge)
	}
	fileBytes, err := fileUnarchive(filePath, res.Content)
	if err != nil {
		return nil, E(op, fmt.Sprintf("unarchiving tar contents from container %s for sandbox %s", c.ID, sandboxID), err)
	}

	logger.DebugContext(ctx, "read file", "containerID", c.ID)
	return fileBytes, nil
}

func (d *DockerRuntime) ListDir(ctx context.Context, sandboxID, dirPath string) ([]FileInfo, error) {
	const op = "runtime.DockerRuntime.ListDir"

	logger := d.logger.With("sandboxID", sandboxID, "path", dirPath)
	logger.DebugContext(ctx, "listing directory")

	if err := validatePath(dirPath); err != nil {
		return nil, E(op, fmt.Sprintf("listing directory %s for sandbox %s", dirPath, sandboxID), err)
	}

	finalCtx, cancel := context.WithTimeout(ctx, fileCopyTimeout)
	defer cancel()

	c, err := d.getContainerBysandboxID(finalCtx, sandboxID)
	if err != nil {
		return nil, Wrap(op, "", err)
	}

	res, err := d.Client.CopyFromContainer(finalCtx, c.ID, client.CopyFromContainerOptions{SourcePath: dirPath})
	if err != nil {
		return nil, classifyNotFound(op, fmt.Sprintf("loading tar reader from container %s for sandbox %s", c.ID, sandboxID), ErrPathNotFound, err)
	}
	defer res.Content.Close()

	fileInfos, err := listDirectoryFromTarStream(dirPath, res.Content)
	if err != nil {
		return nil, E(op, fmt.Sprintf("listing directory from container %s for sandbox %s", c.ID, sandboxID), err)
	}

	logger.DebugContext(ctx, "listed directory", "containerID", c.ID)
	return fileInfos, nil
}

func (d *DockerRuntime) RemovePath(ctx context.Context, sandboxID, filePath string) error {
	const op = "runtime.DockerRuntime.RemovePath"

	logger := d.logger.With("sandboxID", sandboxID, "path", filePath)
	logger.DebugContext(ctx, "removing path")

	if err := validateRemove(filePath); err != nil {
		return E(op, fmt.Sprintf("removing path %s for sandbox %s", filePath, sandboxID), err)
	}

	execResult, err := d.Exec(ctx, sandboxID, []string{"rm", "-rf", filePath}, ExecOpts{})
	if err != nil {
		return Wrap(op, "", err)
	}

	if execResult.ExitCode != 0 {
		logger.WarnContext(ctx, "remove path command failed", "exitCode", execResult.ExitCode)
		return E(op, fmt.Sprintf("removing path %s for sandbox %s", filePath, sandboxID),
			fmt.Errorf("rm exited %d: %s", execResult.ExitCode, execResult.Stderr))
	}

	logger.DebugContext(ctx, "removed path")
	return nil
}

// killExec reaps a command its caller is abandoning. The kill runs on a
// detached context: every call happens because the exec's context just died,
// and a kill issued on it would never reach the daemon.
func (d *DockerRuntime) killExec(ctx context.Context, containerID, sandboxID string, cmd []string, opts ExecOpts) error {
	const op = "runtime.DockerRuntime.killExec"

	killCtx, cancelKill := context.WithTimeout(context.WithoutCancel(ctx), execKillTimeout)
	defer cancelKill()

	// No attach flags: nothing reads the kill's streams, and holding them open
	// makes ExecStart wait on output that is immediately discarded.
	killExecConfig := client.ExecCreateOptions{
		Cmd:        pkillCommand(cmd),
		Env:        envToArgs(opts.Env),
		WorkingDir: opts.WorkDir,
	}

	killExecCreate, err := d.Client.ExecCreate(killCtx, containerID, killExecConfig)
	if err != nil {
		return E(op, fmt.Sprintf("creating kill command to stop exec command in container %s for sandbox %s", containerID, sandboxID), err)
	}

	if _, err := d.Client.ExecStart(killCtx, killExecCreate.ID, client.ExecStartOptions{}); err != nil {
		return E(op, fmt.Sprintf("starting kill command to stop exec command in container %s for sandbox %s", containerID, sandboxID), err)
	}

	exitCode, err := d.awaitExecExit(killCtx, killExecCreate.ID)
	if err != nil {
		return E(op, fmt.Sprintf("inspecting kill command results to stop exec command in container %s for sandbox %s", containerID, sandboxID), err)
	}
	if msg, ok := classifyPkillExit(exitCode); !ok {
		return E(op, fmt.Sprintf("stopping exec command in container %s for sandbox %s: %s", containerID, sandboxID, msg), ErrExecNotKilled)
	}

	d.logger.DebugContext(ctx, "killed abandoned exec command",
		"sandboxID", sandboxID,
		"containerID", containerID,
	)
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

// pkillCommand signals the runaway exec via a second exec; the Engine API has
// no kill-exec endpoint. pkill takes exactly one pattern, so the argv is
// space-joined (-f sees /proc/<pid>/cmdline with NULs as spaces) and treated as
// an extended regexp — QuoteMeta plus -x keeps a command containing `.` or `[`
// from killing processes the caller never named.
func pkillCommand(cmd []string) []string {
	return []string{"pkill", "-f", "-x", "--", regexp.QuoteMeta(strings.Join(cmd, " "))}
}

func classifyPkillExit(exitCode int) (string, bool) {
	switch exitCode {
	// 1 is "nothing matched": the command exited on its own between the
	// deadline and the kill.
	case 0, 1:
		return "", true
	case 126, 127:
		// Distroless and scratch images have no pkill.
		return "pkill is not available in this image", false
	default:
		return fmt.Sprintf("pkill exited %d", exitCode), false
	}
}

// cleanupTimeout is generous because force-removing a starting container can
// take a moment, and finite so a wedged daemon cannot hang the caller.
const cleanupTimeout = 30 * time.Second

// cleanupFailed removes what a failed operation already made and returns cause.
// It addresses the container by id because the caller never received a sandbox
// id. A cleanup failure is joined onto cause: cause explains the failure, the
// join reports the leak. The removal runs detached — WithoutCancel plus a fresh
// deadline — because the caller cancelling mid-create is exactly when there is
// something to clean up; see docs/reference/runtime-backend-testing.mdx.
func (d *DockerRuntime) cleanupFailed(ctx context.Context, op, sandboxID, containerID string, cause error) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()

	if err := d.removeContainer(cleanupCtx, containerID); err != nil {
		return Wrap(op,
			fmt.Sprintf("sandbox %s leaked container %s", sandboxID, containerID),
			errors.Join(cause, err))
	}

	// cause is logged upstream; this is the only record that the container was
	// removed rather than leaked.
	d.logger.WarnContext(cleanupCtx, "removed container after failed operation",
		"sandboxID", sandboxID,
		"containerID", containerID,
	)
	return cause
}

// pullImage drains the pull stream: ImagePull returns once the daemon accepts
// the request, so the failures that matter arrive inside the stream and are
// reported by Wait. Discarding the response also leaks its connection.
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

// removeContainer treats an already-absent container as success, so it is safe
// on any path that races another remove. Force covers the stop.
func (d *DockerRuntime) removeContainer(ctx context.Context, containerID string) error {
	const op = "runtime.DockerRuntime.removeContainer"

	_, err := d.Client.ContainerRemove(ctx, containerID, client.ContainerRemoveOptions{Force: true})
	if err != nil && !errdefs.IsNotFound(err) {
		return E(op, fmt.Sprintf("removing container %s", containerID), err)
	}

	return nil
}

// Destroy of an unknown id returns nil; cleanup needs to be retry safe.
func (d *DockerRuntime) Destroy(ctx context.Context, sandboxID string) error {
	const op = "runtime.DockerRuntime.Destroy"

	d.logger.DebugContext(ctx, "destroying sandbox", "sandboxID", sandboxID)

	c, err := d.getContainerBysandboxID(ctx, sandboxID)
	// Absent is success; a malformed id is a caller bug and still errors below.
	if errors.Is(err, ErrNotFound) {
		d.logger.DebugContext(ctx, "sandbox already absent", "sandboxID", sandboxID)
		return nil
	}
	if err != nil {
		return Wrap(op, "", err)
	}

	if err := d.removeContainer(ctx, c.ID); err != nil {
		return Wrap(op, fmt.Sprintf("destroying sandbox %s", sandboxID), err)
	}

	d.logger.InfoContext(ctx, "sandbox destroyed",
		"sandboxID", sandboxID,
		"containerID", c.ID,
	)
	return nil
}

func (d *DockerRuntime) Inspect(ctx context.Context, sandboxID string) (Info, error) {
	const op = "runtime.DockerRuntime.Inspect"

	d.logger.DebugContext(ctx, "inspecting sandbox", "sandboxID", sandboxID)

	// The list summary already carries the state; ContainerInspect would be a
	// second round trip to re-read one string.
	c, err := d.getContainerBysandboxID(ctx, sandboxID)
	if err != nil {
		return Info{}, Wrap(op, "", err)
	}

	info := NewInfo(sandboxID, stateFromContainerState(string(c.State)), createdAtFromUnix(c.Created))
	d.logger.DebugContext(ctx, "inspected sandbox",
		"sandboxID", sandboxID,
		"containerID", c.ID,
		"state", info.State,
	)
	return info, nil
}

func (d *DockerRuntime) getContainerBysandboxID(ctx context.Context, sandboxID string) (container.Summary, error) {
	const op = "runtime.DockerRuntime.getContainerBysandboxID"

	if err := validateSandboxID(sandboxID); err != nil {
		return container.Summary{}, E(op, fmt.Sprintf("resolving sandbox %q", sandboxID), err)
	}

	result, err := d.Client.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: labelFilter(managedLabels(sandboxID)),
	})
	if err != nil {
		return container.Summary{}, E(op, fmt.Sprintf("listing containers for sandbox %s", sandboxID), err)
	}

	// The daemon already filtered on both labels; re-checking here keeps a wrong
	// filter from silently widening the match.
	for _, c := range result.Items {
		if id, ok := managedSandboxID(c.Labels); ok && id == sandboxID {
			return c, nil
		}
	}

	return container.Summary{}, E(op, fmt.Sprintf("no container labelled %s=%s", labelSandboxID, sandboxID), ErrNotFound)
}
