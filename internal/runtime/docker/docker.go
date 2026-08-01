package docker

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"regexp"
	goruntime "runtime"
	"slices"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/nickstrad/quickspin/internal/runtime"
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

// gVisorRuntime is the name Docker knows gVisor by; it must match the key the
// daemon was configured with, which `runsc install` writes as "runsc".
const gVisorRuntime = "runsc"

// containerRuntimeEnv names the OCI runtime for the daemon this process talks
// to, overriding the GOOS default below. It exists because the two are
// independent: the Lima daemon is the same daemon whether the client that
// reached it was cross-compiled into the VM or run from the Mac.
const containerRuntimeEnv = "QUICKSPIN_DOCKER_RUNTIME"

// daemonDefaultRuntimeLabel keeps the empty string out of logs, where it reads as
// a dropped field rather than as the deliberate "let the daemon choose".
const daemonDefaultRuntimeLabel = "daemon-default"

// runtimeCheckTimeout bounds the one daemon round trip New makes; it is a
// liveness check on an already-open client, not a user-facing operation.
const runtimeCheckTimeout = 10 * time.Second

// defaultContainerRuntime picks gVisor on Linux and the daemon's own default
// elsewhere; GOOS is a proxy for "this daemon has runsc", since a darwin build
// reaches Docker Desktop or a forwarded socket that may not.
//
// The empty string means "send no Runtime field", which is not the same as
// sending "runc" — the daemon's configured default may be neither.
func defaultContainerRuntime() string {
	if name, ok := os.LookupEnv(containerRuntimeEnv); ok {
		return name
	}
	if goruntime.GOOS == "linux" {
		return gVisorRuntime
	}
	return ""
}

type Runtime struct {
	Client *client.Client
	logger *slog.Logger

	// containerRuntime is resolved once at construction so every sandbox in a
	// process lands on the same isolation boundary.
	containerRuntime string
}

var _ runtime.Runtime = (*Runtime)(nil)

func New(ctx context.Context, c *client.Client, logger *slog.Logger) (*Runtime, error) {
	if logger == nil {
		return nil, runtime.E("docker.New", "logger is required", nil)
	}

	if c == nil {
		fromEnv, err := client.New(client.FromEnv)
		if err != nil {
			return nil, runtime.E("docker.New", "creating docker client", err)
		}
		c = fromEnv
	}

	containerRuntime := defaultContainerRuntime()
	logger.Debug("selected container runtime", "containerRuntime", cmp.Or(containerRuntime, daemonDefaultRuntimeLabel))

	if err := checkRuntimeRegistered(ctx, c, containerRuntime); err != nil {
		return nil, err
	}

	return &Runtime{
		Client:           c,
		logger:           logger,
		containerRuntime: containerRuntime,
	}, nil
}

// ContainerRuntime reports the OCI runtime this Runtime asks the daemon for.
// Empty means the daemon's own default was left to stand.
func (d *Runtime) ContainerRuntime() string { return d.containerRuntime }

// checkRuntimeRegistered refuses to construct a Runtime when the daemon has no
// runtime by the selected name. Create would otherwise be the first thing to
// notice, and only for the request that hit it — a daemon missing runsc would
// keep serving sandboxes on whatever boundary it does have.
func checkRuntimeRegistered(ctx context.Context, c *client.Client, name string) error {
	const op = "docker.New"

	// Empty means the daemon picks, so there is no name to disprove.
	if name == "" {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, runtimeCheckTimeout)
	defer cancel()

	info, err := c.Info(ctx, client.InfoOptions{})
	if err != nil {
		return runtime.E(op, "asking the daemon which runtimes it has", err)
	}

	if _, ok := info.Info.Runtimes[name]; !ok {
		return runtime.E(op, fmt.Sprintf(
			"the daemon has no %q runtime; it offers %v", name, slices.Sorted(maps.Keys(info.Info.Runtimes))), nil)
	}
	return nil
}

// newContainerConfigs is the one place Spec becomes Docker's vocabulary.
// Receiver-free so the field mapping can be tested without a live daemon.
func newContainerConfigs(spec runtime.Spec, sandboxID, containerRuntime string) (container.Config, container.HostConfig, error) {
	const op = "docker.newContainerConfigs"

	if err := spec.Validate(); err != nil {
		return container.Config{}, container.HostConfig{}, runtime.Wrap(op, "", err)
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
			// The daemon rejects a name it has no runtime for, so a missing runsc
			// fails Create loudly rather than dropping to a weaker boundary.
			Runtime: containerRuntime,
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
		return runtime.E(op, fmt.Sprintf("%s: %v", message, err), sentinel)
	}
	return runtime.E(op, message, err)
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
		return runtime.E(op, cancelMsg, parentErr)
	}
	if errors.Is(derivedErr, context.DeadlineExceeded) {
		return runtime.E(op, timeoutMsg, runtime.ErrExecTimeout)
	}
	return runtime.E(op, cancelMsg, errors.New("exec command cancelled"))
}

