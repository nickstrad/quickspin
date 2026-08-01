package docker

import (
	"maps"
	"testing"

	"github.com/nickstrad/quickspin/internal/runtime"
)

// Valid ids in the format NewSandboxID produces. Tests use these rather than
// inventing one, so tightening runtime.ValidateSandboxID cannot leave a hand-written
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
	id := runtime.NewSandboxID()

	got, err := sandboxIDFromLabels(managedLabels(id))
	if err != nil {
		t.Fatalf("sandboxIDFromLabels(managedLabels(%q)) error = %v, want nil", id, err)
	}
	if got != id {
		t.Errorf("round trip of %q = %q, want the original id", id, got)
	}
}

func TestManagedSandboxID(t *testing.T) {
	id := runtime.NewSandboxID()

	tests := []struct {
		name   string
		labels map[string]string
		want   string
		wantOK bool
	}{
		{
			name:   "managed with a valid id",
			labels: managedLabels(id),
			want:   id,
			wantOK: true,
		},
		{
			name:   "not ours",
			labels: map[string]string{"com.example.app": "web"},
		},
		{
			// Ours, but unnameable: no caller can Inspect or Destroy it, so it
			// must not appear in a listing that implies it can be.
			name:   "managed with a malformed id",
			labels: map[string]string{labelManaged: labelManagedValue, labelSandboxID: "not-a-sandbox"},
		},
		{
			name:   "id present without the marker",
			labels: map[string]string{labelSandboxID: id},
		},
		{
			name: "no labels at all",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := managedSandboxID(tt.labels)
			if ok != tt.wantOK {
				t.Fatalf("managedSandboxID(%v) ok = %v, want %v", tt.labels, ok, tt.wantOK)
			}
			if got != tt.want {
				t.Errorf("managedSandboxID(%v) = %q, want %q", tt.labels, got, tt.want)
			}
		})
	}
}

func TestManagedMarkerLabelsMatchesManagedLabels(t *testing.T) {
	// The filter used by List must agree with what Create writes, or List sees
	// nothing it created.
	marker := managedMarkerLabels()
	written := managedLabels(runtime.NewSandboxID())

	for k, v := range marker {
		if written[k] != v {
			t.Errorf("managedMarkerLabels()[%q] = %q, but managedLabels writes %q", k, v, written[k])
		}
	}
	if !isManaged(marker) {
		t.Error("isManaged(managedMarkerLabels()) = false, want true")
	}
}
