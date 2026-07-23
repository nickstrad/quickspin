# quickspin

Quickspin is a **learning project**. The user is building it to learn, not to ship
production software as fast as possible.

## Working guidelines

- Do not overwrite or rewrite code the user authored unless explicitly asked. Prefer
  identifying the issue, explaining why it matters, and presenting possible changes.
- Default to analysis first. When you find a bug, incomplete stub, questionable design,
  or missing feature, explain it and wait for direction before editing unless the user
  directly asked you to implement or fix it.
- Incomplete functions, TODOs, and rough experiments may be intentional learning steps.
  Do not finish, refactor, or polish them on your own initiative.
- When proposing a design, distinguish current behavior from possible future
  architecture. Do not describe a proposal as though it already exists.
- Explain unfamiliar Go or TypeScript conventions when they materially affect a change.
  Optimize for the user's understanding, not merely for completing the task.
- State assumptions and tradeoffs. When several reasonable approaches exist, present
  them with the question each approach would help answer.

## Reporting changes

When the user asks you to change code or fix a bug, report each material change as a
numbered finding with a short title, stacked before/after excerpts, and a concise
explanation:

````markdown
## 1. Empty input now uses the documented default

```go
// before
return fmt.Sprintf("Hello, %s!", name)

// after
if name == "" {
    name = "World"
}
return fmt.Sprintf("Hello, %s!", name)
```

An empty input previously produced `Hello, !`, contrary to the documented fallback.
The new branch makes the empty-input behavior explicit.
````

- Stack **before over after**, never side by side.
- Use small excerpts with only enough context to understand the change, not whole files
  or functions.
- Order numbered findings from most to least consequential. Group mechanical cleanup
  such as typos and renames into a final unnumbered note.
- Explain the prior behavior or failure mode and why the new behavior resolves it. Do
  not merely narrate the diff.
- Call out behavior changes and design decisions separately, including what the user
  should challenge if the tradeoff is not what they want.
- For a new file, say that it did not exist before and show only its important new
  contract. For deleted code, show the removed excerpt and explain why it was safe to
  remove.
- Report validation performed and anything that remains untested.

## Validation

Use the repository's existing commands where applicable:

- `make test` runs all Go tests.
- `make fmt` formats Go code.
- `make vet` reports suspicious Go constructs.
- `make build` builds the current CLI into `bin/`.

Add TypeScript commands here when a TypeScript workspace is introduced. Do not assume a
package manager until the repository chooses one.

## Documentation

All project documentation lives in [`docs/`](docs/). Start at
[`docs/index.mdx`](docs/index.mdx), which describes the documentation layout and links to
the current reference material.

- `docs/plans/open/` contains MDX plans that are proposed or still in progress. Do not
  implement a plan merely because it exists; wait for the user to ask.
- `docs/plans/closed/` contains completed MDX plans retained as historical context. They may
  explain why something changed, but the current code remains authoritative.
- `docs/reference/` contains forward-looking learning and architecture material. It is
  not a specification for current behavior and should not be implemented unless asked.

Keep `docs/index.mdx` current when adding, moving, or removing documentation.

The local reader lives in `docs/`. Use `make docs` to start it and `make docs-build` to
type-check and produce a static build. Documentation content uses `.mdx`; keep
`docs/plans/AGENTS.md` as Markdown because its exact filename activates scoped agent
instructions.

## Git

- Never commit, merge, rebase, push, or create a pull request unless the user explicitly
  asks for that specific action.
- Do not treat permission for one commit or other Git action as standing permission for
  later actions.
- Do not add AI attribution or `Co-Authored-By:` trailers to commit messages.
- Preserve unrelated working-tree changes and call them out rather than modifying them.

`CLAUDE.md` is a symlink to this file so both agent entry points share the same
instructions. Edit `AGENTS.md`, not the symlink.
