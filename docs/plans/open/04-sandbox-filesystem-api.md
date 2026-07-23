# 04 — Sandbox filesystem operations

Depends on: 03.

## Summary

Add file manipulation to the runtime: write a file into a sandbox, read one out, list a
directory, delete a path. This is the second half of the workload contract agents need
(exec + files); with it, an agent can drop source code in, run it, and read results
back without shell-quoting tricks through `Exec`.

## Industry context

E2B exposes `sandbox.files.read/write/list`; Daytona and Vercel Sandbox have
equivalents. Under the hood most container platforms use tar streams — Docker's
copy API (`CopyToContainer`/`CopyFromContainer`) speaks tar because it predates any
nicer transport, and tar preserves permissions and directory structure in one stream.
You'll wrap that oddity behind a clean interface, which is precisely what the
commercial SDKs do. In plan 07 these operations move into the guest agent (files served
from inside the sandbox), but this interface — the seam callers depend on — survives
that move unchanged. That continuity is the lesson.

## What you'll learn

- The tar format as an API: writing/reading `tar.Writer`/`tar.Reader` in Go.
- Path handling in a hostile context: what happens with `../../etc/passwd`, absolute vs
  relative paths, symlinks pointing outside the tree.
- API shape choices for binary data: `[]byte` vs `io.Reader`, and size caps.
- Interface stability across implementation swaps (the point above).

## Design and interfaces

```go
type FileInfo struct {
    Path  string
    Size  int64
    Mode  fs.FileMode
    IsDir bool
}

var (
    ErrPathNotFound = errors.New("path not found in sandbox")
    ErrFileTooLarge = errors.New("file exceeds size cap")
)

type Runtime interface {
    // ...plans 02–03...
    WriteFile(ctx context.Context, id, path string, content []byte, mode fs.FileMode) error
    ReadFile(ctx context.Context, id, path string) ([]byte, error)
    ListDir(ctx context.Context, id, path string) ([]FileInfo, error)
    RemovePath(ctx context.Context, id, path string) error
}
```

Committed decisions:

- Paths are absolute inside the sandbox; relative paths are rejected, and any path is
  cleaned and validated before use.
- `ReadFile` enforces a size cap (e.g. 10 MiB) with `ErrFileTooLarge`; bulk transfer is
  out of scope until someone needs it.
- `WriteFile` creates parent directories (agents expect this; E2B does it).

## Tasks

1. Add the methods to the interface and implement via Docker's copy API.
2. Path validation helpers with table-driven unit tests (no Docker needed).
3. Wire `quickspin sandbox cp`/`ls`/`rm` CLI verbs.
4. Integration tests below.

## Definition of done

Pure unit tests (in `make test`):

- `TestPathValidationRejectsTraversal` — table-driven: `../x`, `a/../../x`, empty,
  relative paths all rejected.

Integration tests (`make test-docker`):

- `TestWriteThenReadRoundTrip` — content and mode survive, including a nested path
  whose parents did not exist.
- `TestWriteThenExecReadsFile` — write a script, `Exec` runs it: proves the two halves
  of the contract compose.
- `TestReadMissingPath` — `ErrPathNotFound`.
- `TestReadFileTooLarge` — a file over the cap yields `ErrFileTooLarge`, not an OOM.
- `TestListDir` — names, sizes, dir flags correct.
- `TestBinaryContentSurvives` — random bytes round-trip unchanged (catches any
  text-mode or encoding sloppiness early, before SDKs exist).

Deliberately untested: symlink edge cases inside the container, ownership/uid mapping,
concurrent writes to one path, large-file streaming.

## Solo-developer tradeoffs

Commercial platforms support streaming uploads, globbing, file watching, and
presigned-URL bulk transfer. Byte-slice APIs with caps cover the agent-development use
case (source files, logs, small artifacts) with a fraction of the surface. The
size-cap-with-typed-error pattern means the limitation is explicit in the API rather
than a silent failure — the habit that matters more than the feature.
