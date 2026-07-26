package runtime

import (
	"context"
	"time"
)

type Spec struct {
	Image string
	Env   map[string]string
}

func NewSpec(img string, env map[string]string) Spec {
	return Spec{
		Env:   env,
		Image: img,
	}
}

type State string

const (
	StateRunning State = "running"
	StateStopped State = "stopped"
)

type Info struct {
	ID        string    `json:"id" yaml:"id"`
	State     State     `json:"state" yaml:"state"`
	CreatedAt time.Time `json:"created_at" yaml:"created_at"`
}

func NewInfo(id string, state State) Info {
	return Info{
		ID:        id,
		State:     state,
		CreatedAt: time.Now(),
	}
}

type Runtime interface {
	Create(ctx context.Context, spec Spec) (Info, error)
	Inspect(ctx context.Context, id string) (Info, error)
	List(ctx context.Context) ([]Info, error)
	// Destroy of an unknown id return nil; cleanup needs to be retry safe
	Destroy(ctx context.Context, id string) error
}