func (d *Runtime) List(ctx context.Context) ([]runtime.Info, error) {
	const op = "docker.Runtime.List"

	d.logger.DebugContext(ctx, "listing managed sandboxes")

	result, err := d.Client.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: labelFilter(managedMarkerLabels()),
	})
	if err != nil {
		return nil, runtime.E(op, "listing managed containers", err)
	}

	infos := make([]runtime.Info, 0, len(result.Items))
	for _, c := range result.Items {
		// Skip rather than fail: one corrupt id label would otherwise hide
		// every healthy sandbox.
		id, ok := managedSandboxID(c.Labels)
		if !ok {
			d.logger.WarnContext(ctx, "skipping managed container with invalid sandbox label", "containerID", c.ID)
			continue
		}
		infos = append(infos, runtime.NewInfo(id, stateFromContainerState(string(c.State)), createdAtFromUnix(c.Created)))
	}

	d.logger.DebugContext(ctx, "listed managed sandboxes", "count", len(infos))
	return infos, nil
}

func (d *Runtime) Create(ctx context.Context, sandboxID string, spec runtime.Spec) (runtime.Info, error) {
	const op = "docker.Runtime.Create"

	d.logger.DebugContext(ctx, "creating sandbox", "sandboxID", sandboxID, "image", spec.Image)

	if err := runtime.ValidateSandboxID(sandboxID); err != nil {
		return runtime.Info{}, runtime.E(op, fmt.Sprintf("creating sandbox %q", sandboxID), err)
	}

	// Configs are built before the pull so an invalid spec fails without a
	// registry round trip.
	containerConfig, hostConfig, err := newContainerConfigs(spec, sandboxID, d.containerRuntime)
	if err != nil {
		return runtime.Info{}, runtime.Wrap(op, "", err)
	}

	if err := d.pullImage(ctx, spec.Image); err != nil {
		return runtime.Info{}, runtime.Wrap(op, "", err)
	}

	created, err := d.Client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &containerConfig,
		HostConfig: &hostConfig,
	})
	if err != nil {
		// ContainerCreate does not pull, so an image absent from the daemon
		// surfaces here as a not-found rather than during pullImage.
		return runtime.Info{}, classifyNotFound(op, fmt.Sprintf("creating container from image %s", spec.Image), runtime.ErrImageMissing, err)
	}

	logger := d.logger.With(
		"sandboxID", sandboxID,
		"containerID", created.ID,
		"image", spec.Image,
		"containerRuntime", cmp.Or(d.containerRuntime, daemonDefaultRuntimeLabel),
	)
	logger.DebugContext(ctx, "created container")

	if _, err := d.Client.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return runtime.Info{}, d.cleanupFailed(ctx, op, sandboxID, created.ID,
			runtime.E(op, fmt.Sprintf("starting container %s for sandbox %s", created.ID, sandboxID), err))
	}

	// ContainerStart succeeding is what "running" means here; callers who need
	// current state call Inspect. CreatedAt is the local clock because
	// ContainerCreateResult carries no timestamp; List and Inspect report the
	// daemon's.
	info := runtime.NewInfo(sandboxID, runtime.StateRunning, time.Now().UTC())
	logger.InfoContext(ctx, "sandbox created", "state", info.State)
	return info, nil
}

