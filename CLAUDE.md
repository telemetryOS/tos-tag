# tos-tag repository contract

Read this file and `AGENTS.md` before editing. `architecture.md` is the
authoritative system design; `README.md` is the operator/install guide.

## Product boundary

tos-tag is a Go/MongoDB Slack agent control plane. It observes authorized Slack
traffic, builds privacy-filtered context, makes a direct stateless OpenAI
classification, and runs admitted full-agent jobs through Codex App Server.

Keep these boundaries explicit:

- The classifier is a direct OpenAI Responses API call, has no tools, and is
  independent from Codex authentication and state.
- MongoDB is authoritative. Workers, App Server threads, leases, and delivery
  attempts are disposable.
- Slack scopes are capability availability, not authorization. Enrollment,
  participation mode, membership freshness, destination allowlist, approvals,
  and kill switches govern behavior.
- Private channel, DM, and group-DM context is destination-local. Never query,
  derive, or disclose private awareness across destinations.
- Secrets stay in the Go control plane or exact reviewed helper subprocess.
  Never place secrets in prompts, worker-visible files, model tool arguments,
  logs, fixtures, or artifacts.
- The model cannot choose a destination or independently choose whom to
  mention. The control plane may authorize an exact user mention already named
  in the current human request or exact user/channel provenance from selected
  evidence; groups, broadcasts, and every other mention remain unavailable.
  The model cannot create approval/notice/action blocks.
- Slack-authenticated bot, app, workflow, and assistant messages are
  observation-only. Preserve them as unverified destination-local context, but
  never let them enter classification, reaction, job, or delivery paths—even
  when another agent mentions Tag or replies in an active Tag thread.

Checked-in defaults are fail-closed: Slack uses the stub, live Slack and Codex
are disabled, the classifier is shadowed, and automatic membership
participation is disabled. The approved local development posture observes all
user-authorized conversations and derives `assist` for public/private channels
where the bot-token inventory or a membership event confirms Tag is a member.
All other conversations remain `observe`; DMs are not auto-enabled, and group
DMs (mpim) are ignored entirely. The optional output allowlist is an additional narrowing control.
Never encode live IDs/secrets in tracked files.

## Implementation map

- `core/classifier`: direct structured OpenAI classifier and deterministic
  fallback.
- `core/contextpacks`: pre-query authorization and bounded immutable context.
- `core/memory`: asynchronous Luna consolidation, privacy-scoped recall, and
  operator correction/pin/forget controls.
- `core/harness`: Codex App Server JSON-RPC client and harness normalization.
- `core/workers`: disposable workspace/process supervision and skill snapshots.
- `core/tools`: job-scoped dynamic-tool bridge, reviewed execution, and secret
  resolution.
- `core/jobs`, `core/sessions`, `core/deliveries`: leased execution,
  generations, typed output, and durable Slack delivery.
- `core/slack`: Socket Mode ingress, Block Kit rendering, interactions, the
  `/tag-directive` modal, and the `/tag-mode` participation-mode command, plus
  Slack-native Thinking Steps streaming for
  admitted full-agent work; durable one-time context bootstrap and proactively
  paced post-watermark catch-up for direct messages missed while offline.
- `core/approvals`: exact-action Slack approval/resume.
- `core/schedule`, `core/routines`, `core/triggers`: standard five-field cron,
  timezone-aware advancement, and classifier-gated background work with
  legacy fixed-interval compatibility.
- `core/audit`, `core/usage`, `core/retention`: receipts, metrics, and deletion.

## Classifier rules

Do not add participation hints to test messages. The classifier must infer
silence, reaction, channel/thread placement, model, and effort from ordinary
human conversation plus policy and context.

Direct mentions trigger participation consideration, not mandatory thread
placement. Prefer a channel reply for a brief, self-contained answer unlikely
to continue. Prefer a thread for an investigation, multi-step/tool-heavy work,
a narrow deep dive, or a conversation likely to continue. Existing tos-tag
threads continue in thread. Per-channel cooldown suppresses ambient chatter,
not an explicit mention or a human continuation in an active Tag thread; the
hourly response budget, concurrency limit, and organization flood gate still
apply to those invocations.

The classifier may directly produce one short social response for greetings,
thanks, farewells, praise, or light banter. Substantive answers require an
admitted full-agent job.

