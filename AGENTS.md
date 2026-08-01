# tos-tag agent guide

Read `CLAUDE.md` completely before changing this repository. It is the local
implementation contract. `architecture.md` is authoritative when design
documents differ, and `IMPLEMENTATION_CHECKLIST.md` tracks verified progress.

Current initiative constraints:

- Live Slack testing is permitted only in a dedicated development workspace and
  explicitly enrolled test channels.
- The development Slack installation is intentionally broadly granted for
  future agent surfaces. Treat those Slack scopes as capability availability,
  not runtime authority: tos-tag policy, enrollment, requester, destination,
  approval, and kill-switch checks still govern every read and write.
- Load Slack credentials from an approved secret store or the gitignored local
  `runtime.env`; never commit, log, or expose them to workers or prompts.
- Keep global classifier in `shadow` mode. Begin channels in `observe`, then enable
  `mention` only after ingestion is verified.
- Do not enable `assist`, `proactive`, credentialed connectors, or external
  writes without explicit approval.
- Keep normal tests and evals deterministic and network-free. Live Slack tests
  are opt-in and must report their exact scope and evidence separately.
- MongoDB is the production authority; unit tests may use project-owned memory
  stores behind the same consumer interfaces.
- Keep secrets outside workers, prompts, fixtures, logs, and artifacts.
- Update the checklist only after the implementation and its named verification
  are present.

## Headless skill sources

tos-tag owns no ad hoc skills inside this repository. Future tos-tag-specific
skills and their Bash helper source belong in the sibling private repository
`../tag-agent-skills`:

- author `SKILL.md` at `src/skills/<skill-name>/SKILL.md`;
- place Bash helper source at
  `src/skills/<skill-name>/scripts/<helper>.sh`;
- regenerate `plugins/base` with
  `python3 scripts/build-plugin.py`, then run its `--check` mode and
  `python3 scripts/validate.py`; and
- commit and push that repository independently.

The live local configuration automatically loads and injects two complete
behavioral plugins into every response worker:

1. `telemetryos-automation` from `../telemetryos-agent-skills` (the headless
   TelemetryOS workflow plugin); and
2. `base` from `../tag-agent-skills` (tos-tag-owned skills, currently including
   the job-scoped `tag-triggers` heartbeat-subscription workflow).

Configure each source with its root, `.claude-plugin/marketplace.json`, and
exact plugin name. Missing roots, manifests, or selected plugins fail startup.
Skill runtime names are flat and must be unique across both plugins. The
control plane snapshots validated behavioral files and materializes them
read-only under `.opencode/skills`; it never copies the entire repository or
loads Codex/Claude/Cursor manifests as executable OpenCode plugins.

Bash helpers may live beside their owning skill in `tag-agent-skills`, but they
are not included in behavioral snapshots and are never executed directly by
OpenCode. To make one usable, add a separately reviewed executable-tool
manifest, pin the helper hash and exact argv/ENV contract, bind its scope, and
invoke it only through the job-scoped `tos_tag_tool` capability gateway.

After either plugin changes, restart tos-tag to obtain new content hashes, run
the marketplace and worker tests plus `make verify`, and use an isolated
OpenCode `debug skill` check to prove the expected skill names are discoverable.

## Container workspace

`Dockerfile.dev`, `docker-compose.yml`, and `container/bootstrap-workspace.sh`
define the reproducible development environment. The persistent layout is:

- `/workspace/projects/tos-tag` for this repository;
- `/workspace/code` for Aion's `developer_path` and `aion sync` output;
- `/workspace/skills/telemetryos-agent-skills` and
  `/workspace/skills/tag-agent-skills` for behavioral plugin sources; and
- `/workspace/state` plus `/home/tag` for retained local state and tool auth.

The bootstrap installs `/workspace/AGENTS.md` only when it is absent. Keep that
default in `container/workspace-AGENTS.md`. Do not mount the host Docker socket,
persist individual Slack-worker sessions as authority, or let the durable
operator workspace replace per-job isolation.

Run the narrowest relevant test first. Before completion, run `make verify` and
report any unavailable security or integration gate exactly.
