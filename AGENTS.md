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
- Treat the web management home page as the live operations surface. Keep its
  SSE activity feed organization-scoped and bounded; show source text only as a
  bounded public classifier excerpt, hide restricted content, and expose Codex
  method/status lifecycle without prompts, output, provider bodies, tool
  payloads, or credentials. Configuration and raw-data inspection remain
  secondary navigation.
- Keep checked-in defaults fail-closed: Slack `stub`, classifier `shadow`, new
  conversations `observe`, automatic membership participation disabled, and
  Codex/tools disabled. The approved local deployment may derive `assist` for
  public/private channels where Slack confirms Tag is a member; other
  user-authorized conversations remain observe-only. DMs and group DMs are
  never auto-enabled.
- Treat a direct mention as a hard participation trigger, not a hard thread
  placement. Prefer an in-channel response for a brief, self-contained answer
  unlikely to continue; use a thread for deeper, multi-step, tool-heavy, narrow,
  or likely-to-continue work. Once a tos-tag thread is active, continue there.
- Use Slack Thinking Steps for admitted full-agent thread jobs instead of an `eyes`
  reaction as a generic working indicator. Keep task titles to safe operational
  facts from reviewed control-plane events; never expose chain-of-thought, model
  deltas, raw tool arguments/output, credentials, or private context. Preserve
  reactions for intentional reaction-only and lightweight classifier outcomes.
  Slack requires a stream `thread_ts`; do not force brief in-channel answers
  into threads solely to obtain a progress surface.
- Treat the full-agent model/effort/token/latency footer as control-plane-owned.
  Capture provider-reported turn usage, append one final context block, and
  omit it from direct classifier replies, reactions, approvals, and notices.
  Never ask the model to author this execution metadata.
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
- Do not broaden `proactive`, credentialed helpers, or external writes beyond
  current explicit authorization. Membership-managed `assist` is authorized
  only for Slack-confirmed joined channels and reverts to `observe` on leave.
- Keep normal tests and evals deterministic and network-free. Live Slack tests
  are opt-in and must report their exact scope and evidence separately.
- Keep classifier tests naturalistic: put expected silence, reaction, placement,
  model, effort, retrieval, and source-rendering behavior in evaluator metadata,
  never in the Slack message. Do not send outcome or method cues such as `no
  response needed`, `stay silent`, `reply in a thread`, `use tools`, or `include
  a clickable link` merely to make a probe pass. Test explicit placement or
  citation language only as separately labeled user-intent contracts.
- MongoDB is the production authority; unit tests may use project-owned memory
  stores behind the same consumer interfaces.
- Treat durable agent memory as control-plane state, never Codex session state.
  Curate it asynchronously with the configured Luna model, preserve source
  IDs/hash/confidence/expiry, keep restricted memory destination-local, and
  require corroboration before model-derived memory grounds consequential
  claims. Human correction pins operator memory; forget must erase content and
  retain only the source-hash tombstone.
- Keep secrets outside workers, prompts, fixtures, logs, and artifacts.
- Keep Slack context ingestion event-driven after a one-time bounded
  conversation bootstrap. Persist content-free completion/live watermarks,
  backfill only new or interrupted conversations, and proactively pace each Web
  API history method rather than using HTTP 429 responses as the normal scheduler.
- Update the checklist only after the implementation and its named verification
  are present.
- Keep `README.md`, `architecture.md`, `IMPLEMENTATION_STATUS.md`,
  `IMPLEMENTATION_CHECKLIST.md`, `SECURITY.md`, `runtime.env.example`, and the
  container guide synchronized with current code. Verify names and defaults
  against `core/config`, `Makefile`, manifests, and tool catalogs instead of
  copying older status text.

Current local regression baseline (2026-08-02): direct classifier and ambient
silence/social placement, native Tables/Data Tables, presentation-only
Cards/Carousels, approval/resume, Wiki and reviewed
source access, three overlapping jobs on the eight-worker pool, private-context
isolation, the deterministic 41-case eval, the opt-in live OpenAI 41-case eval,
and full `make verify` all passed. `make eval-live` must use only natural message
text; evaluator outcomes, placement, reactions, model, and effort remain outside
the provider input. Treat this as development evidence, not as authorization to
widen output or as proof of production readiness.

## Behavioral skill source

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

The live local configuration automatically loads and injects the complete
`base` plugin from `../tag-agent-skills` into every response worker. It owns
Slack composition, triggers, alignment, product knowledge, read-only code
inspection, Linear management, bug/feature intake, suitability, OTel, and Wiki
behavior.

