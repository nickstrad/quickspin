package store

import (
	"errors"
	"reflect"
	"testing"
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
				err := canTransition(string(from), string(to))
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
		from string
		to   string
	}{
		{name: "unknown source", from: "unknown", to: string(Running)},
		{name: "unknown destination", from: string(Pending), to: "unknown"},
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

func TestParseSpecFileAcceptsJSONAndYAML(t *testing.T) {
	tests := map[string]string{
		"JSON": `{
			"image": "alpine:3.20",
			"env": {"MODE": "test"},
			"cpus": 0.5,
			"memory": "64m",
			"pids-limit": 64,
			"allow-network": true
		}`,
		"YAML": `image: alpine:3.20
env:
  MODE: test
cpus: 0.5
memory: 64m
pids-limit: 64
allow-network: true
`,
	}

	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			spec, err := parseSpecFile(input)
			if err != nil {
				t.Fatalf("parseSpecFile() error = %v, want nil", err)
			}

			if spec.Image == nil || *spec.Image != "alpine:3.20" {
				t.Errorf("Image = %v, want alpine:3.20", spec.Image)
			}
			if !reflect.DeepEqual(spec.Env, map[string]string{"MODE": "test"}) {
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

func TestParseSpecFilePreservesExplicitZeroValues(t *testing.T) {
	spec, err := parseSpecFile(`{"cpus":0,"pids-limit":0,"allow-network":false}`)
	if err != nil {
		t.Fatalf("parseSpecFile() error = %v, want nil", err)
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

func TestParseSpecFileRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "empty", input: " \n\t"},
		{name: "no recognized fields", input: `{"unknown":"value"}`},
		{name: "malformed", input: `{"image":`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseSpecFile(tt.input); !errors.Is(err, ErrInvalidSpec) {
				t.Errorf("parseSpecFile(%q) error = %v, want ErrInvalidSpec", tt.input, err)
			}
		})
	}
}

func TestSpecFileToJSONRejectsNilReceiver(t *testing.T) {
	var spec *SpecFile

	if _, err := spec.ToJSON(); !errors.Is(err, ErrInvalidSpec) {
		t.Errorf("(*SpecFile)(nil).ToJSON() error = %v, want ErrInvalidSpec", err)
	}
}
