package runtime

import (
	"maps"
	"reflect"
	"slices"
	"testing"
)

func TestStateFromContainerState(t *testing.T) {
	// Every status Docker can report gets a row, so this table is the coverage
	// check: a status missing from it is one whose answer nobody wrote down.
	tests := []struct {
		name      string
		container string
		want      State
	}{
		{name: "running", container: "running", want: StateRunning},
		{name: "created but not started", container: "created", want: StateStopped},
		{name: "exited", container: "exited", want: StateStopped},
		{name: "dead", container: "dead", want: StateStopped},
		{name: "removing", container: "removing", want: StateStopped},
		{name: "paused is not running", container: "paused", want: StateStopped},
		// A restart is transient and self-healing, so reporting it stopped would
		// have plan 06's reconciler act on a condition that fixes itself.
		{name: "restarting counts as running", container: "restarting", want: StateRunning},
		{name: "unknown state falls back to stopped", container: "something-new", want: StateStopped},
		{name: "empty state falls back to stopped", container: "", want: StateStopped},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stateFromContainerState(tt.container); got != tt.want {
				t.Errorf("stateFromContainerState(%q) = %q, want %q", tt.container, got, tt.want)
			}
		})
	}
}

func TestEveryStartedStateMapsToRunning(t *testing.T) {
	// Binds the list to the mapping: adding a status to one without the other
	// shows up here.
	for _, status := range startedStates {
		if got := stateFromContainerState(status); got != StateRunning {
			t.Errorf("stateFromContainerState(%q) = %q, want %q: it is listed in startedStates", status, got, StateRunning)
		}
	}
}

func TestEnvToArgs(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want []string
	}{
		{name: "single pair", env: map[string]string{"FOO": "bar"}, want: []string{"FOO=bar"}},
		{
			// Go randomizes map iteration, so unsorted output differs run to run.
			name: "multiple pairs are sorted by key",
			env:  map[string]string{"ZED": "3", "ALPHA": "1", "MID": "2"},
			want: []string{"ALPHA=1", "MID=2", "ZED=3"},
		},
		{
			// The only rows that distinguish sorting keys from sorting the joined
			// strings. "=" (0x3D) sorts after the digits and before "_", so joining
			// first yields FOO2= < FOO= < FOO_A=. Every other row here uses keys
			// where none is a prefix of another and passes either way.
			name: "a key that is a prefix of another sorts by key, not by joined string",
			env:  map[string]string{"FOO_A": "y", "FOO2": "x", "FOO": "bar"},
			want: []string{"FOO=bar", "FOO2=x", "FOO_A=y"},
		},
		{
			name: "prefix key with a digit suffix",
			env:  map[string]string{"PATH": "/bin", "PATH2": "/sbin"},
			want: []string{"PATH=/bin", "PATH2=/sbin"},
		},
		{name: "empty value is preserved", env: map[string]string{"FOO": ""}, want: []string{"FOO="}},
		{name: "value containing equals is preserved", env: map[string]string{"EXPR": "a=b"}, want: []string{"EXPR=a=b"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := envToArgs(tt.env); !slices.Equal(got, tt.want) {
				t.Errorf("envToArgs(%v) = %v, want %v", tt.env, got, tt.want)
			}
		})
	}
}

func TestEnvToArgsEmptyInputYieldsNoArgs(t *testing.T) {
	// Not a row above, because slices.Equal treats nil and empty as equal and
	// could not pin either. Which one comes back is deliberately out of contract
	// — container.Config.Env treats them the same — but neither input may invent
	// an argument.
	tests := []struct {
		name string
		env  map[string]string
	}{
		{name: "nil map", env: nil},
		{name: "empty map", env: map[string]string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := envToArgs(tt.env); len(got) != 0 {
				t.Errorf("envToArgs(%v) = %v, want no args", tt.env, got)
			}
		})
	}
}

func TestEnvToArgsDoesNotMutateItsInput(t *testing.T) {
	env := map[string]string{"FOO": "bar", "BAZ": "qux"}
	before := maps.Clone(env)

	envToArgs(env)

	if !reflect.DeepEqual(env, before) {
		t.Errorf("envToArgs mutated its argument: got %v, want %v", env, before)
	}
}
