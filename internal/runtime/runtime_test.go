package runtime

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateSpecAcceptsTheMinimums(t *testing.T) {
	spec := NewSpec("alpine:3.20", nil, MinCPULimit, MinMemoryLimit, 1, false)

	if err := spec.Validate(); err != nil {
		t.Fatalf("Validate error = %v, want nil at exactly the minimums", err)
	}
}

func TestValidateSpecRejectsNonsense(t *testing.T) {
	valid := func() Spec { return NewSpec("alpine:3.20", nil, 0.5, MinMemoryLimit, 128, false) }

	tests := []struct {
		name    string
		mutate  func(*Spec)
		wantMsg string
	}{
		{
			name:    "no image",
			mutate:  func(s *Spec) { s.Image = "" },
			wantMsg: "image is required",
		},
		{
			// Zero is the dangerous case, not merely an absent one: Docker reads a
			// zero CPU, memory, or pids limit as unlimited, so a forgotten field
			// produces a sandbox with no ceiling and no error anywhere.
			name:    "cpu limit left at zero",
			mutate:  func(s *Spec) { s.CPULimit = 0 },
			wantMsg: "cpu limit",
		},
		{
			name:    "cpu limit below the daemon's floor",
			mutate:  func(s *Spec) { s.CPULimit = MinCPULimit / 2 },
			wantMsg: "cpu limit",
		},
		{
			name:    "memory limit left at zero",
			mutate:  func(s *Spec) { s.MemoryLimit = 0 },
			wantMsg: "memory limit",
		},
		{
			// Below Docker's 6MiB floor the daemon refuses the create; catching it
			// here costs no round trip and names the field rather than the API.
			name:    "memory limit below the daemon's floor",
			mutate:  func(s *Spec) { s.MemoryLimit = MinMemoryLimit - 1 },
			wantMsg: "memory limit",
		},
		{
			name:    "pids limit left at zero",
			mutate:  func(s *Spec) { s.PidsLimit = 0 },
			wantMsg: "pids limit",
		},
		{
			// -1 is Docker's own spelling of unlimited, so it has to be rejected
			// explicitly rather than passed through as a plausible-looking number.
			name:    "pids limit set to docker's unlimited",
			mutate:  func(s *Spec) { s.PidsLimit = -1 },
			wantMsg: "pids limit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := valid()
			tt.mutate(&spec)

			err := spec.Validate()
			if !errors.Is(err, ErrInvalidSpec) {
				t.Fatalf("Validate error = %v, want errors.Is(..., ErrInvalidSpec)", err)
			}
			// The sentinel says only that the spec is bad; the message is what
			// tells the caller which field to fix.
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("Validate error = %q, want it to name %q", err, tt.wantMsg)
			}
		})
	}
}
