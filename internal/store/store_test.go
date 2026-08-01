package store

import (
	"encoding/json"
	"errors"
	"maps"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCanTransitionMatrix(t *testing.T) {
	states := []TaskState{Pending, Running, Failed, Stopping, Stopped}
	legal := map[[2]TaskState]bool{
		{Pending, Running}:  true,
		{Pending, Failed}:   true,
		{Running, Failed}:   true,
		{Running, Stopping}: true,
		{Stopping, Stopped}: true,
	}

	for _, from := range states {
		for _, to := range states {
			t.Run(string(from)+"_to_"+string(to), func(t *testing.T) {
				err := canTransition(from, to)
				if legal[[2]TaskState{from, to}] {
					if err != nil {
						t.Errorf("canTransition(%q, %q) error = %v, want nil", from, to, err)
					}
					return
				}
				if !errors.Is(err, ErrInvalidStateTransition) {
					t.Errorf(
						"canTransition(%q, %q) error = %v, want ErrInvalidStateTransition",
						from,
						to,
						err,
					)
				}
			})
		}
	}
}

func TestCanTransitionRejectsUnknownStates(t *testing.T) {
	tests := []struct {
		name string
		from TaskState
		to   TaskState
	}{
		{name: "unknown source", from: "unknown", to: Running},
		{name: "unknown destination", from: Pending, to: "unknown"},
		{name: "both unknown", from: "unknown", to: "also-unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := canTransition(tt.from, tt.to); !errors.Is(err, ErrInvalidState) {
				t.Errorf(
					"canTransition(%q, %q) error = %v, want ErrInvalidState",
					tt.from,
					tt.to,
					err,
				)
			}
		})
	}
}

func TestSpecFileDecodesFromJSONAndYAML(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		decode func([]byte, any) error
	}{
		{
			name:   "JSON",
			decode: json.Unmarshal,
			input: `{
			"image": "alpine:3.20",
			"env": {"MODE": "test"},
			"cpus": 0.5,
			"memory": "64m",
			"pids-limit": 64,
			"allow-network": true
		}`,
		},
		{
			name:   "YAML",
			decode: yaml.Unmarshal,
			input: `image: alpine:3.20
env:
  MODE: test
cpus: 0.5
memory: 64m
pids-limit: 64
allow-network: true
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := &SpecFile{}
			if err := tt.decode([]byte(tt.input), spec); err != nil {
				t.Fatalf("%s decode error = %v, want nil", tt.name, err)
			}
			if err := spec.Validate(); err != nil {
				t.Fatalf("Validate() error = %v, want nil", err)
			}

			if spec.Image == nil || *spec.Image != "alpine:3.20" {
				t.Errorf("Image = %v, want alpine:3.20", spec.Image)
			}
			if !maps.Equal(spec.Env, map[string]string{"MODE": "test"}) {
				t.Errorf("Env = %#v, want MODE=test", spec.Env)
			}
			if spec.CPUs == nil || *spec.CPUs != 0.5 {
				t.Errorf("CPUs = %v, want 0.5", spec.CPUs)
			}
			if spec.Memory == nil || *spec.Memory != "64m" {
				t.Errorf("Memory = %v, want 64m", spec.Memory)
			}
			if spec.PidsLimit == nil || *spec.PidsLimit != 64 {
				t.Errorf("PidsLimit = %v, want 64", spec.PidsLimit)
			}
			if spec.AllowNetwork == nil || !*spec.AllowNetwork {
				t.Errorf("AllowNetwork = %v, want true", spec.AllowNetwork)
			}
		})
	}
}

func TestSpecFileDecodePreservesExplicitZeroValues(t *testing.T) {
	spec := &SpecFile{}
	if err := json.Unmarshal([]byte(`{"cpus":0,"pids-limit":0,"allow-network":false}`), spec); err != nil {
		t.Fatalf("json.Unmarshal() error = %v, want nil", err)
	}

	if spec.CPUs == nil || *spec.CPUs != 0 {
		t.Errorf("CPUs = %v, want an explicit pointer to 0", spec.CPUs)
	}
	if spec.PidsLimit == nil || *spec.PidsLimit != 0 {
		t.Errorf("PidsLimit = %v, want an explicit pointer to 0", spec.PidsLimit)
	}
	if spec.AllowNetwork == nil || *spec.AllowNetwork {
		t.Errorf("AllowNetwork = %v, want an explicit pointer to false", spec.AllowNetwork)
	}
}

func TestSpecFileValidateRejectsNilSpec(t *testing.T) {
	if err := (*SpecFile)(nil).Validate(); !errors.Is(err, ErrInvalidSpec) {
		t.Errorf("Validate() on nil error = %v, want ErrInvalidSpec", err)
	}
}

func TestSpecFileValidateAcceptsEmptySpec(t *testing.T) {
	if err := (&SpecFile{}).Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil", err)
	}
}

func TestSpecFileResolveUsesDefaultImage(t *testing.T) {
	resolved, err := (&SpecFile{}).Resolve()
	if err != nil {
		t.Fatalf("Resolve() error = %v, want nil", err)
	}
	if resolved.Image != DefaultImage {
		t.Errorf("Image = %q, want %q", resolved.Image, DefaultImage)
	}
}

func TestSpecFileToJSONRejectsNilReceiver(t *testing.T) {
	var spec *SpecFile

	if _, err := spec.ToJSON(); !errors.Is(err, ErrInvalidSpec) {
		t.Errorf("(*SpecFile)(nil).ToJSON() error = %v, want ErrInvalidSpec", err)
	}
}
