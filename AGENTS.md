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
- Keep the Mongo-authoritative organization/workspace flood gate ahead of
  context construction and every direct classifier call, including heartbeat
  gates. Exhaustion or gate-store failure is an auditable silent drop with no
  reaction, worker, or Slack output. Keep its default window coarse (one hour)
  and separate from per-channel response admission.
- Treat a direct mention as a hard participation trigger, not a hard thread
  placement. Prefer an in-channel response for a brief, self-contained answer
  unlikely to continue; use a thread for deeper, multi-step, tool-heavy, narrow,
  or likely-to-continue work. Once a tos-tag thread is active, continue there,
  except when a human reply begins by addressing another Slack user and neither
  mentions nor explicitly addresses Tag; treat that as a human-to-human handoff
  and stay silent. Apply per-channel cooldown only to ambient chatter; never let
  it discard a direct mention or a human continuation in an active Tag thread.
  Keep hourly response budgets, concurrency limits, and the organization flood
  gate intact.
- Use Slack Thinking Steps for admitted full-agent thread jobs as the progress
  surface. Reuse one transient current-action task card for every native or
  reviewed tool and dynamically declared validated skill, replacing it as work
  advances instead of accumulating completed cards. Keep titles to safe operational facts from reviewed control-plane
  events; never expose chain-of-thought, model deltas, raw tool arguments/output,
  credentials, or private context. When answer work is
  admitted, immediately apply the classifier-selected emoji to the source
  message as an acknowledgement; preserve reactions for intentional
  reaction-only and lightweight classifier outcomes, and keep background and
  approval outcomes reaction-free.
  Slack requires a stream `thread_ts`; do not force brief in-channel answers
  into threads solely to obtain a progress surface.
- Treat a strong/high-effort full-agent recommendation as substantial work and
  correct it to a thread unless the requester explicitly requires in-channel
  placement. This keeps long-running synthesis on the Thinking Steps surface.
- Treat the full-agent model/effort/token/latency/activity footer as control-plane-owned.
  Capture provider-reported turn usage and a compact allowlisted summary of
  successfully used capabilities, append one final context block, and
  omit it from direct classifier replies, reactions, approvals, and notices.
  Never ask the model to author this execution metadata.
- Disable Slack link and media unfurls on ordinary posts, updates, stream
  finalization, and fallback delivery. Keep citations clickable but compact;
  do not let Slack append quoted message/page previews below Tag's answer.
- Never classify Tag's own Slack callbacks. Import them directly as authorized,
  resolved destination-local context so conversational follow-ups still work,
  acknowledge them without decision admission, and emit no classifier, job,
  reaction, delivery, or activity-card work for the callback itself.
- Treat every Slack-authenticated bot, app, workflow, or assistant message the
  same way for loop prevention: retain it only as unverified destination-local
  context, never classify it, react to it, or start/deliver work from it. This
  applies even when another agent mentions Tag or posts in an active Tag thread.
- Ambient alignment interventions may use recent destination-safe public
  reports to surface a material factual conflict when doing so prevents
  confusion or a bad operational decision. Attribute reports neutrally, never
  infer channel membership from recent participation, and never use another
  private channel, DM, or group DM for an intervention.
- Keep short and medium results native to Slack. For genuinely long,
  expository, document-shaped work, have the strong Sol-medium worker publish Markdown
  under the Agent Wiki `artifacts` namespace and return a concise Slack synopsis
  plus the exact URL from the successful write. Treat roughly 20,000 visible
  characters as a soft planning signal, never fabricate a Wiki link, and fall
  back to a compact Slack answer if publication fails. The control plane must
  reject a model-created artifact segment unless its URL came from a successful
  reviewed tool call in that same worker attempt.
- Do not broaden `proactive`, credentialed helpers, or external writes beyond
  current explicit authorization. Membership-managed `assist` is authorized
  only for Slack-confirmed joined channels and reverts to `observe` on leave.
