# tos-tag agent guide

Read `CLAUDE.md` completely before changing this repository. It is the local
implementation contract. `architecture.md` is authoritative when design
documents differ, and `IMPLEMENTATION_CHECKLIST.md` tracks verified progress.

Current initiative constraints:

- Live Slack testing is permitted only in the dedicated development workspace,
  with all output hard-restricted to `#tos-tag` unless the user explicitly
  authorizes a different destination.
- The development Slack installation is intentionally broadly granted for
  future agent surfaces. Treat those Slack scopes as capability availability,
  not runtime authority: tos-tag policy, enrollment, requester, destination,
  approval, and kill-switch checks still govern every read and write.
- Load Slack credentials from an approved secret store or the gitignored local
  `runtime.env`; never commit, log, or expose them to workers or prompts.
- Keep checked-in defaults fail-closed: Slack `stub`, classifier `shadow`, new
  conversations `observe`, Codex/tools disabled. The currently approved local
  regression deployment may use the live direct classifier and `assist` only
  in `#tos-tag`; broad user-authorized discovery remains observe-only.
- Treat a direct mention as a hard participation trigger, not a hard thread
  placement. Prefer an in-channel response for a brief, self-contained answer
  unlikely to continue; use a thread for deeper, multi-step, tool-heavy, narrow,
  or likely-to-continue work. Once a tos-tag thread is active, continue there.
- Ambient alignment interventions may use recent destination-safe public
  reports to surface a material factual conflict when doing so prevents
  confusion or a bad operational decision. Attribute reports neutrally, never
  infer channel membership from recent participation, and never use another
  private channel, DM, or group DM for an intervention.
- Keep short and medium results native to Slack. For genuinely long,
  expository, document-shaped work, have the strong/max worker publish Markdown
  under the Agent Wiki `artifacts` namespace and return a concise Slack synopsis
  plus the exact URL from the successful write. Treat roughly 20,000 visible
  characters as a soft planning signal, never fabricate a Wiki link, and fall
  back to a compact Slack answer if publication fails. The control plane must
  reject a model-created artifact segment unless its URL came from a successful
  reviewed tool call in that same worker attempt.
- Do not broaden `assist`/`proactive`, the Slack output allowlist, credentialed
  helpers, or external writes beyond the current explicit authorization.
- Keep normal tests and evals deterministic and network-free. Live Slack tests
  are opt-in and must report their exact scope and evidence separately.
- Keep classifier tests naturalistic: put expected silence, reaction, placement,
  model, and effort in evaluator metadata, never in the Slack message. Do not
  send outcome cues such as `no response needed`, `stay silent`, or `reply in a
  thread` merely to make a probe pass. Test explicit placement language only as
  a separately labeled user-intent contract.
- MongoDB is the production authority; unit tests may use project-owned memory
  stores behind the same consumer interfaces.
- Keep secrets outside workers, prompts, fixtures, logs, and artifacts.
- Update the checklist only after the implementation and its named verification
  are present.
- Keep `README.md`, `architecture.md`, `IMPLEMENTATION_STATUS.md`,
  `IMPLEMENTATION_CHECKLIST.md`, `SECURITY.md`, `runtime.env.example`, and the
  container guide synchronized with current code. Verify names and defaults
  against `core/config`, `Makefile`, manifests, and tool catalogs instead of
  copying older status text.

Current local regression baseline (2026-08-01): direct classifier and ambient
silence/social placement, native tables, approval/resume, Wiki and reviewed
source access, three overlapping jobs on the eight-worker pool, private-context
isolation, the deterministic 35-case eval, the opt-in live OpenAI 35-case eval,
and full `make verify` all passed. `make eval-live` must use only natural message
text; evaluator outcomes, placement, reactions, model, and effort remain outside
the provider input. Treat this as development evidence, not as authorization to
widen output or as proof of production readiness.

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
   `slack-message-design` for typed Block Kit composition, the job-scoped
   `tag-triggers` heartbeat-subscription workflow, and `team-alignment` for
   privacy-safe cross-channel factual reconciliation).

Configure each source with its root, `.claude-plugin/marketplace.json`, and
exact plugin name. Missing roots, manifests, or selected plugins fail startup.
Skill runtime names are flat and must be unique across both plugins. The
control plane snapshots validated behavioral files and materializes them
read-only under `.agents/skills`; it never copies the entire repository or
loads Codex/Claude/Cursor manifests as executable plugins.

