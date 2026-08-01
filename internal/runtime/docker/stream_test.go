package docker

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/moby/moby/api/pkg/stdcopy"
)

func TestCappedWriterKeepsWhatFitsAndFlagsWhatItDropped(t *testing.T) {
	tests := []struct {
		name          string
		limit         int
		writes        []string
		want          string
		wantTruncated bool
	}{
		{
			name:   "under the limit",
			limit:  10,
			writes: []string{"abc"},
			want:   "abc",
		},
		{
			// Exactly at the limit is not truncation: nothing was dropped. Getting
			// this wrong flags every output that happens to land on the boundary.
			name:   "exactly at the limit",
			limit:  3,
			writes: []string{"abc"},
			want:   "abc",
		},
		{
			name:          "one byte over",
			limit:         3,
			writes:        []string{"abcd"},
			want:          "abc",
			wantTruncated: true,
		},
		{
			// The cap is on the total, not per call — the limit is a memory bound,
			// and a caller that writes in small pieces must not escape it.
			name:          "several writes cross the limit",
			limit:         5,
			writes:        []string{"ab", "cd", "ef"},
			want:          "abcde",
			wantTruncated: true,
		},
		{
			// Writes after the buffer is full are swallowed silently rather than
			// erroring, which is what keeps StdCopy draining the connection.
			name:          "writes after the limit are discarded",
			limit:         2,
			writes:        []string{"ab", "cdefgh", "ijk"},
			want:          "ab",
			wantTruncated: true,
		},
		{
			name:   "empty write never flags truncation",
			limit:  0,
			writes: []string{""},
			want:   "",
		},
		{
			name:          "a zero limit keeps nothing",
			limit:         0,
			writes:        []string{"a"},
			want:          "",
			wantTruncated: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newCappedWriter(tt.limit)

			for _, s := range tt.writes {
				// The claimed length must be the full input even when nothing was
				// stored. A short count is io.ErrShortWrite to StdCopy, which stops
				// the copy and strands the process on a full pipe — the stall this
				// type exists to avoid.
				n, err := w.Write([]byte(s))
				if err != nil {
					t.Fatalf("Write(%q) error = %v, want nil: the writer must never fail", s, err)
				}
				if n != len(s) {
					t.Fatalf("Write(%q) = %d, want %d: a short count stops StdCopy", s, n, len(s))
				}
			}

			if got := string(w.Bytes()); got != tt.want {
				t.Errorf("Bytes() = %q, want %q", got, tt.want)
			}
			if w.Truncated() != tt.wantTruncated {
				t.Errorf("Truncated() = %v, want %v", w.Truncated(), tt.wantTruncated)
			}
		})
	}
}

// TestCappedWriterCapsEachStreamIndependently is the reason the cap lives on the
// writers rather than on the multiplexed reader. One budget over the source is
// shared by both payloads and the 8-byte frame headers, so neither stream ends
// up bounded at its own limit and a quiet stderr makes stdout's cap larger.
func TestCappedWriterCapsEachStreamIndependently(t *testing.T) {
	const limit = 16

	var framed bytes.Buffer
	// The frames are built by hand because this package's stdcopy exports only
	// the decoder. That is no loss: writing the header out is what makes the
	// shared-budget problem concrete — those 8 bytes per frame come out of any
	// limit placed on the source, which is why the cap belongs on the writers.
	framed.Write(frame(stdcopy.Stdout, strings.Repeat("o", 100)))
	framed.Write(frame(stdcopy.Stderr, "short"))

	stdout, stderr := newCappedWriter(limit), newCappedWriter(limit)
	if _, err := stdcopy.StdCopy(stdout, stderr, &framed); err != nil {
		t.Fatalf("StdCopy error = %v, want nil: the writer must not stop the copy", err)
	}

	if got := string(stdout.Bytes()); got != strings.Repeat("o", limit) {
		t.Errorf("stdout = %q, want %d bytes of the overflowing stream", got, limit)
	}
	if !stdout.Truncated() {
		t.Error("stdout Truncated() = false, want true")
	}
	// The stream that stayed under its own limit is untouched and unflagged —
	// a caller parsing stderr must not be told it was cut because stdout was.
	if got := string(stderr.Bytes()); got != "short" {
		t.Errorf("stderr = %q, want %q", got, "short")
	}
	if stderr.Truncated() {
		t.Error("stderr Truncated() = true, want false: only stdout overflowed")
	}
}

// frame builds one Docker exec frame: an 8-byte header whose first byte names
// the stream and whose last four are a big-endian payload length, followed by
// the payload. Copying this stream raw is what embeds those header bytes in the
// output — the corruption stdcopy exists to prevent.
func frame(stream stdcopy.StdType, payload string) []byte {
	header := make([]byte, 8)
	header[0] = byte(stream)
	binary.BigEndian.PutUint32(header[4:], uint32(len(payload)))
	return append(header, payload...)
}
