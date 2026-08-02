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
| `core/channelconfig`, `core/routines`, `core/triggers` | channel directives and scheduled/classifier-gated work |
| `tool-marketplace` | reviewed typed helper catalog, risk and approval-policy declarations, scripts, and behavioral tool guidance |
| `container`, `Dockerfile.dev`, `docker-compose.yml` | persistent operator/code/Codex environment with disposable Slack workers |
| `evals`, `integration` | deterministic behavioral/security contracts and explicit opt-in live compatibility tests |

Live Slack uses the `core/slack` Socket Mode adapter behind explicit
configuration; the deterministic stub remains the default test boundary.
The same package persists per-conversation history-bootstrap completion and
live watermarks in MongoDB. Startup skips completed conversations, periodic
discovery seeds only new/interrupted conversations, and the exceptional Web API
reads are proactively paced per method.

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
validation back to `product-knowledge`. `code-change-intake` routes
source-mutation intent to a Linear bug or feature instead of granting a worker
source-write authority.
Executable access is limited to `tos_tag_tool` operations in the reviewed tool
catalog; Aion source is not mounted and can be inspected only through bounded
`telemetryos.code` reads. Linear, Wiki, and OTel workflows are likewise
described by base skills and executed only through their reviewed tools. Public
product content is available only through the
fixed-host `telemetryos.product-docs` read tool, while arbitrary public research
uses Codex's first-party live web search with untrusted-content handling and no
credential or subprocess-network access. `tos_tag_trigger` separately manages
classifier-gated channel heartbeat subscriptions.

Source reads are permanently read-only at catalog load and immediately before
execution; there is no approval path for source mutation. For authoritative
product requests, delivery additionally requires successful same-attempt full
content retrieval from the Primer Wiki, public docs page, or corporate source.

Approval defaults to risk-based at the operation manifest. A reviewed bundle
may declare `approval: never`; the current exception is Agent Wiki page
read/write authoring. The recoverable Wiki page soft-delete always requires
exact-action approval. Namespace, asset, publish-file, cascading move, generic
undo, and admin Wiki operations are unavailable, and admin-risk worker-tool
manifests are rejected globally. Every
execution remains job-scoped, hash-pinned, bounded, kill-switchable, and
audited regardless of approval policy.

Slack delivery is graduated rather than character-gated. Short and medium
answers use typed Block Kit directly, including non-interactive Cards and
Carousels for compact entities/options and sortable/paginated Data Tables for
captioned datasets. Document-shaped long-form synthesis uses
the strong/max worker to publish Markdown under Agent Wiki `artifacts`, then
returns a synopsis and the exact successful write URL as a typed artifact.
About 20,000 visible characters is the soft planning signal; failed writes
produce a compact Slack fallback and never a fabricated link. The pipeline
accepts a model-created artifact segment only when the harness observed the
same URL in a successful reviewed tool response from that worker attempt.
Existing Wiki pages remain normal references, not produced artifacts: any
provided reference uses the exact human HTTPS URL returned by the reviewed
`url` read operation, and bare namespace/slugs are rejected before rendering.
Full-agent results also receive one final context footer with the resolved
model, effort, provider-reported turn tokens, and elapsed worker time. The
control plane owns this metadata; model output and classifier-only replies do
not carry it.

Admitted full-agent thread jobs use Slack Thinking Steps in collapsed timeline mode.
The control plane starts and owns the stream, emits only allowlisted operational
milestones (for example, reading the Wiki, querying telemetry, or publishing a
reviewed artifact), and finalizes the same message with the typed result.
Model deltas, tool arguments, raw outputs, private context, and chain-of-thought
never enter the timeline. Reaction-only classifier decisions remain reactions.
Slack requires a stream to reply to a user message, so the control plane does
not override a brief-answer-in-channel placement merely to show progress.

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