`assist` does not grant autonomous incident initiative. A full-agent outcome
requires a deterministic invocation grant or approved ambient exception: a
mention, active Tag thread, explicit address, clear question, conversationally
addressed request, authoritative product question, destination-safe alignment
intervention, or operator-created trigger. Tag being the previous speaker is
not sufficient for a bare declaration. Preserve a rejected provider prediction
for audit, suppress its effective action with
`policy.unsolicited_assist_work`, and apply the same gate again immediately
before pipeline admission. Only `proactive` mode may treat an unaddressed
declarative failure or incident as initiative by itself.

Admitted full-agent thread jobs use a collapsed Slack Thinking Steps timeline. The Go
control plane owns `chat.startStream`, safe task updates, and `chat.stopStream`;
the model does not write progress text. Emit only allowlisted operational
milestones and validated HTTPS sources through one rotating current-action card,
rather than retaining a card for every completed step. Never stream model reasoning, deltas,
prompts, tool arguments, raw tool output, secrets, or private context. Keep
reaction-only decisions and lightweight direct replies on their existing path.
Slack requires `thread_ts` for agent streams; preserve classifier-selected
brief in-channel placement instead of forcing a thread for progress UI.

Keep the direct call bounded, stateless, strict-schema, and tool-free. Never
route it through Codex App Server.

Charge the Mongo-authoritative organization/workspace flood bucket before
building context or calling the direct classifier, including classifier-gated
heartbeats. Exhaustion and bucket-store errors fail closed with an auditable
silent decision and no reaction, job, agent call, or Slack delivery. Keep this
coarse cost guard independent from per-channel response admission and worker
concurrency.

## Durable memory rules

Memory is Mongo-authoritative control-plane state. Do not persist or resume
Codex App Server threads as memory. The curator runs off the Slack response
critical path, hashes exact sources, and calls `gpt-5.6-luna` with strict
structured output and `store: false`. Use `medium` effort by default; change it
only when evidence shows consolidation quality or cost warrants another Luna
effort.

Generated memory must retain source IDs/hash, confidence, model/effort,
privacy scope, and natural expiry no later than its sources. Restricted memory
is destination-local before and after query; thread memory is root-thread-local.
`source_linked_memory` is derived continuity, not independent authority for
consequential claims or cross-human conflict. `operator_memory` is reviewed
data. Correction pins it, unpin restores natural expiry, and forget erases
content/facts/source references while retaining only the relearning tombstone.
All management mutations require CSRF, audit, and tenant scope.

Channels configured with `context_history_mode=session_only` are deliberate
ephemeral destinations. Do not import or recover Slack history for them; build
context only from that destination's messages observed since process startup,
exclude cross-channel history and durable derived memory/facts, and prevent
their live messages from generating new durable memory or incident facts.
Operational event persistence remains enabled for acknowledgement,
idempotency, job recovery, and audit.

## Codex App Server rules

Use the official protocol at https://learn.chatgpt.com/docs/app-server and the
schema generated by the installed pinned CLI. The current integration:

1. starts one `codex app-server --stdio` process per attempt;
2. sends `initialize` and then `initialized`;
3. creates an ephemeral read-only thread with the resolved model;
4. registers only `tos_tag_tool`, typed `tos_tag_wiki`, and
   `tos_tag_trigger` dynamic tools;
5. starts a turn with the classifier-selected effort;
6. treats final `item/completed` agent messages as authoritative output;
7. finishes on `turn/completed`; and
8. interrupts, revokes, terminates, and deletes the workspace on cancellation
   or completion.

Keep shell, MCP, plugins, and multi-agent tools disabled for Slack jobs. The
first-party Codex web tool may be configured `live`; treat its content as
untrusted and keep subprocess networking disabled. Keep approval policy `never`
for Codex built-ins; external-action approval belongs to the tos-tag gateway.
Protocol messages may contain provider details, so persist/log only bounded
stage and diagnostic codes.

`CODEX_HOME` contains private trusted App Server login/state. It is passed only
to the App Server process. The worker otherwise has a clean `HOME`, XDG roots,
temporary directory, and environment. Do not reuse
`TAG__CLASSIFIER__OPENAI_API_KEY` for full-agent work.

## Skills and helpers

