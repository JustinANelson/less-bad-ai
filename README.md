# less-bad-ai

`less-bad-ai` (`lbai`) runs a coding agent inside a recoverable Git transaction. It snapshots the repository, lets a worker edit, checks the build and architectural rules, asks a reviewer to clean the result, commits verified changes, records a trace, and can restore the pre-run state.

## Build

Go 1.23 or newer is required.

```text
go build -o lbai ./cmd/lbai
```

Install or copy the same binary as `less-bad-ai` if the long alias is desired.

## Configure

Copy `.lbai/config.example.toml` to `.lbai/config.toml` and select either a command provider or an OpenAI-compatible endpoint. Commands are executed directly, without a shell.

```toml
[worker]
type = "openai"
endpoint = "http://localhost:11434/v1"
model = "qwen3-coder"

[reviewer]
type = "command"
command = ["claude", "-p", "--permission-mode", "acceptEdits"]

[build]
executable = "go"
args = ["test", "./..."]
```

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

Copy `.lbai/rules.example.toml` to `.lbai/rules.toml` to enforce project boundaries. With no rules file, linting is permissive.

## Use

```text
lbai run "add request validation to the API"
lbai status
lbai lint --fix-hint
lbai log --limit 5
lbai ui --open
lbai undo
```

`lbai run --dry-run "prompt"` plans the snapshot without changing files or refs. `lbai undo --hard` additionally removes all untracked files and should be used only when broad cleanup is intended.

On success, `lbai` creates a verified code commit followed by a memory commit. The second commit records `ARCHITECTURE.md`, `AI_CONTEXT.md`, `docs/decisions/LOG.md`, and `.lbai/traces/<timestamp>_<code-sha>.json`. This two-commit protocol avoids the impossible requirement for a commit to contain its own SHA while keeping `lbai undo` atomic from the developer's perspective.

## Safety model

- Existing dirty changes, including untracked files, are retained under a transaction-specific Git ref and restored after success or rollback.
- Automatic rollback removes only files recorded as created by the active transaction unless `--hard` is supplied.
- The dashboard listens on loopback only. Its rollback route requires POST, a same-origin request, and a per-process token.
- Provider output and errors are size-limited. API credentials are read from a named environment variable and are never persisted in trace files.

See [the agentic workflow](docs/agentic-workflow.md) for phase contracts and design decisions.
