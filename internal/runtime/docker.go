package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

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
func newContainerConfigs(spec Spec, platformID string) (container.Config, container.HostConfig) {
	return container.Config{
			Image:  spec.Image,
			Env:    envToArgs(spec.Env),
			Labels: managedLabels(platformID),
		}, container.HostConfig{
			RestartPolicy:   container.RestartPolicy{Name: container.RestartPolicyAlways},
			PublishAllPorts: true,
		}
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

	if err := d.pullImage(ctx, spec.Image); err != nil {
		return Info{}, Wrap(op, "", err)
	}

	platformID := newSandboxID()
	containerConfig, hostConfig := newContainerConfigs(spec, platformID)

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

// cleanupFailed removes what an operation already made and returns the error to
// hand back. It addresses the container by id rather than by label because the
// caller never received a sandbox id, so the container id is the only handle
// anyone still holds. A cleanup failure is joined onto cause rather than
// replacing it: cause explains the failure, the join reports the leak.
func (d *DockerRuntime) cleanupFailed(ctx context.Context, op, platformID, containerID string, cause error) error {
	if err := d.removeContainer(ctx, containerID); err != nil {
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
