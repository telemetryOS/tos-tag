# tos-tag implementation design

The authoritative architecture is [architecture.md](architecture.md). This
file is the concise map from the implemented source tree and operational assets
to that architecture.

| Source area | Architecture responsibility |
| --- | --- |
| `cmd/api`, `core/core.go` | composition root and ordered lifecycle |
| `core/config`, `core/database`, `core/server` | configuration, MongoDB, readiness, HTTP |
| `core/slack`, `core/observer`, `core/intelligence` | stub/live Socket Mode transports, observations, context discovery/backfill, organization timeline |
| `core/contextpacks`, `core/classifier`, `core/memory` | immutable bounded context, durable source-linked recall, and tool-free decision admission |
| `core/sessions`, `core/jobs`, `core/deliveries` | thread generations, leased work, Slack output contract |
| `core/modelrouter`, `core/harness`, `core/workers` | dynamic routing, Codex App Server, and disposable execution |
| `core/policy`, `core/tools`, `core/approvals` | authorization, reviewed tools, and exact-action approval |
| `core/audit`, `core/retention`, `core/usage` | receipts, integrity, metering, and deletion fan-out |
| `core/channelconfig`, `core/automations`, `core/routines`, `core/triggers` | channel directives plus channel-locked Slack/admin editing and scheduled/classifier-gated work |
| `tool-marketplace` | reviewed typed helper catalog, risk and approval-policy declarations, scripts, and behavioral tool guidance |
| `container`, `Dockerfile.dev`, `docker-compose.yml` | persistent operator/code/Codex environment with disposable Slack workers |
| `evals`, `integration` | deterministic behavioral/security contracts and explicit opt-in live compatibility tests |

Live Slack uses the `core/slack` Socket Mode adapter behind explicit
configuration; the deterministic stub remains the default test boundary.
The same package persists per-conversation history-bootstrap completion and
live watermarks in MongoDB. New conversations receive a context-only bootstrap.
Completed bot-joined channels receive a bounded startup and periodic gap scan
after the watermark; recovered ambient messages stay resolved context, while
direct mentions return to the durable decision queue. Exceptional Web API
reads are proactively paced per method.

Each channel may independently select durable or `session_only` context
history. A session-only destination skips Slack history bootstrap and offline
catch-up, builds context solely from that destination's messages observed
since the current process started, and does not recall or create durable
memory/situation facts. Live operational records remain durable for
acknowledgement, idempotency, recovery, and audit, but are not eligible as
future-session conversational context for that destination.

Normalized Slack messages have no expiration field or TTL index. Startup drops
the retired `message_expiry` index from existing databases, while context-pack
assembly applies its own configurable lookback (30 days by default). Raw
observations and immutable prompt/context revisions retain separate short TTLs.

`core/memory` asynchronously consolidates changed human channel/thread scopes
with Luna medium effort, stores source hashes, summaries, confidence, and
source-bound facts in MongoDB, and recalls them through destination-safe
filters. Operator correction pins reviewed memory; forgetting erases content
and retains only the relearning tombstone. Memory calls never block Slack
acknowledgement, classification, or response work.

Behavioral skills are loaded from the complete sibling `base` plugin,
hash-verified, and copied read-only into disposable workers. Skill scripts do
not grant executable authority.
The base plugin includes `team-alignment`, which turns classifier-admitted
public factual conflicts into neutral, attributed Slack reconciliation while
preserving destination-local private context, and `product-knowledge`, which
routes mandatory product retrieval among the Primer and bounded public product
sources. `telemetryos-documentation` treats the public documentation
`llms.txt` as a discovery index and reads the exact indexed page before
answering procedural or technical-reference questions. `marketing-messaging` requires the full corporate website source for
every TelemetryOS promotional-copy request and delegates concrete product-fact
validation back to `product-knowledge`. Source-mutation requests are silently
suppressed before worker admission. `code-change-intake` is used only for a
separate explicit request to create a Linear bug or feature and never grants
source-write authority.
Executable access is limited to `tos_tag_tool` operations in the reviewed tool
catalog plus the typed `tos_tag_wiki` page-CRUD facade; Aion source is not mounted and can be inspected only through bounded
`telemetryos.code` reads against an on-demand verified remote-default-branch
snapshot. Conceptual discovery uses pinned offline semantic search before an
exact decisive read. Linear, Wiki, and OTel workflows are likewise
described by base skills and executed only through their reviewed tools. Public
product content is available only through the
fixed-host `telemetryos.product-docs` read tool, while arbitrary public research
uses Codex's first-party live web search with untrusted-content handling and no
credential or subprocess-network access. Go validates Wiki fields and builds
the only admitted CLI argv; models cannot submit Wiki argv directly.
`tos_tag_trigger` separately manages
classifier-gated channel heartbeat subscriptions with explicit cron and IANA-timezone trigger
authority. Ordinary `assist` traffic cannot launch full-agent work from an
unaddressed declarative status update; only `proactive` or a deterministic
invocation grant may do so.

