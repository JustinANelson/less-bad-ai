Build a Go CLI tool named `less-bad-ai` (binary name: `lbai`, with `less-bad-ai` as a symlink/alias).

Objective: Provide an isolated Git transaction wrapper around coding agent operations so any prompt can be cleanly inspected, committed, or atomically rolled back without manual Git intervention.

Requirements:
1. CLI Setup (using github.com/spf13/cobra):
   - Root command: `lbai` (alias: `less-bad-ai`)
   - Commands & Flags:
     - `lbai run "<prompt>"` (aliases: `lbai r`, `lbai exec`)
       Flags:
         - `--dry-run`: Simulate snapshot and execution steps without modifying files.
         - `--message`, `-m`: Optional manual commit message override.
     - `lbai undo` (alias: `lbai revert`)
       Flags:
         - `--hard`: Force reset discarding any untracked or dirty files since the last run.
     - `lbai status`
       Flags:
         - `--json`: Output current transaction and snapshot status in JSON format.

2. Git Transaction & State Engine (`pkg/gitengine`):
   - Store all internal metadata inside `.lbai/`:
     - `.lbai/state.json`: tracks current transaction state.
     - `.lbai/snapshots/`: stores temporary rollback metadata.
   - On `lbai run`:
     - Verify current working directory is a clean Git repo. If uncommitted changes exist, stash them under ref `refs/lbai/stash`.
     - Record the pre-run HEAD SHA and snapshot the working tree into `refs/lbai/snapshots/<timestamp>`.
     - Update `.lbai/state.json` with:
       `{ "last_clean_head": "<sha>", "snapshot_ref": "<ref>", "timestamp": "<iso>", "status": "in_progress", "prompt": "<prompt>" }`
   - On `lbai undo`:
     - Read `.lbai/state.json`.
     - Execute a hard reset back to `last_clean_head`.
     - Clean any newly generated untracked files added during that run.
     - Update `.lbai/state.json` status to `"rolled_back"`.
     - Output: `[lbai] Reverted workspace cleanly to pre-prompt state (<sha>).`

3. Execution Stub:
   - Provide a simulated worker execution step in `lbai run` that prints staged progress output:
     `[lbai] Snapshotting HEAD (ref: refs/lbai/snapshots/...)`
     `[lbai] Executing prompt transaction...`
     `[lbai] Done.`

Include comprehensive unit tests in `pkg/gitengine` testing snapshots, stashing, and rollbacks using temporary Git fixtures.