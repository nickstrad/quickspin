package runtime

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestSandboxIDPrefixIsStable(t *testing.T) {
	// A literal on both sides: every other test uses the constant, so a typo in
	// its value would pass all of them. Changing it invalidates every issued id.
	if sandboxIDPrefix != "sbx_" {
		t.Errorf("sandboxIDPrefix = %q, want %q", sandboxIDPrefix, "sbx_")
	}
}

func TestNewSandboxIDHasPrefixAndIsUnique(t *testing.T) {
	const n = 1000

	seen := make(map[string]struct{}, n)
	for range n {
		id := newSandboxID()
		if !strings.HasPrefix(id, sandboxIDPrefix) {
			t.Fatalf("newSandboxID() = %q, want prefix %q", id, sandboxIDPrefix)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("newSandboxID() returned %q twice within %d calls", id, n)
		}
		seen[id] = struct{}{}
	}
}

func TestNewSandboxIDIsNeverEmpty(t *testing.T) {
	// A dropped suffix names every sandbox "sbx_", which makes one label filter
	// match every container this package owns.
	for range 100 {
		id := newSandboxID()
		if id == "" {
			t.Fatal("newSandboxID() = \"\", want a non-empty id")
		}
		if suffix := strings.TrimPrefix(id, sandboxIDPrefix); suffix == "" {
			t.Fatalf("newSandboxID() = %q, want a non-empty suffix after %q", id, sandboxIDPrefix)
		}
	}
}

func TestNewSandboxIDPassesItsOwnValidator(t *testing.T) {
	// A generator and validator that disagree means every Create mints an id its
	// own Inspect rejects.
	for range 100 {
		id := newSandboxID()
		if err := validateSandboxID(id); err != nil {
			t.Fatalf("validateSandboxID(%q) = %v, want nil for a freshly generated id", id, err)
		}
	}
}

func TestValidateSandboxID(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{name: "uuid suffix is valid", id: "sbx_9f8e7d6c-5b4a-4938-8271-60514f3e2d1c"},
		{name: "truncated suffix is rejected", id: "sbx_a1b2c3", wantErr: true},
		{name: "empty string is rejected", id: "", wantErr: true},
		{name: "prefix with no suffix is rejected", id: "sbx_", wantErr: true},
		{name: "missing prefix is rejected", id: "a1b2c3", wantErr: true},
		{name: "wrong separator is rejected", id: "sbx-a1b2c3", wantErr: true},
		{name: "prefix appearing later is rejected", id: "xsbx_a1b2c3", wantErr: true},
		{name: "uppercase suffix is rejected", id: "sbx_A1B2C3", wantErr: true},
		{name: "suffix with a space is rejected", id: "sbx_a1 b2c3", wantErr: true},
		{name: "leading whitespace is rejected", id: " sbx_a1b2c3", wantErr: true},
		{name: "trailing newline is rejected", id: "sbx_a1b2c3\n", wantErr: true},
		// An id is concatenated into "label=k=v", so characters with meaning to
		// the daemon's filter syntax must not survive.
		{name: "filter metacharacters are rejected", id: "sbx_a1b2c3,label=quickspin.managed=true", wantErr: true},
		{name: "equals sign is rejected", id: "sbx_a1b2=c3", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSandboxID(tt.id)
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Fatalf("validateSandboxID(%q) error = %v, want error presence %v", tt.id, err, tt.wantErr)
			}
			// Rejecting is half the contract; an error without the sentinel is
			// unusable to a caller that branches on it.
			if tt.wantErr && !errors.Is(err, ErrInvalidSandboxID) {
				t.Errorf("validateSandboxID(%q) error = %v, want errors.Is(err, ErrInvalidSandboxID)", tt.id, err)
			}
		})
	}
}

