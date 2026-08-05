package events

import (
	"errors"
	"testing"

	"github.com/nickstrad/quickspin/internal/sandbox"
)

func TestValidateRejectsAnIncompleteEvent(t *testing.T) {
	complete := Event{
		SandboxID: "sbx_a1b2c3",
		VersionID: 2,
		FromState: sandbox.Pending,
		ToState:   sandbox.Running,
		Reason:    "reconciler started the container",
	}

	tests := []struct {
		name    string
		mutate  func(*Event)
		wantErr bool
	}{
		{name: "complete event", mutate: func(*Event) {}},
		{name: "missing sandbox id", mutate: func(e *Event) { e.SandboxID = "" }, wantErr: true},
		{name: "missing version id", mutate: func(e *Event) { e.VersionID = 0 }, wantErr: true},
		{name: "missing to state", mutate: func(e *Event) { e.ToState = "" }, wantErr: true},
		{name: "missing reason", mutate: func(e *Event) { e.Reason = "" }, wantErr: true},
		{name: "missing from state records a creation", mutate: func(e *Event) { e.FromState = "" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := complete
			tt.mutate(&e)

			err := e.Validate()
			if tt.wantErr && !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("Validate() error = %v, want ErrInvalidEvent", err)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Validate() error = %v, want nil", err)
			}
		})
	}
}

func TestValidateRejectsANilEvent(t *testing.T) {
	var e *Event

	if err := e.Validate(); !errors.Is(err, ErrInvalidEvent) {
		t.Errorf("Validate() error = %v, want ErrInvalidEvent", err)
	}
}
