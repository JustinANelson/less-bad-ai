Implement the dual-agent execution loop in `less-bad-ai` (`lbai`).

Objective: When `lbai run "<prompt>"` is executed, coordinate a Worker Agent to write code, automatically verify builds and AST rules, and run a background Tech Lead Agent to clean and standardize changes before finalizing.

Requirements:
1. Agent Subsystem (`pkg/runner`):
   - Abstract LLM communication behind a clean interface:
     - Support local LLMs via Ollama / OpenAI-compatible local APIs (`http://localhost:11434/v1` or custom).
     - Support CLI tools (e.g., invoking `claude`, `aider`, or custom CLI runners) configured in `.lbai/config.toml`.
   - Flags for `lbai run`:
     - `--model`, `-m`: Override active LLM model.
     - `--max-retries`: Maximum self-correction attempts (default: 3).
     - `--skip-review`: Skip the Tech Lead cleanup step.

2. The Autonomous Verification & Correction Pipeline:
   - Step 1 [Worker]: Dispatch prompt and file context to Worker Agent.
   - Step 2 [Build & AST Validation]:
     - Run project build/test command (configured in `.lbai/config.toml`, e.g., `go test ./...`, `./gradlew build -x test`).
     - Run `pkg/linter` AST invariant check.
   - Step 3 [Correction Loop]:
     - If compilation or AST lint fails, formulate a diagnostic prompt containing the exact compiler errors and linter hints.
     - Send back to Worker Agent to self-correct (up to `--max-retries`).
     - If retries are exhausted, invoke automatic rollback (`lbai undo`) and log failure reason.
   - Step 4 [Tech Lead Reviewer]:
     - If Step 2 passes, feed the Git diff to the Tech Lead Agent.
     - System prompt: "You are the Automated Tech Lead. Inspect this diff. Enforce existing code idioms, eliminate temporary logs/debug statements, extract inline logic into shared utilities if duplicate patterns exist, and preserve all architectural boundaries."
     - Apply Tech Lead refactor patch and run one final build verification.

3. Interactive Terminal UX:
   - Output clean spinner / step progression:
     `[lbai] Snapshot created (refs/lbai/snapshots/1741567200)`
     `[lbai] [1/4] Worker generating code... OK`
     `[lbai] [2/4] Verifying build & AST boundaries... OK`
     `[lbai] [3/4] Running Tech Lead cleanup... OK`
     `[lbai] [4/4] Finalizing transaction... DONE`