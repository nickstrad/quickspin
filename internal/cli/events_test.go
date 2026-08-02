package cli_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/quickspin/internal/events"
	"github.com/nickstrad/quickspin/internal/sandbox"
)

func lifecycleEvents() []*events.Event {
	return []*events.Event{
		{
			SandboxID: testID,
			ToState:   sandbox.Pending,
			At:        testTime,
			Reason:    "sandbox record created",
		},
		{
			SandboxID: testID,
			FromState: sandbox.Pending,
			ToState:   sandbox.Running,
			At:        testTime.Add(time.Second),
			Reason:    "container observed running",
		},
	}
}

func TestEventsWritesStableJSONInAppendOrder(t *testing.T) {
	var gotID string
	api := fakeAPI{
		EventsFn: func(_ context.Context, id string) ([]*events.Event, error) {
			gotID = id
			return lifecycleEvents(), nil
		},
	}

	stdout, _, err := execute(t, api, "sandbox", "events", testID, "--output", "json")
	if err != nil {
		t.Fatalf("execute events error = %v, want nil", err)
	}
	if gotID != testID {
		t.Errorf("GetSandboxEvents id = %q, want %q", gotID, testID)
	}

	var got []*events.Event
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode events output %q: %v", stdout, err)
	}
	if len(got) != 2 || got[0].ToState != sandbox.Pending || got[1].ToState != sandbox.Running {
		t.Fatalf("events output = %+v, want create then running", got)
	}
	if got[1].Reason != "container observed running" || !got[1].At.Equal(testTime.Add(time.Second)) {
		t.Errorf("second event = %+v, want its reason and timestamp intact", got[1])
	}
}

func TestEventsTablePreservesAppendOrder(t *testing.T) {
	api := fakeAPI{
		EventsFn: func(context.Context, string) ([]*events.Event, error) {
			return lifecycleEvents(), nil
		},
	}

	stdout, _, err := execute(t, api, "sandbox", "events", testID)
	if err != nil {
		t.Fatalf("execute events error = %v, want nil", err)
	}
	if !strings.Contains(stdout, "SANDBOX ID") || !strings.Contains(stdout, "FROM") || !strings.Contains(stdout, "REASON") {
		t.Errorf("events table header = %q, want lifecycle columns", stdout)
	}
	created := strings.Index(stdout, "sandbox record created")
	running := strings.Index(stdout, "container observed running")
	if created < 0 || running < 0 || created >= running {
		t.Errorf("events table = %q, want creation before running", stdout)
	}
}

func TestEmptyEventsWritesAnEmptyJSONArray(t *testing.T) {
	api := fakeAPI{
		EventsFn: func(context.Context, string) ([]*events.Event, error) {
			return nil, nil
		},
	}

	stdout, _, err := execute(t, api, "sandbox", "events", testID, "-o", "json")
	if err != nil {
		t.Fatalf("execute events error = %v, want nil", err)
	}
	if stdout != "[]\n" {
		t.Errorf("empty events output = %q, want %q", stdout, "[]\n")
	}
}

func TestEventsWrapsTheAPIError(t *testing.T) {
	want := errors.New("events unavailable")
	api := fakeAPI{
		EventsFn: func(context.Context, string) ([]*events.Event, error) {
			return nil, want
		},
	}

	stdout, _, err := execute(t, api, "sandbox", "events", testID)
	if !errors.Is(err, want) {
		t.Fatalf("execute events error = %v, want wrapped API error", err)
	}
	if got := err.Error(); got != `list events for sandbox "`+testID+`": events unavailable` {
		t.Errorf("execute events error = %q, want command and sandbox context", got)
	}
	if stdout != "" {
		t.Errorf("events output = %q after API error, want empty", stdout)
	}
}
