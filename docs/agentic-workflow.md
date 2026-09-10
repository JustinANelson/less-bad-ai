# Agentic Development Workflow

This document turns the five phase briefs into an implementation workflow that can be followed consistently by Claude Code, Codex, Gemini CLI, or a human contributor. The phase briefs define product scope. This document defines sequencing, cross-phase contracts, and resolutions for requirements that cannot coexist literally.

## Delivery Strategy

Implement phases in order. Each phase should leave the repository buildable and tested; later phases extend stable interfaces rather than replacing them.

| Phase | Product increment | Primary packages | Exit condition |
| --- | --- | --- | --- |
| 1 | Recoverable Git transaction and CLI shell | `cmd`, `pkg/gitengine` | A simulated run can snapshot and roll back safely |
| 2 | Architecture policy enforcement | `pkg/linter` | Changed files and manifests produce deterministic diagnostics |
| 3 | Worker, correction, and tech-lead loop | `pkg/runner` | Orchestration succeeds or restores the exact pre-run state |
| 4 | Durable memory, trace log, and commits | `pkg/memory` | Verified runs produce queryable decisions and commits |
| 5 | Terminal summary and local topology UI | topology/UI package | Latest-run impact is visible and rollback is safely exposed |

Do not build phase 5 handlers around placeholder phase 4 trace shapes, or phase 3 orchestration around concrete provider commands. Establish the lower-phase contract first.

## Cross-Phase Decisions

### Zero-configuration discovery

When `.lbai/config.toml` is absent, `lbai run` detects a supported local coding
agent and a conventional project verification command without writing to the
repository. Explicit configuration always wins. `lbai init` persists the same
detected values for inspection and customization, and refuses to overwrite an
existing configuration.

When `.lbai/rules.toml` is absent, select only high-confidence rules inferred
from project metadata. Compare changed-file diagnostics with the pre-run Git
revision and fail only regressions, while making the count of unchanged legacy
violations visible. An explicit path audit continues to report every violation.

### Verification graph

Represent project verification as named checks with explicit dependencies.
Execute independent checks concurrently with bounded parallelism, but collect
output and errors in stable name order. A failed check prevents its dependents
from running. The legacy `[build]` configuration maps to one check; `[[checks]]`
is the graph form, and the two forms cannot be combined. Architectural scanning
is always an LBAI-owned graph node.

### CLI flag collision

Both phase 1 and phase 3 assign `-m`: phase 1 to `--message`, phase 3 to `--model`. Cobra cannot assign the same shorthand twice on one command. Preserve the conventional and earlier public shorthand `-m` for `--message`; expose model selection as `--model` without a shorthand. If compatibility with a released implementation dictates otherwise, retain that behavior and document the deviation.

### Dirty worktrees

The phrase “verify a clean Git repo” and the instruction to stash dirty changes describe two paths:

1. Resolve and validate the Git repository root.
2. If clean, start the transaction directly.
3. If dirty, capture tracked and untracked user changes under a unique `refs/lbai/stash/<transaction-id>` ref, then make the worktree clean.
4. Run the transaction against the recorded clean HEAD.
5. On success or rollback, reapply the user's saved changes. Delete the stash ref only after successful restoration.

Never use one global `refs/lbai/stash` value for overlapping transactions. State may expose the most recent ref for compatibility, but refs must be transaction-specific internally. Refuse to start a second transaction in the same worktree while one is `in_progress`.

### Runtime state versus project memory

Runtime recovery data is local and must not be committed:

```text
.lbai/state.json
.lbai/snapshots/
```

Durable trace artifacts are project history and should be committed:

```text
.lbai/traces/
ARCHITECTURE.md
AI_CONTEXT.md
docs/decisions/LOG.md
```

Add precise ignore rules rather than ignoring all of `.lbai/`. Git refs remain in Git's ref database; `.lbai/snapshots/` is reserved for supplementary rollback metadata.

### Trace commit SHA cycle

A commit cannot contain a file whose name and content include that same commit's final SHA: changing the file changes the commit SHA. Use a two-commit finalization protocol:

1. Create the verified code commit and capture its SHA.
2. Generate `.lbai/traces/<timestamp>_<code-sha>.json`, update architecture/context/decision documents, and create a metadata commit.

The trace's `commit_sha` is the code commit SHA. The code commit footer should carry a stable `LBAI-Trace-ID` generated before commit, not a not-yet-known trace path. The metadata commit can reference both the trace path and code SHA. `lbai undo` resets to `last_clean_head`, reverting both commits atomically from the user's perspective.

If the product later requires a single commit, change the trace filename contract to use a precomputed transaction ID and omit the self-referential commit SHA from committed content.

### Rollback ownership

Record the pre-run HEAD, repository root, transaction ID, snapshot ref, optional stash ref, and the exact paths that were untracked before and created during the run. Rollback may reset commits and remove transaction-created files; it must not broadly clean files it cannot prove belong to the transaction. `--hard` allows broader cleanup only after showing or otherwise making the scope explicit.

### Provider output

Do not depend on free-form prose to apply changes. A CLI provider may edit the worktree directly; an HTTP provider should return a defined patch or file-operation result. Record provider stdout/stderr with size limits and redaction, and surface structured errors to the correction loop.