Configure its root, `.claude-plugin/marketplace.json`, and exact plugin name.
A missing root, manifest, or selected plugin fails startup. Skill runtime names
are flat and unique. The
control plane snapshots validated behavioral files and materializes them
read-only under `.agents/skills`; it never copies the entire repository or
loads Codex/Claude/Cursor manifests as executable plugins.

Bash helpers may live beside their owning skill in `tag-agent-skills`, but they
are not included in behavioral snapshots and are never executed directly by
Codex App Server. To make one usable, add a separately reviewed executable-tool
manifest, pin the helper hash and exact argv/ENV contract, bind its scope, and
invoke it only through the job-scoped `tos_tag_tool` capability gateway.

The reviewed runtime catalog is `tool-marketplace/`. It is deliberately
separate from the behavioral skill repository. A tool bundle contains only
`SKILL.md`, `tool.json`, and one executable reviewed script. Every operation
declares its exact environment names, timeout, output bound, risk, and optional
approval policy. Omitted approval remains risk-based; only reviewed manifests
may opt out. A reviewed `public_env` entry may expose only a credential-free
HTTPS `*_URL`; malformed, credential-bearing, query-bearing, and fragment-bearing
values remain secret and fail closed. The
Codex App Server worker cannot supply secret references: tos-tag imports only the
selected tools' declared variables into its encrypted organization-scoped
keystore and derives their opaque bindings server-side. Keep the executor PATH
deterministic and never add an arbitrary shell operation.

The reviewed catalog currently contains:

- `telemetryos.code` (`read` only): bounded repository/file listing,
  fixed-string search, and numbered source reads under the server-owned
  `TAG_AION_DEVELOPER_PATH`; rejects traversal, symlinks, runtime environment
  files, and credential ledgers;
- `telemetryos.product-docs` (`read` only, no approval): fixed-host HTTPS reads
  of the public documentation index/pages and corporate `llms-full.txt`; no
  arbitrary URLs, redirects, headers, methods, credentials, or shell;
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

Behavioral skill presence is not tool authority. The current inventory is 14
skills in `base`; use the checked-in plugin manifest as the source of truth and
keep the exact list in `README.md`.

For TelemetryOS product questions, apply `product-knowledge`: retrieve named
product claims from the Agent Wiki Primer, public docs, and/or corporate source
according to the claim and audience. Do not let a worker substitute generic
model memory for available product evidence. When the classifier marks
authoritative product retrieval as required, the pipeline must observe a
successful same-attempt full-content Wiki page, docs page, or corporate source
read before delivery. Search/index/web/Slack context alone is insufficient.
Every product answer automatically includes concise clickable links to the
authoritative sources materially used; a requester never needs to ask for them.
For a Wiki source, use the exact human HTTPS URL returned by the reviewed Wiki
`get` or `url` read operation and render it as a descriptive Slack link.
Namespace/slugs are internal lookup identifiers and must never be delivered as
citations; opaque page URLs must never be guessed. Every reviewed `get` returns
a full page envelope containing that URL.
The reviewed product reader is the preferred deterministic path; arbitrary Codex live web search is available for
broader/current research but remains untrusted and cannot widen authority.
For customer-facing procedures and technical reference, apply
`telemetryos-documentation`: read `docs-index`, select an exact listed page,
then read that `docs-page` before answering. The index is discovery metadata,
not sufficient authoritative evidence, and supplied references must use the
exact indexed HTTPS URL.
For TelemetryOS marketing copy, positioning, campaigns, landing pages, sales
collateral, announcements, or social posts, apply `marketing-messaging` and
require a same-attempt `telemetryos.product-docs/read corporate-full` before
drafting. Use the relevant human page URL from that source for customer links.

TelemetryOS source access is permanently read-only. Enforce that invariant when
loading a reviewed code bundle and again immediately before execution; never
add or approve an edit, patch, commit, push, merge, deploy, or generic shell
operation. Classify source-mutation requests for a brief control-plane redirect
to a Linear bug for broken existing behavior or a Linear feature for new or
changed behavior. Use the `code-change-intake` skill only after explicit issue
creation intent; normal reviewed Linear approval still applies.

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

After the plugin changes, restart tos-tag to obtain new content hashes, run
the marketplace and worker tests plus `make verify`, and use App Server
`skills/list` with the disposable worker `cwd` when a live discovery check is
needed.

## Container workspace

`Dockerfile.dev`, `docker-compose.yml`, and `container/bootstrap-workspace.sh`
define the reproducible development environment. The persistent layout is:

- `/workspace/projects/tos-tag` for this repository;
- `/workspace/code` for Aion's `developer_path` and `aion sync` output;
- `/workspace/skills/tag-agent-skills` for the behavioral plugin source; and
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
