package docker

import "bytes"

// cappedWriter buffers up to limit bytes and reports whether anything was
// dropped. It is the destination side of the buffered-output tradeoff: capping
// the *reader* instead would stop draining the connection, leaving the process
// blocked on a full pipe and the exec never finishing.
//
// Write therefore always claims the full length and never errors, even after the
// cap. A short return makes stdcopy.StdCopy stop with io.ErrShortWrite, which is
// the same stall by a different route — the writer must keep swallowing output
// so the command can run to completion and its exit code stays retrievable.
type cappedWriter struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func newCappedWriter(limit int) *cappedWriter {
	return &cappedWriter{limit: limit}
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	room := w.limit - w.buf.Len()
	if len(p) > room {
		w.truncated = true
	}
	if room > 0 {
		w.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

func (w *cappedWriter) Bytes() []byte { return w.buf.Bytes() }

func (w *cappedWriter) Truncated() bool { return w.truncated }
