# less-bad-ai

`less-bad-ai` (`lbai`) is a guardrail for solo and indie developers who want the
convenience of autonomous coding agents without making every generated change a
leap of faith. It runs an agent inside a recoverable Git transaction, checks the
build and architectural rules, asks a reviewer to clean the result, commits
verified changes, records a trace, and can restore the pre-run state.

## Install

After the first tagged GitHub release is published, prebuilt binaries will not
require Go. On Windows PowerShell:

```powershell
irm https://raw.githubusercontent.com/JustinANelson/less-bad-ai/main/install.ps1 | iex
```

On macOS or Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/JustinANelson/less-bad-ai/main/install.sh | sh
```

Both installers verify the release archive checksum and install the `lbai` and
`less-bad-ai` command names. Releases are produced for Intel and ARM systems.

Until then, install the current working copy from source with Go 1.26 or newer:

```powershell
go install ./cmd/lbai
```

Or build without installing:

```sh
go build -o lbai ./cmd/lbai
```

## Quick start

Open the project you want LBAI to manage and run:

```sh
cd path/to/project
lbai setup
lbai run "add request validation"
```

`lbai setup` detects the installed coding agent and project checks, creates
`.lbai/config.toml`, and makes a baseline commit when the project is not already
committed to Git. It refuses to baseline common secret files such as `.env`,
private keys, and credential files. Add those files to `.gitignore`; use
`--allow-sensitive` only when committing them is intentional.

Check readiness at any time:

```sh
lbai doctor
```

Projects that already have an initial Git commit and a supported agent can skip
setup entirely: `lbai run "describe the change"` uses read-only automatic
discovery. Run `lbai version` to inspect an installed build.

## Configure

Configuration is optional when a supported coding-agent CLI is installed. `lbai`
auto-detects `codex`, `claude`, or `aider` (in that order), along with common
Go, Rust, Maven, Gradle, Node, and Python test commands. Detection is read-only.
`lbai setup` is the recommended first-run experience; use `lbai init` only to
persist configuration inside an existing Git repository without bootstrapping it.

An explicit `.lbai/config.toml` always takes precedence. Copy
`.lbai/config.example.toml` or edit the file produced by `lbai init` to select a
different command provider or an OpenAI-compatible endpoint. Commands are
executed directly, without a shell.

```toml
[worker]
type = "openai"
endpoint = "http://localhost:11434/v1"
model = "qwen3-coder"

[reviewer]
type = "command"
command = ["claude", "-p", "--permission-mode", "acceptEdits"]

[[checks]]
name = "test"
executable = "go"
args = ["test", "./..."]

[[checks]]
name = "vet"
executable = "go"
args = ["vet", "./..."]
```

Checks form a dependency graph. Independent checks run concurrently with
deterministic output; `depends_on` delays a check until its prerequisites pass.

```toml
[[checks]]
name = "integration"
executable = "go"
args = ["test", "-tags=integration", "./..."]
depends_on = ["test"]
```

The `architecture` check name is reserved for LBAI's automatic boundary scan.
Configuration is strict: obsolete or misspelled fields fail before a transaction
starts instead of silently disabling verification.

Command providers edit the worktree directly. OpenAI-compatible HTTP providers must return one JSON object; free-form or Markdown-wrapped responses are rejected. Writes and deletes are confined to the repository, and `.git` plus LBAI runtime recovery state are protected.

```json
{
  "summary": "Implemented request validation.",
  "operations": [
    {
      "operation": "write",
      "path": "pkg/api/validation.go",
      "content": "package api\n"
    },
    {
      "operation": "delete",
      "path": "pkg/api/obsolete.go"
    }
  ]
}
```

Each `write` operation supplies the complete new file content. Return an empty `operations` array when no edits are needed.

Copy `.lbai/rules.example.toml` to `.lbai/rules.toml` to customize project
boundaries. Without a rules file, `lbai` selects a conservative project profile.
For Go modules, reusable `pkg/` and `internal/` packages may not import the
module's executable `cmd/` packages.

During `lbai run` and a default `lbai lint`, diagnostics are compared with the
pre-change Git revision. Existing violations are reported as an unchanged
baseline and do not block work; newly introduced violations fail verification.
Use `lbai lint --path <path>` when intentionally auditing all violations in a
file or directory.

## Use

```text
lbai setup
lbai doctor
lbai run "add request validation to the API"
lbai status
lbai lint --fix-hint
lbai log --limit 5
lbai ui --open
lbai undo
```

The prompt may be quoted as one argument or entered as trailing words (for example,
`lbai run add request validation to the API`). `lbai` joins all trailing prompt
arguments with spaces, which avoids shell-specific quoting surprises.

`lbai run --dry-run "prompt"` plans the snapshot without changing files or refs. `lbai undo --hard` additionally removes all untracked files and should be used only when broad cleanup is intended.

On success, `lbai` creates a verified code commit followed by a memory commit. The second commit records `ARCHITECTURE.md`, `AI_CONTEXT.md`, `docs/decisions/LOG.md`, and `.lbai/traces/<timestamp>_<code-sha>.json`. This two-commit protocol avoids the impossible requirement for a commit to contain its own SHA while keeping `lbai undo` atomic from the developer's perspective.

The final fixed-width ASCII summary reports touched module directories, checked invariants, review outcome, commit, and undo command. `lbai ui` renders the same run as an embedded SVG dependency graph without external browser assets.

## Manual A/B verification

Run a deterministic one-shot comparison of the same project and prompt with and
without LBAI:

```powershell
.\scripts\manual-compare.ps1
```

To use an installed coding agent instead of the deterministic stand-in:

```powershell
.\scripts\manual-compare.ps1 -Mode live -Agent codex
```

Both projects are cloned from one seed commit. Results, transcripts, and a
comparison report are written beneath the ignored `.manual-eval/` directory.
See [Manual A/B Verification](docs/manual-comparison.md) for expected results,
supported agents, interpretation, and cleanup.

## Safety model

- Existing dirty changes, including untracked files, are retained under a transaction-specific Git ref and restored after success or rollback.
- Snapshot and stash startup phases are journaled in `.lbai/state.json`; interrupted startup is recovered before any hard reset. Runtime paths are added to Git's local exclude file without modifying the project's tracked ignore rules.
- Automatic rollback removes only files recorded as created by the active transaction unless `--hard` is supplied.
- The dashboard listens on loopback only. Its rollback route requires POST, a same-origin request, and a per-process token.
- Dashboard trace context is redacted and length-limited before it crosses the HTTP API boundary; full prompts remain in the versioned trace store.
- Provider output and errors are size-limited, and authorization headers are not persisted. Prompts and summaries are committed to the trace store, so they should not contain secrets.

See [the agentic workflow](docs/agentic-workflow.md) for phase contracts and design decisions.