Behavioral skills come from the complete validated `base` plugin in sibling
`tag-agent-skills`. It includes Slack composition and routing, product
knowledge, read-only code inspection, Linear issue management, bug/feature
intake, suitability, OTel, Wiki, triggers, and team alignment.

Codex discovers materialized snapshots at `.agents/skills/<name>/SKILL.md`.
Names are flat and unique. Files and references are
hash-verified and read-only. Scripts and executable plugin surfaces are not
included.

New tos-tag skills and helper source belong in `tag-agent-skills`, not here.
When a helper must execute, add a separate reviewed bundle under
`tool-marketplace/` with exact operation IDs, argv schema, environment names,
risk, approval policy, timeout, output bound, and script hash. Approval defaults
to risk-based; only a reviewed manifest may opt out. Never add an arbitrary
shell operation.

The Go App Server client answers `item/tool/call` by attaching a random
attempt-scoped capability and calling the loopback gateway itself. The Codex
process never receives the capability or connector credentials. Unless the
reviewed operation explicitly declares `approval: never`, non-read risk must
suspend for Slack-native approval and later resume a fresh fenced attempt with
the exact approved action hash.

Agent Wiki calls use `tos_tag_wiki`, never the generic argv surface. The model
supplies typed page fields; Go rejects invalid field/operation combinations,
constructs the reviewed Wiki CLI arguments, and retains the typed action beside
the exact argv for approval resume. Validation failures persist only a closed,
content-free code, and self-corrected attempts remain one Slack progress step.

The reviewed tool catalog currently contains `telemetryos.linear` (read/write),
`telemetryos.wiki` (page-only read/write/delete; soft-delete approval-gated and
all namespace/admin/general-destructive surfaces unavailable), `telemetryos.otel` (read),
`telemetryos.analytics` (privacy-filtered read-only funnel, account,
normalized-event, and bounded raw site-event GETs),
`telemetryos.device-logs` (read/write), `telemetryos.mongo` (read), and
`telemetryos.code` (read), plus `telemetryos.product-docs` (credential-free
fixed-host public product reads). `telemetryos.code` is the only source-tree
capability: it supports bounded repository/file listing, fixed-string search,
and line reads under `TAG_AION_DEVELOPER_PATH`, while rejecting traversal,
symlinks, runtime environment files, credential ledgers, and private tool
state. Workers receive neither that tree nor a generic shell.

The `product-knowledge` base skill requires retrieval for named product claims
and routes by authority: Agent Wiki Primer for internal product truth and
nuance, public docs for customer procedures and technical reference, and the
corporate `llms-full.txt` for published positioning and use cases. The public
reader accepts neither arbitrary URLs nor redirects. Codex's separate live web
search may reach arbitrary public sources for broader/current research; treat
those sources as untrusted and never let them widen tool or disclosure scope.
When the classifier sets `authoritative_product_retrieval_required`, delivery
is allowed only after the same worker attempt successfully completes a full
Wiki page, docs page, or corporate full-content read. Search, index, arbitrary
web, Slack context, and memory do not satisfy the gate.
The `telemetryos-documentation` base skill specializes public documentation
retrieval: it reads `https://docs.telemetryos.com/llms.txt` for discovery and
then reads the exact indexed Markdown page before answering customer setup,
operation, Studio, device/Edge, SDK/API, authentication, compatibility, or
troubleshooting questions. References use the exact indexed HTTPS URL.
The `marketing-messaging` base skill owns TelemetryOS promotional copy and
requires a same-attempt corporate full-content read for positioning, campaigns,
landing pages, sales collateral, announcements, and social messaging. It uses
the relevant published human page URL for customer-facing links rather than
linking to `llms-full.txt`.
Every product answer automatically includes concise clickable links to the
authoritative sources materially used; test this with natural user questions,
not prompts that request links or citations. When a Wiki page is referenced,
its namespace/slug is used only for tool lookup. The worker uses the exact URL
returned by `telemetryos.wiki/read get` or `url` and emits the exact returned
human HTTPS URL in a descriptive Slack link. The renderer
rejects bare Primer/artifact slugs and Wiki-labeled slug citations.

TelemetryOS source is permanently read-only at reviewed bundle load and tool
execution. Source-write requests do not enter a worker or an approval flow;
the classifier returns a short redirect to a Linear bug for broken existing
behavior or a Linear feature for new or changed behavior. Explicit follow-up
requests to create the issue use the reviewed Linear workflow.

