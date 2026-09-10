Add a lightweight dependency visualizer and local status dashboard to `less-bad-ai` (`lbai`).

Objective: Give developers instant spatial awareness of what the agent touched, which architectural boundaries were tested, and how modules connect—without forcing them to read diffs.

Requirements:
1. Terminal Summary Output:
   - At the conclusion of `lbai run`, display an ASCII summary table:
     ┌────────────────────────────────────────────────────────┐
     │ less-bad-ai run summary                                │
     ├────────────────────────────────────────────────────────┤
     │ Modules Touched : ui/inventory, engine/gestures        │
     │ Invariants      : 3 checked, 0 violations              │
     │ Tech Lead Clean : 1 inline function extracted          │
     │ Status          : Committed (4f8a12c)                  │
     │ Undo Command    : lbai undo                            │
     └────────────────────────────────────────────────────────┘

2. Local Web Topology Server (`lbai ui` / `lbai visualizer`):
   - Command: `lbai ui`
     Flags:
       - `--port`, `-p`: Port to serve dashboard (default: 3141).
       - `--open`: Automatically open default web browser.
     - Also add flag to `lbai run`: `--serve` (spins up or refreshes dashboard after run).
   - Backend:
     - Embed HTML/JS frontend assets into the Go binary (`//go:embed`).
     - Provide a REST/SSE endpoint (`/api/topology` and `/api/events`) parsing:
       - Project directory structure and package imports into graph nodes and edges.
       - The latest trace from `.lbai/traces/`.
   - Frontend UI:
     - Visual dependency graph (using an embedded D3 or Cytoscape.js canvas).
     - Color-code project packages by layer (e.g., UI, Core, Data).
     - Visually highlight nodes modified during the most recent `lbai run`.
     - Side drawer showing the last prompt, reasoning summary, and the "Revert" button (which triggers `lbai undo` via API).

Include tests verifying that the dependency graph builder properly identifies internal package nodes and import edges across project source files.