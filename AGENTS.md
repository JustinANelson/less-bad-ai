# less-bad-ai Agent Instructions

## Mission

Build `less-bad-ai`, a Go CLI that wraps coding-agent work in a recoverable Git transaction, verifies architectural boundaries, reviews generated changes, records durable project memory, and exposes a local topology view.

The shipped binary is `lbai`; `less-bad-ai` is an alias or installation-time symlink.

## Sources of Truth

Read these sources in order before changing behavior:

1. The user's current request.
2. The phase brief for the phase being implemented: `phase1.md` through `phase5.md`.
3. `docs/agentic-workflow.md`, which resolves conflicts and fills gaps between phase briefs.
4. Existing code, tests, and public CLI behavior.

Do not silently reinterpret a phase requirement. If a requirement conflicts with an established public interface, preserve compatibility when practical and record the decision in `docs/decisions/LOG.md` once that file exists.

## Working Method

1. Inspect the repository and Git status before editing. Preserve unrelated user changes.
2. Identify the earliest incomplete phase. Work on one phase at a time unless the user explicitly requests a different scope.
3. Write a short implementation plan for work that crosses packages or changes transaction semantics.
4. Implement the smallest complete vertical slice that satisfies the active phase.
5. Add or update tests with the implementation. Prefer observable behavior over private implementation details.
6. Run formatting, focused tests, then the full available suite.
7. Review the final diff for accidental API changes, unsafe Git operations, secrets, generated files, and stale documentation.
8. Report what changed, what was verified, and any remaining limitation. Do not claim a check passed unless it ran.

Ask for clarification only when a missing decision would materially change public behavior, destroy data, require credentials, or expand scope. Otherwise, state the assumption and proceed.

## Phase Gates

### Phase 1: Transaction Core

Implement the Cobra command tree and `pkg/gitengine` before integrating a real agent. Cover clean and dirty repositories, snapshot refs, state persistence, dry runs, rollback, and untracked-file cleanup with temporary Git repositories.

Gate: snapshot and rollback tests pass on a disposable repository, and destructive Git commands cannot target a repository other than the resolved transaction root.

### Phase 2: Architectural Rules

Implement configuration parsing and deterministic scanners separately from CLI rendering. Diagnostics must carry rule, path, location when known, offending import/dependency, severity, and optional fix hint.

Gate: Go, Java/Kotlin, JavaScript/TypeScript, boundary matching, dependency manifests, and strict exit behavior are tested.

### Phase 3: Agent Pipeline

Keep providers behind interfaces. Treat command runners, HTTP clients, builds, linters, retry policy, and review as injected dependencies so orchestration tests do not require a live model or network.

Gate: tests cover success, correction, retry exhaustion with rollback, skipped review, review failure, cancellation, and final verification.

### Phase 4: Memory and Commits

Generate architecture updates and traces from structured transaction results. Keep machine-owned runtime state separate from versioned project memory. Validate generated commit subjects and trace JSON before committing.

Gate: tests cover trace round trips, deterministic log ordering, architecture section updates, commit synthesis, and failure recovery between commits.

### Phase 5: Summary and UI

Build topology data independently of HTTP handlers and rendering. Bind the UI to loopback by default. The revert endpoint is mutating and must be POST-only, reject cross-origin requests, and require a per-process token or equivalent confirmation mechanism.

Gate: graph extraction, latest-trace highlighting, handlers, SSE disconnects, port conflicts, and authorized rollback are tested.

## Project Conventions

- Target the Go version declared in `go.mod` once it exists. Use the standard library unless a phase names a dependency or a dependency materially reduces risk.
- Keep Cobra wiring in `cmd/`; put reusable behavior in `pkg/` packages named by the phase briefs.
- Pass `context.Context` through agent, process, build, and server boundaries.
- Wrap errors with operation and relevant path/ref. Never include tokens, authorization headers, or full sensitive prompts in errors.
- Use injectable clocks, ID generators, command executors, and HTTP clients where determinism matters.
- Write state files atomically with restrictive permissions where supported.
- Treat repository paths as untrusted input: normalize them, reject escapes from the repository root, and do not follow unsafe symlinks during cleanup.
- Never invoke a shell for configured build commands by default. Parse an executable plus argument list, or require an explicit shell opt-in.
- Never weaken, delete, or skip a test merely to make a run pass.

## Verification Commands

The project is documentation-only today. Once the Go module exists, the baseline checks are:

```text
gofmt -w <changed-go-files>
go vet ./...
go test ./...
go build ./cmd/lbai
```

Use focused package tests while iterating. Run the full baseline before handing off a completed phase. Add `go test -race ./...` when changing concurrent runner, SSE, or state-management code.

## Git and Transaction Safety

- Do not run destructive Git operations until the repository root and expected pre-run HEAD have been verified.
- Do not overwrite `refs/lbai/*` from another active transaction.
- Track files created by the active run so rollback removes only transaction-owned untracked files.
- Preserve a user's pre-existing dirty worktree through both success and rollback paths. If restoration conflicts, stop with recovery instructions and retain the stash ref.
- A dry run must not write files, refs, indexes, commits, stashes, or state.
- Do not push, force-push, amend user commits, or change remotes unless the user explicitly asks.

## Documentation Discipline

When public behavior changes, update the relevant phase-derived documentation in the same change. Keep `ARCHITECTURE.md` focused on current topology and recent decisions; keep detailed historical records in traces and `docs/decisions/LOG.md`.

Do not copy these instructions into tool-specific files. `CLAUDE.md` and `GEMINI.md` import this file so the shared workflow stays consistent.