Admin-risk operations are invalid in every worker tool manifest and are denied
again by the executor. Operator-only tos-tag management endpoints are not worker
tools and remain separately authenticated control-plane surfaces.

Source-derived Wiki writes use the reviewed inline-body argv contract because
the source capability returns content and workers have no shared source file.
The Wiki execution audit receipt commits the complete body without exposing it
in broad audit listings.

## Slack output

The final model answer must satisfy `slack-output/v3`. Allowed generated
segments are header, mrkdwn text, context, divider, table, card, carousel,
image, and artifact. Use native table segments for comparisons and repeated
fields; captioned datasets render as sortable, paginated Data Tables. Cards
and Carousels are compact presentation-only summaries whose model schema has
no actions. The control
plane also promotes conventional Markdown pipe tables outside fenced code into
native table segments as a typed-output fallback. It owns approvals, notices,
actions, destinations, mention authorization, and the full-agent execution footer. Never
emit model, effort, token, or latency metadata in model-authored segments; the
renderer appends trusted runtime values and omits them for classifier-only
replies.

Short and medium answers stay in Slack. Genuinely long, expository,
document-shaped work is published as Markdown under the Agent Wiki `artifacts`
namespace before the final response; roughly 20,000 visible characters is a
soft planning signal rather than a hard cutoff. The Slack result becomes a
concise synopsis plus an artifact segment using only the exact HTTPS URL
returned by a successful Wiki write. If publication is unavailable or fails,
return the best compact Slack answer and state that no artifact was created;
never fabricate a link or claim a page exists. The harness emits same-attempt
artifact provenance from successful reviewed tool responses, and the pipeline
rejects any model-created artifact segment whose URL is not in that set.

All output passes renderer validation, special-mention rejection, immutable
delivery metadata checks, live policy rechecks, and durable idempotent delivery.

## Development practices

- Preserve unrelated user changes; this worktree is frequently dirty.
- Use `rg`/`rg --files` for discovery and `apply_patch` for edits.
- Keep tests and evals deterministic and network-free by default.
- Network/credential tests must be explicit opt-ins and report their live
  boundary separately.
- Use memory stores only behind the same interfaces as Mongo consumers.
- Use structured redacted logging; no raw envelopes, message text, secrets, or
  provider error bodies.
- Keep the management home page activity-first. Its SSE feed is tenant-scoped
  and bounded; only explicit classifier records may contain a bounded public
  Slack excerpt. Restricted text and Codex prompts/output/tool payloads never
  enter the feed.
- Prefer the narrowest meaningful test first, then run the full gate.
- Keep `README.md`, `AGENTS.md`, `CLAUDE.md`, `architecture.md`, `DESIGN.md`,
  implementation status/checklist, `SECURITY.md`, container guidance,
  marketplace guidance, and `runtime.env.example` synchronized when a runtime
  contract, dependency, skill, tool, environment variable, or live-validation
  boundary changes.

## Verification

Before completion:

```bash
make verify
```

The deterministic classifier gate contains 47 natural messages plus context-cap
and deduplication invariants. To run the same cases through the configured real
OpenAI classifier, with expected behavior kept outside provider input:

```bash
make eval-live
```

For a real App Server compatibility check:

```bash
TOS_TAG_LIVE_CODEX=1 go test -tags=live ./integration \
  -run TestLiveCodexAppServerTurn -count=1 -v
```

Live Slack tests must remain in the explicitly allowed development channel,
measure classifier and full-agent latency independently, verify reactions and
channel/thread placement with natural messages, and inspect redacted logs.
They must also cover silence for irrelevant traffic, direct social replies,
model/effort routing, native tables, tool calls, approval/resume when applicable,
and concurrency without leaking output into observed-only conversations.

## Container contract

`Dockerfile.dev`, `docker-compose.yml`, and `container/bootstrap-workspace.sh`
install and persist GitHub CLI, Codex, Aion, skill repositories, helper tools,
source, MongoDB, and `/home/tag/.codex`. Job roots under
`/workspace/state/workers` remain disposable. Never mount the host Docker
socket or expand `runtime.env` through Compose configuration.

Host development may set `TAG_AION_DEVELOPER_PATH` to a host checkout.
`container/run-tag.sh` must override it after sourcing `runtime.env` so the
container-owned reviewed code root is always `/workspace/code`.