- In `assist`, never admit full-agent work from an unmentioned top-level bare
  status declaration, even when Tag spoke last. Require a deterministic
  invocation grant or an approved ambient exception, preserve the provider
  prediction for audit, and recheck the grant immediately before admission.
  Operator-created classifier-gated triggers carry their own explicit grant;
  free-form directives do not silently upgrade a channel to `proactive`.
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
  repair completed bot-joined channels strictly after the watermark, promote
  only human direct mentions, keep all other recovered history resolved, never
  replay first-time history as work, and
  proactively pace each Web API history method rather than using HTTP 429
  responses as the normal scheduler.
- Channel policy may set `context_history_mode=session_only` for noisy test
  destinations. Such a destination skips history import and offline catch-up,
  queries only its own messages observed after the current process started,
  recalls no durable memory or situation facts, and produces no new durable
  memory or incident facts. Live events remain durable for acknowledgement,
  audit, idempotency, and job recovery.
- Update the checklist only after the implementation and its named verification
  are present.
- Use standard five-field cron plus a separate IANA timezone for new routines
  and classifier-gated trigger subscriptions. Keep legacy fixed intervals
  readable until migrated, and surface schedules through the combined
  operator-facing Automation page rather than raw routine/trigger tables.
- Keep `README.md`, `architecture.md`, `IMPLEMENTATION_STATUS.md`,
  `IMPLEMENTATION_CHECKLIST.md`, `SECURITY.md`, `runtime.env.example`, and the
  container guide synchronized with current code. Verify names and defaults
  against `core/config`, `Makefile`, manifests, and tool catalogs instead of
  copying older status text.

Current local regression baseline (2026-08-04): direct classifier and ambient
silence/social placement, native Tables/Data Tables, presentation-only
Cards/Carousels, approval/resume, Wiki and reviewed
source access, three overlapping jobs on the eight-worker pool, private-context
isolation, the deterministic 50-case eval, the latest opt-in live OpenAI
48-case baseline (before the ambient Wiki report-link regression was added),
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
invoke it only through the appropriate job-scoped capability gateway. Wiki
page CRUD uses typed `tos_tag_wiki`; other catalog tools use `tos_tag_tool`.

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
  fixed-string search, numbered source reads, and deterministic manifest/build/
  CI version evidence under the server-owned `TAG_AION_DEVELOPER_PATH`; rejects
  traversal, symlinks, runtime environment files, and credential ledgers;
- `telemetryos.product-docs` (`read` only, no approval): fixed-host HTTPS reads
  of the public documentation index/pages and corporate `llms-full.txt`; no
  arbitrary URLs, redirects, headers, methods, credentials, or shell;
- `telemetryos.linear` (`read`, `write`);
- `telemetryos.wiki` (`read`, `write`, `delete`; page CRUD only): ordinary page
  reads/writes execute without per-action approval, recoverable page soft-delete
  always requires approval, and namespace/assets/publish-file/cascading-move/
  activity/undo/admin operations are unavailable;
- `telemetryos.otel` (`read`);
- `telemetryos.analytics` (`read` only, no approval): fixed funnel, website,
  account, normalized-event, and bounded raw site-event GETs through the Site
  Analytics Token boundary with direct-identifier and free-form-property
  filtering and no visitor/session lookup;
- `telemetryos.device-logs` (`read`, `write`); and
- `telemetryos.mongo` (`read`, disabled by default pending the human-opened
  security-key session).

Wiki content obtained through `telemetryos.code` must use the typed Wiki
`body` field. Go constructs the reviewed inline body argument. Disposable
workers have no shared source filename; never invent `/workspace/...` paths.
The exact action is committed by the Wiki execution audit receipt.

Behavioral skill presence is not tool authority. The current inventory is 18
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
Prefer that human URL, but an unresolved namespace/slug may remain readable in
internal Slack rather than invalidating the answer. Opaque page URLs must never
be guessed. Every reviewed `get` returns a full page envelope containing that
URL.
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
Linear, Wiki, SigNoz, DLA, and optional Site Analytics variables from the current shell or
`~/.config/telemetryos`, writes them to ignored mode-0600 `runtime.env`, and
enables the corresponding reviewed tools. Analytics remains disabled when no
Site Analytics Token is available. It reports variable names only.
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