func (d *Runtime) Exec(ctx context.Context, sandboxID string, cmd []string, opts runtime.ExecOpts) (runtime.ExecResult, error) {
	const op = "docker.Runtime.Exec"

	logger := d.logger.With("sandboxID", sandboxID)
	logger.DebugContext(ctx, "executing command", "cmd", strings.Join(cmd, " "))

	to := opts.Timeout
	if to <= 0 {
		to = runtime.DefaultExecTimeout
	}
	finalCtx, cancel := context.WithTimeout(ctx, to)
	defer cancel()

	// The cap is per destination stream: the multiplexed source carries both
	// payloads plus frame headers under one budget, so a limit there bounds
	// neither stream at MaxStreamBytes. Buffers are declared up front so every
	// failure path can return what the command wrote, with ExitCode -1 so an
	// abandoned command cannot read as success.
	stdout := newCappedWriter(runtime.MaxStreamBytes)
	stderr := newCappedWriter(runtime.MaxStreamBytes)
	result := func(exitCode int) runtime.ExecResult {
		return runtime.ExecResult{
			ExitCode:        exitCode,
			Stdout:          stdout.Bytes(),
			Stderr:          stderr.Bytes(),
			StdoutTruncated: stdout.Truncated(),
			StderrTruncated: stderr.Truncated(),
		}
	}
	partial := func() runtime.ExecResult { return result(-1) }

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
		return partial(), runtime.Wrap(op, "", err)
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
		return partial(), runtime.E(op, fmt.Sprintf("executing command in container %s for sandbox %s", c.ID, sandboxID), err)
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
		return partial(), runtime.E(op, fmt.Sprintf("attached to exec command in container %s for sandbox %s", c.ID, sandboxID), err)
	}
	defer attachResult.Close()

	// abandon reaps the command being given up on and returns what it had
	// written; without the kill the process keeps running in the container.
	abandon := func(cause error) (runtime.ExecResult, error) {
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
		return abandon(runtime.E(op, fmt.Sprintf("streaming container stdout and stderr in container %s for sandbox %s", c.ID, sandboxID), copyErr))
	}

	exitCode, err := d.awaitExecExit(finalCtx, execCreateResult.ID)
	if err != nil {
		if finalCtx.Err() != nil {
			return abandon(doneErr("while awaiting the exit code"))
		}
		return abandon(runtime.E(op, fmt.Sprintf("inspecting exec status in container %s for sandbox %s", c.ID, sandboxID), err))
	}

	logger.DebugContext(ctx, "executed command",
		"containerID", c.ID,
		"exitCode", exitCode,
		"stdoutTruncated", stdout.Truncated(),
		"stderrTruncated", stderr.Truncated(),
	)
	return result(exitCode), nil
}

func (d *Runtime) WriteFile(ctx context.Context, sandboxID string, filePath string, content []byte, mode fs.FileMode) error {
	const op = "docker.Runtime.WriteFile"

	logger := d.logger.With("sandboxID", sandboxID, "path", filePath)
	logger.DebugContext(ctx, "writing file", "size", len(content))

	if err := runtime.ValidateWrite(filePath, content); err != nil {
		return runtime.E(op, fmt.Sprintf("writing file %s for sandbox %s", filePath, sandboxID), err)
	}

	finalCtx, cancel := context.WithTimeout(ctx, fileCopyTimeout)
	defer cancel()

	// Lookup before archiving: a missing sandbox should not pay for a
	// MaxFileSize-scale allocation it can never use.
	c, err := d.getContainerBysandboxID(finalCtx, sandboxID)
	if err != nil {
		return runtime.Wrap(op, "", err)
	}

	archiveBytes, err := fileArchive(filePath, content, mode)
	if err != nil {
		return runtime.E(op, fmt.Sprintf("archiving file %s", filePath), err)
	}

	copyToContainerOpts := client.CopyToContainerOptions{
		DestinationPath:           "/",
		Content:                   bytes.NewReader(archiveBytes),
		AllowOverwriteDirWithFile: true,
		CopyUIDGID:                true,
	}

	if _, err := d.Client.CopyToContainer(finalCtx, c.ID, copyToContainerOpts); err != nil {
		return runtime.E(op, fmt.Sprintf("copying to container %s for sandbox %s", c.ID, sandboxID), err)
	}

	logger.DebugContext(ctx, "wrote file", "containerID", c.ID)
	return nil
}

func (d *Runtime) ReadFile(ctx context.Context, sandboxID, filePath string) ([]byte, error) {
	const op = "docker.Runtime.ReadFile"

	logger := d.logger.With("sandboxID", sandboxID, "path", filePath)
	logger.DebugContext(ctx, "reading file")

	if err := runtime.ValidateRead(filePath); err != nil {
		return nil, runtime.E(op, fmt.Sprintf("reading file %s for sandbox %s", filePath, sandboxID), err)
	}

	finalCtx, cancel := context.WithTimeout(ctx, fileCopyTimeout)
	defer cancel()

	c, err := d.getContainerBysandboxID(finalCtx, sandboxID)
	if err != nil {
		return nil, runtime.Wrap(op, "", err)
	}

	res, err := d.Client.CopyFromContainer(finalCtx, c.ID, client.CopyFromContainerOptions{SourcePath: filePath})
	if err != nil {
		return nil, classifyNotFound(op, fmt.Sprintf("loading tar reader from container %s for sandbox %s", c.ID, sandboxID), runtime.ErrPathNotFound, err)
	}
	defer res.Content.Close()

	// Stat check bails before parsing the stream; fileUnarchive re-checks the
	// header to guard the allocation if the two disagree.
	if res.Stat.Size > runtime.MaxFileSize {
		return nil, runtime.Wrap(op, "", runtime.ErrFileTooLarge)
	}
	fileBytes, err := fileUnarchive(filePath, res.Content)
	if err != nil {
		return nil, runtime.E(op, fmt.Sprintf("unarchiving tar contents from container %s for sandbox %s", c.ID, sandboxID), err)
	}

	logger.DebugContext(ctx, "read file", "containerID", c.ID)
	return fileBytes, nil
}