## End-to-End Run State Machine

Persist state after each transition so interruption recovery is explicit:

```text
idle
  -> snapshotting
  -> worker_running
  -> verifying
  -> correcting -> verifying       (bounded by max retries)
  -> reviewing  -> final_verifying
  -> committing_code
  -> writing_memory
  -> committing_memory
  -> restoring_user_changes
  -> complete
```

Any failure before a code commit transitions through `rolling_back`. Failure after the code commit but before the metadata commit must also reset to `last_clean_head`; do not leave a partially finalized transaction unless rollback itself fails. When rollback fails, retain refs and state, mark the transaction `recovery_required`, and print exact non-destructive recovery guidance.

## Phase 1 Checklist

- Bootstrap `go.mod`, `cmd/lbai`, and Cobra commands and aliases.
- Separate Git command execution from state serialization.
- Resolve the repository root once and use it for every operation.
- Write `state.json` through a temporary file plus atomic rename.
- Make snapshots and stash refs collision-resistant with UTC timestamp plus transaction ID.
- Make `--dry-run` render the intended operations from a read-only plan.
- Test nested working directories, detached HEAD, initial repositories without commits, dirty tracked files, pre-existing untracked files, paths with spaces, corrupt state, stale refs, and idempotent undo.

## Phase 2 Checklist

- Define typed TOML configuration and validate regex/glob patterns at load time.
- Model forbidden dependencies as array-of-table entries with explicit fields such as `name`, `pattern`, and optional `hint`; the prose example's “array” wording is not a complete TOML schema.
- Normalize scanned paths to repository-relative slash-separated paths before glob matching.
- Parse Go imports with `go/parser`; use small language-aware scanners for Java/Kotlin and JavaScript/TypeScript that ignore comments and ordinary strings.
- Scan dependency manifests with format parsers, not substring matching.
- Define allowed-list precedence explicitly: an import must pass the allowlist when present and must never match a forbidden pattern.
- Sort diagnostics by path, location, and rule so text and JSON output are reproducible.

## Phase 3 Checklist

- Define interfaces for worker/reviewer agents, verifier, linter, patch applier, process runner, and rollback.
- Load `.lbai/config.toml` once, validate it, and pass a typed config into orchestration.
- Represent build commands as executable plus arguments. Support shell syntax only through an explicit opt-in.
- Send correction prompts the original objective, current diff summary, exact diagnostics, retry count, and instruction to modify only the active repository.
- Count retries consistently: `--max-retries=3` means one initial attempt plus at most three correction attempts.
- Run build and lint after worker changes, after every correction, and after review changes.
- Treat cancellation, timeout, malformed provider output, patch failure, and review-induced regression as transaction failures.
- Redact credentials and cap prompt, diff, stdout, and stderr sizes before persistence or model submission.

## Phase 4 Checklist

- Use a versioned trace schema so future readers can migrate old records.
- Derive touched files from Git, not model claims.
- Mark summaries and ADR text as model-generated; validate required sections before writing.
- Update named Markdown sections with a parser or stable markers instead of rewriting unrelated prose.
- Use UTC, RFC 3339 timestamps in JSON and filesystem-safe UTC timestamps in names.
- Synthesize a conventional commit subject with a bounded length, a `Why:` paragraph, and a stable trace ID footer.
- Implement `lbai log` from trace files, sort by parsed timestamps, and degrade gracefully when referenced commits have been pruned.
- Test interruption between the code and metadata commits and verify full rollback.

## Phase 5 Checklist

- Define a serializable graph model before choosing frontend rendering details.
- Reuse phase 2 import extraction where possible so lint and topology agree.
- Give nodes stable IDs based on normalized module/package paths; deduplicate and sort nodes and edges.
- Embed all browser assets. Do not depend on a CDN in the local dashboard.
- Bind to `127.0.0.1` and `::1` semantics by default; require an explicit flag for non-loopback exposure.
- Protect the rollback API with POST, same-origin validation, and an unguessable per-process token. Serialize it with other transactions.
- Do not expose full prompts or reasoning by default; show a redacted user prompt, worker summary, reviewer modifications, touched modules, and status.
- Handle SSE clients with request cancellation and bounded fan-out; never let a slow browser block a run.
- Render the terminal summary with a plain ASCII fallback when Unicode or terminal width is unsuitable.

## Testing Layers

Use three complementary layers:

1. Pure unit tests for config validation, scanners, state transitions, graph building, summaries, and commit synthesis.
2. Git fixture tests using temporary repositories and a real `git` executable for refs, stashes, commits, dirty states, and rollback.
3. Orchestration tests using scripted fake agents and process runners for correction, review, timeout, cancellation, and recovery paths.

Network-backed model calls are opt-in integration tests and must not be necessary for `go test ./...`. Tests must not depend on global Git identity; set repository-local identity in each fixture.

## Definition of Done

A phase is complete when its public behavior matches the phase brief plus the decisions above, focused and full tests pass, user-owned work survives both success and failure, documentation describes new flags/configuration/state, and the final diff contains no unrelated changes or secrets.

For any intentionally deferred requirement, leave a named issue or clearly scoped TODO with the reason and dependency. Do not describe a partial phase as complete.