func TestValidateSandboxIDAcceptsOnlyTheCanonicalUUIDForm(t *testing.T) {
	// Every id below passes a bare uuid.Parse and none can be produced by
	// newSandboxID. The canonical round trip is what rejects them.
	tests := []struct {
		name string
		id   string
		why  string
	}{
		{name: "uppercase", id: "sbx_9F8E7D6C-5B4A-4938-8271-60514F3E2D1C", why: "hex decodes case-insensitively"},
		{name: "mixed case", id: "sbx_9f8E7d6C-5b4A-4938-8271-60514F3e2d1c", why: "hex decodes case-insensitively"},
		{name: "dashless 32 char form", id: "sbx_9f8e7d6c5b4a4938827160514f3e2d1c", why: "length 32, not 36"},
		{name: "braced 38 char form", id: "sbx_{9f8e7d6c-5b4a-4938-8271-60514f3e2d1c}", why: "length 38, not 36"},
		{name: "urn form", id: "sbx_urn:uuid:9f8e7d6c-5b4a-4938-8271-60514f3e2d1c", why: "length 45, not 36"},
		{name: "urn form with uppercase scheme", id: "sbx_URN:UUID:9f8e7d6c-5b4a-4938-8271-60514f3e2d1c", why: "the urn scheme is matched case-insensitively too"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateSandboxID(tt.id); err == nil {
				t.Errorf("validateSandboxID(%q) = nil, want an error: %s, so newSandboxID cannot produce it", tt.id, tt.why)
			}
		})
	}
}

func TestValidateSandboxIDRejectsTheNilUUID(t *testing.T) {
	// uuid.UUID's zero value, so it arrives from an unassigned field or a
	// discarded error rather than from a caller. Rejecting it turns an internal
	// bug into an immediate error instead of a 404 for a sandbox nobody named.
	var unassigned uuid.UUID
	id := sandboxIDPrefix + unassigned.String()

	if err := validateSandboxID(id); !errors.Is(err, ErrInvalidSandboxID) {
		t.Errorf("validateSandboxID(%q) error = %v, want ErrInvalidSandboxID", id, err)
	}
}

func TestValidateSandboxIDRejectsMalformedSuffixes(t *testing.T) {
	// These already fail today. Pinning them stops a later rewrite of the suffix
	// check — a hex scan, a regexp, a switch on length — from quietly relaxing a
	// guarantee that currently holds for free.
	tests := []struct {
		name string
		id   string
	}{
		{name: "underscores instead of hyphens", id: "sbx_9f8e7d6c_5b4a_4938_8271_60514f3e2d1c"},
		{name: "hyphens in the wrong positions", id: "sbx_9f8e7d6-c5b4a-4938-8271-60514f3e2d1c"},
		{name: "one character too long", id: "sbx_9f8e7d6c-5b4a-4938-8271-60514f3e2d1cc"},
		{name: "one character too short", id: "sbx_9f8e7d6c-5b4a-4938-8271-60514f3e2d1"},
		{name: "non-hex character", id: "sbx_9f8e7d6g-5b4a-4938-8271-60514f3e2d1c"},
		{name: "braced and dashless together", id: "sbx_{9f8e7d6c5b4a4938827160514f3e2d1c}"},
		{name: "leading space in the suffix", id: "sbx_ 9f8e7d6c-5b4a-4938-8271-60514f3e2d1c"},
		{name: "trailing newline", id: "sbx_9f8e7d6c-5b4a-4938-8271-60514f3e2d1c\n"},
		{name: "equals sign would alter a label filter", id: "sbx_9f8e7d6c-5b4a-4938-8271-60514f3e2d=c"},
		{name: "comma would alter a label filter", id: "sbx_9f8e7d6c-5b4a-4938-8271-60514f3e2d,c"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateSandboxID(tt.id); !errors.Is(err, ErrInvalidSandboxID) {
				t.Errorf("validateSandboxID(%q) error = %v, want ErrInvalidSandboxID", tt.id, err)
			}
		})
	}
}