func (d *Runtime) ListDir(ctx context.Context, sandboxID, dirPath string) ([]runtime.FileInfo, error) {
	const op = "docker.Runtime.ListDir"

	logger := d.logger.With("sandboxID", sandboxID, "path", dirPath)
	logger.DebugContext(ctx, "listing directory")

	if err := runtime.ValidatePath(dirPath); err != nil {
		return nil, runtime.E(op, fmt.Sprintf("listing directory %s for sandbox %s", dirPath, sandboxID), err)
	}

	finalCtx, cancel := context.WithTimeout(ctx, fileCopyTimeout)
	defer cancel()

	c, err := d.getContainerBysandboxID(finalCtx, sandboxID)
	if err != nil {
		return nil, runtime.Wrap(op, "", err)
	}

	res, err := d.Client.CopyFromContainer(finalCtx, c.ID, client.CopyFromContainerOptions{SourcePath: dirPath})
	if err != nil {
		return nil, classifyNotFound(op, fmt.Sprintf("loading tar reader from container %s for sandbox %s", c.ID, sandboxID), runtime.ErrPathNotFound, err)
	}
	defer res.Content.Close()

	fileInfos, err := listDirectoryFromTarStream(dirPath, res.Content)
	if err != nil {
		return nil, runtime.E(op, fmt.Sprintf("listing directory from container %s for sandbox %s", c.ID, sandboxID), err)
	}

	logger.DebugContext(ctx, "listed directory", "containerID", c.ID)
	return fileInfos, nil
}

func (d *Runtime) RemovePath(ctx context.Context, sandboxID, filePath string) error {
	const op = "docker.Runtime.RemovePath"

	logger := d.logger.With("sandboxID", sandboxID, "path", filePath)
	logger.DebugContext(ctx, "removing path")

	if err := runtime.ValidateRemove(filePath); err != nil {
		return runtime.E(op, fmt.Sprintf("removing path %s for sandbox %s", filePath, sandboxID), err)
	}

	execResult, err := d.Exec(ctx, sandboxID, []string{"rm", "-rf", filePath}, runtime.ExecOpts{})
	if err != nil {
		return runtime.Wrap(op, "", err)
	}

	if execResult.ExitCode != 0 {
		logger.WarnContext(ctx, "remove path command failed", "exitCode", execResult.ExitCode)
		return runtime.E(op, fmt.Sprintf("removing path %s for sandbox %s", filePath, sandboxID),
			fmt.Errorf("rm exited %d: %s", execResult.ExitCode, execResult.Stderr))
	}

	logger.DebugContext(ctx, "removed path")
	return nil
}

// killExec reaps a command its caller is abandoning. The kill runs on a
// detached context: every call happens because the exec's context just died,
// and a kill issued on it would never reach the daemon.
func (d *Runtime) killExec(ctx context.Context, containerID, sandboxID string, cmd []string, opts runtime.ExecOpts) error {
	const op = "docker.Runtime.killExec"

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
		return runtime.E(op, fmt.Sprintf("creating kill command to stop exec command in container %s for sandbox %s", containerID, sandboxID), err)
	}

	if _, err := d.Client.ExecStart(killCtx, killExecCreate.ID, client.ExecStartOptions{}); err != nil {
		return runtime.E(op, fmt.Sprintf("starting kill command to stop exec command in container %s for sandbox %s", containerID, sandboxID), err)
	}

	exitCode, err := d.awaitExecExit(killCtx, killExecCreate.ID)
	if err != nil {
		return runtime.E(op, fmt.Sprintf("inspecting kill command results to stop exec command in container %s for sandbox %s", containerID, sandboxID), err)
	}
	if msg, ok := classifyPkillExit(exitCode); !ok {
		return runtime.E(op, fmt.Sprintf("stopping exec command in container %s for sandbox %s: %s", containerID, sandboxID, msg), runtime.ErrExecNotKilled)
	}

	d.logger.DebugContext(ctx, "killed abandoned exec command",
		"sandboxID", sandboxID,
		"containerID", containerID,
	)
	return nil
}