Source reads are permanently read-only at catalog load and immediately before
execution; there is no approval path for source mutation. For authoritative
product requests, delivery additionally requires successful same-attempt full
content retrieval from the Primer Wiki, public docs page, or corporate source.

Approval defaults to risk-based at the operation manifest. A reviewed bundle
may declare `approval: never`. The bounded `telemetryos.linear/intake`
operation uses that exception only for an explicitly requested bug/feature
workflow and restricts argv to issue creation, evidence comments, feature
normalization, and its suitability follow-up. Agent Wiki page read/write
authoring is the other exception. The recoverable Wiki page soft-delete always
requires exact-action approval. Namespace, asset, publish-file, cascading move,
generic undo, and admin Wiki operations are unavailable, and admin-risk
worker-tool manifests are rejected globally. Every
execution remains job-scoped, hash-pinned, bounded, kill-switchable, and
audited regardless of approval policy.

Slack delivery is graduated rather than character-gated. Short and medium
answers use typed Block Kit directly, including non-interactive Cards and
Carousels for compact entities/options and sortable/paginated Data Tables for
captioned datasets. Document-shaped long-form synthesis uses
the strong Sol-medium worker to publish Markdown under Agent Wiki `artifacts`, then
returns a synopsis and the exact successful write URL as a typed artifact.
About 20,000 visible characters is the soft planning signal; failed writes
produce a compact Slack fallback and never a fabricated link. The pipeline
accepts a model-created artifact segment only when the harness observed the
same URL in a successful reviewed tool response from that worker attempt.
Existing Wiki pages remain normal references, not produced artifacts: any
provided reference uses the exact human HTTPS URL returned by the reviewed
`url` read operation, and bare namespace/slugs are rejected before rendering.
Full-agent results also receive one final context footer with the resolved
model, effort, provider-reported turn tokens, elapsed worker time, and a compact
allowlisted summary of successfully used capabilities. The
control plane owns this metadata; model output and classifier-only replies do
not carry it.

Admitted full-agent thread jobs use the classifier-selected reaction as their
immediate acknowledgement and immediately set Slack's native assistant thread
status. Slack rotates generic lifecycle messages while the worker starts; every
native or reviewed tool call then receives an allowlisted, tool-specific status
such as consulting the Wiki, checking Linear, or querying telemetry. The control
plane refreshes the current value during long-running work and finishes with a
generic response-preparation status. It does not open a plan-mode stream or
create in-thread task-card pills. Model deltas, tool arguments, raw outputs,
private context, and chain-of-thought never enter the status. Reaction-only
classifier decisions remain reactions. Because status requires `thread_ts`, the
control plane does not override brief-answer-in-channel placement merely to show
progress.

New one-to-one DM full-agent sessions receive one best-effort
`assistant.threads.setTitle` update after durable job enqueue. Title derivation
is deterministic and control-plane-owned: Slack markup, URLs, code, controls,
and likely secrets are removed, suspicious input falls back to `Tag request`,
and the live transport rejects channel and group-DM targets. Title failures do
not affect job execution or delivery.

The management home page is the operator's live-operations surface rather than
a database browser. An organization-scoped Server-Sent Events stream presents a
bounded timeline of Slack intake, classifier input and outcome, agent jobs,
reviewed tools, delivery, and Codex App Server method/status traffic. Public
classifier records may include a short message excerpt; restricted conversation
content and Codex prompts, results, arguments, and provider bodies are always
redacted. Configuration, history tables, and diagnostics remain available one
navigation level below the activity view.

Checked-in settings stay fail-closed. The approved local development deployment
observes user-authorized conversations, defaults discoveries to `observe`, and
can derive `assist` only for public/private channels independently confirmed in
the bot-token membership inventory. Optional exact-ID output restrictions can
narrow that set further.
