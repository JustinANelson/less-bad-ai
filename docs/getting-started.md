# Getting Started

## The shortest path

Install a release, enter a project directory, and run two commands:

```text
lbai setup
lbai run "describe the change you want"
```

Setup performs the mechanical preparation without asking configuration
questions. It finds Git, detects Codex, Claude, or Aider, detects conventional
project tests, persists the detected configuration, and creates an initial Git
baseline when one does not exist.

An empty project may have no checks to detect during setup. In that case,
`lbai run` discovers checks again after the agent generates the project, so a
newly created supported manifest is built and tested before the run commits.

If the project already has a Git commit, setup never commits existing work. In
a clean worktree it commits only the newly detected `.lbai/config.toml`; in a
dirty worktree it leaves the configuration uncommitted alongside the existing
work. It also ensures that a repository-local Git author identity is available.
`lbai run` works without setup in an already committed project by using
read-only automatic discovery.

## Readiness checks

Run `lbai doctor` to check Git, the initial baseline, agent and reviewer
commands, credentials, verification executables, Git identity, configuration,
and architectural rules:

```text
lbai doctor
lbai doctor --json
```

Failures include the command needed to fix the project. Warnings identify
optional hardening, such as adding an explicit build check.

## Safe initial baselines

Creating the first commit necessarily tracks the initial project files. Setup
respects `.gitignore` and refuses to create that commit when an unignored file
looks like a common secret:

- `.env` and environment variants;
- private-key and certificate containers;
- SSH private-key names;
- common credential JSON files; and
- user-level package-manager credential files.

Add such files to `.gitignore` and rerun setup. The
`lbai setup --allow-sensitive` override is available for repositories where
tracking a flagged file is deliberate.

When Git has no configured identity, setup adds `Less Bad AI <lbai@localhost>`
to that repository only. Supply a real identity immediately with:

```text
git config user.name "Your Name"
git config user.email "you@example.com"
```

Alternatively, provide it during setup:

```text
lbai setup --git-name "Your Name" --git-email "you@example.com"
```

## Custom configuration

Most users can rely on discovery. Edit `.lbai/config.toml` when a project needs
a specific agent, model, or verification graph, and add `.lbai/rules.toml` when
the automatically inferred architecture is not sufficient. Run `lbai doctor`
after editing either file.

## Installation alternatives

The repository-level `install.ps1` and `install.sh` scripts download a matching
release archive, verify it against `checksums.txt`, and install both executable
names. Set `LBAI_VERSION` and `LBAI_INSTALL_DIR` for a pinned Unix installation,
or pass `-Version` and `-InstallDir` to the PowerShell installer.

Release archives can also be downloaded directly from GitHub and contain both
`lbai` and the `less-bad-ai` alias. Source builds remain available for Go users.
