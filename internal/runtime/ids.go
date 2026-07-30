package runtime

import (
	"strings"

	"github.com/google/uuid"
)

const sandboxIDPrefix = "sbx_"

func NewSandboxID() string { return sandboxIDPrefix + uuid.NewString() }

// validateSandboxID rejects anything NewSandboxID could not have produced, so a
// malformed id can answer 400 where a missing one answers 404.
func validateSandboxID(id string) error {
	uid, ok := strings.CutPrefix(id, sandboxIDPrefix)
	if !ok {
		return ErrInvalidSandboxID
	}

	// uuid.Parse also accepts braced, urn:uuid: and dashless encodings and
	// decodes hex case-insensitively, so the round trip through String is what
	// narrows it to the single canonical form. uuid.Nil is uuid.UUID's zero
	// value: it means an unset field, never caller input.
	v, err := uuid.Parse(uid)
	if err != nil || v == uuid.Nil || v.String() != uid {
		return ErrInvalidSandboxID
	}

	return nil
}
