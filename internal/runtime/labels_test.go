package runtime

import (
	"maps"
	"testing"
)

// Valid ids in the format newSandboxID produces. Tests use these rather than
// inventing one, so tightening validateSandboxID cannot leave a hand-written
// fixture behind as a false failure.
const (
	testSandboxID      = "sbx_9f8e7d6c-5b4a-4938-8271-60514f3e2d1c"
	otherTestSandboxID = "sbx_2c1d0e9f-8a7b-4c6d-9e5f-4a3b2c1d0e9f"
)

func TestLabelKeysAreStable(t *testing.T) {
	// Literals on both sides: every other test uses the constants, so a typo in a
	// value would pass all of them.
	tests := []struct {
		name string
		got  string
		want string
	}{
		{name: "sandbox id label", got: labelSandboxID, want: "quickspin.id"},
		{name: "managed label", got: labelManaged, want: "quickspin.managed"},
		{name: "managed value", got: labelManagedValue, want: "true"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %q, want %q — renaming this orphans every container already labelled with it", tt.got, tt.want)
			}
		})
	}
}

func TestManagedLabelsWritesBothLabels(t *testing.T) {
	// Docker cannot add labels after create, so omitting one here is permanent.
	const id = testSandboxID

	got := managedLabels(id)
	want := map[string]string{
		labelSandboxID: id,
		labelManaged:   "true",
	}
	if !maps.Equal(got, want) {
		t.Errorf("managedLabels(%q) = %v, want %v", id, got, want)
	}
}

func TestManagedLabelsReturnsAFreshMap(t *testing.T) {
	// A shared package-level map would let one caller's mutation silently
	// relabel every later container.
	first := managedLabels(testSandboxID)
	first["injected"] = "yes"

	if second := managedLabels(otherTestSandboxID); second["injected"] != "" {
		t.Errorf("managedLabels() returned a map sharing state with a previous call: %v", second)
	}
}

func TestManagedLabelFilterMatchesOnlyOurs(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{
			name:   "container we created",
			labels: map[string]string{labelManaged: "true", labelSandboxID: testSandboxID},
			want:   true,
		},
		{
			name:   "nil labels",
			labels: nil,
		},
		{
			name:   "empty labels",
			labels: map[string]string{},
		},
		{
			name:   "unrelated container",
			labels: map[string]string{"com.docker.compose.project": "something"},
		},
		{
			name:   "managed explicitly false",
			labels: map[string]string{labelManaged: "false", labelSandboxID: testSandboxID},
		},
		{
			name:   "managed with an unexpected value",
			labels: map[string]string{labelManaged: "yes", labelSandboxID: testSandboxID},
		},
		{
			name:   "managed value is case sensitive",
			labels: map[string]string{labelManaged: "TRUE", labelSandboxID: testSandboxID},
		},
		{
			name:   "id label without the managed marker is not ours",
			labels: map[string]string{labelSandboxID: testSandboxID},
		},
		{
			// A half-labelled container is still ours, so the leak check can see
			// it. Rejecting the id is sandboxIDFromLabels' job.
			name:   "managed marker without an id is still ours",
			labels: map[string]string{labelManaged: "true"},
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isManaged(tt.labels); got != tt.want {
				t.Errorf("isManaged(%v) = %v, want %v", tt.labels, got, tt.want)
			}
		})
	}
}

func TestSandboxIDFromLabels(t *testing.T) {
	tests := []struct {
		name    string
		labels  map[string]string
		want    string
		wantErr bool
	}{
		{
			name:   "reads our id",
			labels: map[string]string{labelManaged: "true", labelSandboxID: testSandboxID},
			want:   testSandboxID,
		},
		{
			name:    "nil labels",
			labels:  nil,
			wantErr: true,
		},
		{
			name:    "id label absent",
			labels:  map[string]string{labelManaged: "true"},
			wantErr: true,
		},
		{
			name:    "id label empty",
			labels:  map[string]string{labelManaged: "true", labelSandboxID: ""},
			wantErr: true,
		},
		{
			// A hand-labelled impostor must not become an Info whose ID no later
			// Inspect or Destroy can resolve.
			name:    "id label malformed",
			labels:  map[string]string{labelManaged: "true", labelSandboxID: "not-a-sandbox"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := sandboxIDFromLabels(tt.labels)
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Fatalf("sandboxIDFromLabels(%v) error = %v, want error presence %v", tt.labels, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("sandboxIDFromLabels(%v) = %q, want %q", tt.labels, got, tt.want)
			}
		})
	}
}

func TestManagedLabelsRoundTripsThroughSandboxIDFromLabels(t *testing.T) {
	// Pins the key in both directions: renaming it in the writer only would
	// leave every container unreadable to its own reader.
	id := newSandboxID()

	got, err := sandboxIDFromLabels(managedLabels(id))
	if err != nil {
		t.Fatalf("sandboxIDFromLabels(managedLabels(%q)) error = %v, want nil", id, err)
	}
	if got != id {
		t.Errorf("round trip of %q = %q, want the original id", id, got)
	}
}
