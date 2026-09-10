Add an automated memory and documentation subsystem to `less-bad-ai` (`lbai`).

Objective: Prevent long-term context loss by automatically recording decision traces, updating project architecture manifests, and generating semantic commit logs on every `lbai run`.

Requirements:
1. Living Architecture Manifest (`pkg/memory`):
   - Automatically maintain `ARCHITECTURE.md` and `AI_CONTEXT.md` in the repo root.
   - After code is verified, run a lightweight extraction prompt:
     - Summarize what changed in 2 sentences.
     - Document any new interfaces, state structures, or cross-package communication patterns.
     - Update the "Topology" and "Recent Decisions" sections in `ARCHITECTURE.md`.

2. Trace Store (`.lbai/traces/`):
   - Persist structured trace logs for every run:
     - Location: `.lbai/traces/<timestamp>_<commit_sha>.json`
     - Fields: `trace_id`, `commit_sha`, `timestamp`, `user_prompt`, `worker_summary`, `tech_lead_modifications`, `touched_files`, `adr_decision`
   - Maintain a chronological human-readable summary in `docs/decisions/LOG.md`.

3. Commit Synthesis & Auto-Commit:
   - Generate narrative conventional commit messages based on the trace:
     ```
     feat(inventory): implement card drag-and-drop mechanics

     Why: Extracted inline card listeners to GestureService to maintain UI/Engine boundaries.
     LBAI-Trace: .lbai/traces/2026-09-09_4f8a12c.json
     ```
   - Automatically commit the staged code along with the updated `ARCHITECTURE.md` and `.lbai/traces/` artifacts.

4. CLI Commands & Flags:
   - `lbai log`: View recent LBAI traces, prompts, and affected modules.
     Flags:
       - `--limit`, `-n`: Number of entries to show (default: 5).
       - `--diff`: Show git diff associated with the selected trace.