Bash helpers may live beside their owning skill in `tag-agent-skills`, but they
are not included in behavioral snapshots and are never executed directly by
Codex App Server. To make one usable, add a separately reviewed executable-tool
manifest, pin the helper hash and exact argv/ENV contract, bind its scope, and
invoke it only through the job-scoped `tos_tag_tool` capability gateway.

The reviewed runtime catalog is `tool-marketplace/`. It is deliberately
separate from both behavioral skill repositories. A tool bundle contains only
`SKILL.md`, `tool.json`, and one executable reviewed script. Every operation
declares its exact environment names, timeout, output bound, risk, and optional
approval policy. Omitted approval remains risk-based; only reviewed manifests
may opt out. The
Codex App Server worker cannot supply secret references: tos-tag imports only the
selected tools' declared variables into its encrypted organization-scoped
keystore and derives their opaque bindings server-side. Keep the executor PATH
deterministic and never add an arbitrary shell operation.

The reviewed catalog currently contains:

- `telemetryos.code` (`read` only): bounded repository/file listing,
  fixed-string search, and numbered source reads under the server-owned
  `TAG_AION_DEVELOPER_PATH`; rejects traversal, symlinks, runtime environment
  files, and credential ledgers;
- `telemetryos.linear` (`read`, `write`);
- `telemetryos.wiki` (`read`, `write`, `delete`; page CRUD only): ordinary page
  reads/writes execute without per-action approval, recoverable page soft-delete
  always requires approval, and namespace/assets/publish-file/cascading-move/
  activity/undo/admin operations are unavailable;
- `telemetryos.otel` (`read`);
- `telemetryos.device-logs` (`read`, `write`); and
- `telemetryos.mongo` (`read`, disabled by default pending the human-opened
  security-key session).

Wiki content obtained through `telemetryos.code` must use the reviewed inline
body argument. Disposable workers have no shared source filename; never invent
`/workspace/...` paths. The exact body is committed by the Wiki execution audit
receipt.

Behavioral skill presence is not tool authority. The current plugin inventory
is 29 skills in `telemetryos-automation` and three in `base`; use the checked-in
plugin manifests as the source of truth and keep the exact list in `README.md`.

For local setup, run `make sync-tool-env`. The script copies only the known
Linear, Wiki, SigNoz, and DLA variables from the current shell or
`~/.config/telemetryos`, writes them to ignored mode-0600 `runtime.env`, and
enables the corresponding reviewed tools. It reports variable names only.
It also binds `TAG_AION_DEVELOPER_PATH` for the read-only `telemetryos.code`
tool. Workers may list repositories/files, fixed-string search, and read a
bounded source range through that capability; never mount the source tree into
their disposable workspace or add shell/write operations to the code tool.
Never print, inspect, commit, or paste their values. Mongo access remains
disabled until `MONGO_QA_URI` exists and a human has opened the security-key
session; adding it requires an explicit injected-tool allowlist change.

After either plugin changes, restart tos-tag to obtain new content hashes, run
the marketplace and worker tests plus `make verify`, and use App Server
`skills/list` with the disposable worker `cwd` when a live discovery check is
needed.

## Container workspace

`Dockerfile.dev`, `docker-compose.yml`, and `container/bootstrap-workspace.sh`
define the reproducible development environment. The persistent layout is:

- `/workspace/projects/tos-tag` for this repository;
- `/workspace/code` for Aion's `developer_path` and `aion sync` output;
- `/workspace/skills/telemetryos-agent-skills` and
  `/workspace/skills/tag-agent-skills` for behavioral plugin sources; and
- `/workspace/state` plus `/home/tag` for retained local state and tool auth.

Bootstrap also clones and installs `telemetry-otel-fetch`,
`Device-Log-Analyzer`, and `TelemetryOS-Mongo-Fetch` into
`/home/tag/.local/bin`; the reviewed executor PATH includes that directory.

The bootstrap installs `/workspace/AGENTS.md` only when it is absent. Keep that
default in `container/workspace-AGENTS.md`. Do not mount the host Docker socket,
persist individual Slack-worker sessions as authority, or let the durable
operator workspace replace per-job isolation.

`runtime.env` may contain a host `TAG_AION_DEVELOPER_PATH`. Container startup
must override it with `/workspace/code` after sourcing the file; otherwise the
reviewed code capability starts with an invalid host-only binding.

Run the narrowest relevant test first. Before completion, run `make verify` and
report any unavailable security or integration gate exactly. For documentation
changes, also run `git diff --check`, `bash -n` on changed shell scripts, search
for stale runtime/config terms, and verify every referenced local path exists.