// awaitExecExit polls because the Engine API has no wait-for-exec endpoint.
func (d *Runtime) awaitExecExit(ctx context.Context, execID string) (int, error) {
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
func (d *Runtime) cleanupFailed(ctx context.Context, op, sandboxID, containerID string, cause error) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()

	if err := d.removeContainer(cleanupCtx, containerID); err != nil {
		return runtime.Wrap(op,
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
func (d *Runtime) pullImage(ctx context.Context, image string) error {
	const op = "docker.Runtime.pullImage"

	logger := d.logger.With("image", image)
	logger.DebugContext(ctx, "pulling image")

	resp, err := d.Client.ImagePull(ctx, image, client.ImagePullOptions{})
	if err != nil {
		return classifyNotFound(op, fmt.Sprintf("requesting pull of %s", image), runtime.ErrImageMissing, err)
	}
	defer resp.Close()

	if err := resp.Wait(ctx); err != nil {
		return classifyNotFound(op, fmt.Sprintf("pulling %s", image), runtime.ErrImageMissing, err)
	}

	logger.DebugContext(ctx, "pulled image")
	return nil
}

// removeContainer treats an already-absent container as success, so it is safe
// on any path that races another remove. Force covers the stop.
func (d *Runtime) removeContainer(ctx context.Context, containerID string) error {
	const op = "docker.Runtime.removeContainer"

	_, err := d.Client.ContainerRemove(ctx, containerID, client.ContainerRemoveOptions{Force: true})
	if err != nil && !errdefs.IsNotFound(err) {
		return runtime.E(op, fmt.Sprintf("removing container %s", containerID), err)
	}

	return nil
}

// Destroy of an unknown id returns nil; cleanup needs to be retry safe.
func (d *Runtime) Destroy(ctx context.Context, sandboxID string) error {
	const op = "docker.Runtime.Destroy"

	d.logger.DebugContext(ctx, "destroying sandbox", "sandboxID", sandboxID)

	c, err := d.getContainerBysandboxID(ctx, sandboxID)
	// Absent is success; a malformed id is a caller bug and still errors below.
	if errors.Is(err, runtime.ErrNotFound) {
		d.logger.DebugContext(ctx, "sandbox already absent", "sandboxID", sandboxID)
		return nil
	}
	if err != nil {
		return runtime.Wrap(op, "", err)
	}

	if err := d.removeContainer(ctx, c.ID); err != nil {
		return runtime.Wrap(op, fmt.Sprintf("destroying sandbox %s", sandboxID), err)
	}

	d.logger.InfoContext(ctx, "sandbox destroyed",
		"sandboxID", sandboxID,
		"containerID", c.ID,
	)
	return nil
}

func (d *Runtime) Inspect(ctx context.Context, sandboxID string) (runtime.Info, error) {
	const op = "docker.Runtime.Inspect"

	d.logger.DebugContext(ctx, "inspecting sandbox", "sandboxID", sandboxID)

	// The list summary already carries the state; ContainerInspect would be a
	// second round trip to re-read one string.
	c, err := d.getContainerBysandboxID(ctx, sandboxID)
	if err != nil {
		return runtime.Info{}, runtime.Wrap(op, "", err)
	}

	info := runtime.NewInfo(sandboxID, stateFromContainerState(string(c.State)), createdAtFromUnix(c.Created))
	d.logger.DebugContext(ctx, "inspected sandbox",
		"sandboxID", sandboxID,
		"containerID", c.ID,
		"state", info.State,
	)
	return info, nil
}

func (d *Runtime) getContainerBysandboxID(ctx context.Context, sandboxID string) (container.Summary, error) {
	const op = "docker.Runtime.getContainerBysandboxID"

	if err := runtime.ValidateSandboxID(sandboxID); err != nil {
		return container.Summary{}, runtime.E(op, fmt.Sprintf("resolving sandbox %q", sandboxID), err)
	}

	result, err := d.Client.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: labelFilter(managedLabels(sandboxID)),
	})
	if err != nil {
		return container.Summary{}, runtime.E(op, fmt.Sprintf("listing containers for sandbox %s", sandboxID), err)
	}

	// The daemon already filtered on both labels; re-checking here keeps a wrong
	// filter from silently widening the match.
	for _, c := range result.Items {
		if id, ok := managedSandboxID(c.Labels); ok && id == sandboxID {
			return c, nil
		}
	}

	return container.Summary{}, runtime.E(op, fmt.Sprintf("no container labelled %s=%s", labelSandboxID, sandboxID), runtime.ErrNotFound)
}
