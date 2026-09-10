# Manual A/B Verification

The comparison harness runs the same prompt and agent command against two Git
repositories cloned from one seed commit:

- `without-lbai`: the agent edits the repository directly.
- `with-lbai`: `lbai run` snapshots the repository, invokes the agent, checks the
  build and architectural rules, requests corrections, reviews the result, and
  records the verified commits and trace.

Generated runs live under `.manual-eval/` and are ignored by Git. The harness
does not modify, commit, reset, or run an agent against the less-bad-ai working
tree itself.

## Deterministic smoke comparison

From the less-bad-ai repository root, run:

```powershell
.\scripts\manual-compare.ps1
```

This mode builds a deterministic coding-agent stand-in. On its initial pass, the
stand-in creates a working, tested implementation that imports `net/http`
directly from `internal/app`, violating the fixture's application boundary.
When LBAI returns the exact lint diagnostics, the stand-in corrects the design by
introducing an application interface and moving HTTP access into an adapter.
This deliberately tests the core `GOAL.md` claim: LBAI should act like a compiler
for an AI change by rejecting code that appears functional but is not acceptable
for the project's declared architecture.

A successful comparison demonstrates all of the following in one run:

| Check | Without LBAI | With LBAI |
| --- | --- | --- |
| Agent makes a change | Yes | Yes |
| Go tests pass | Yes | Yes |
| Architectural policy passes | No | Yes, after correction |
| Tech-lead review runs | No | Yes |
| Verified code and memory commits exist | No | Yes |
| Durable trace exists | No | Yes |

The command prints the generated locations and writes `REPORT.md` plus separate
agent, test, lint, and LBAI transcripts. Open both project directories in a diff
tool to inspect the resulting code directly.

## Live-agent comparison

Live mode uses a locally installed and already authenticated coding-agent CLI:

```powershell
.\scripts\manual-compare.ps1 -Mode live -Agent codex
```

Supported values are `codex`, `claude`, and `aider`. Use `-Agent auto` to choose
the first available agent in that order. A custom prompt can be supplied while
keeping both sides identical:

```powershell
.\scripts\manual-compare.ps1 -Mode live -Agent auto `
  -Prompt "Add a FetchTitle use case that retrieves text from an HTTP endpoint. Keep it simple and add tests."
```

The live comparison passes only when the LBAI run completes and its resulting
project passes both tests and an explicit full architectural audit. The control
result is recorded rather than required to fail: a capable agent may choose the
correct architecture without LBAI, and model output naturally varies between
runs.

For a more meaningful evaluation, repeat the live run several times and compare:

- completion rate;
- test pass rate;
- architectural violations;
- correction attempts and review modifications;
- committed trace quality; and
- manual diff-review findings.

Keep the agent, prompt, starting commit, credentials, and model configuration
the same for each pair. The harness guarantees the same seed and command within
one pair, but it is a manual product check—not a statistically controlled model
benchmark.

## Cleanup

Each execution creates a new timestamped directory and never deletes an earlier
run. Remove a specific completed run when it is no longer needed, for example:

```powershell
Remove-Item -LiteralPath .manual-eval\20260910T203525Z -Recurse
```

Verify the exact timestamped path before running the cleanup command.
