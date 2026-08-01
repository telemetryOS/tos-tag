# tos-tag implementation design

The authoritative architecture is [architecture.md](architecture.md). This
file is the concise map from the implemented source tree and operational assets
to that architecture.

| Source area | Architecture responsibility |
| --- | --- |
| `cmd/api`, `core/core.go` | composition root and ordered lifecycle |
| `core/config`, `core/database`, `core/server` | configuration, MongoDB, readiness, HTTP |
| `core/slack`, `core/observer`, `core/intelligence` | stub/live Socket Mode transports, observations, context discovery/backfill, organization timeline |
| `core/contextpacks`, `core/classifier` | immutable bounded context and tool-free decision admission |
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

Behavioral skills are loaded from the complete sibling
`telemetryos-automation` and `base` plugins, hash-verified, and copied read-only
into disposable workers. Their scripts do not grant executable authority.
The base plugin includes `team-alignment`, which turns classifier-admitted
public factual conflicts into neutral, attributed Slack reconciliation while
preserving destination-local private context.
Executable access is limited to `tos_tag_tool` operations in the reviewed tool
catalog; Aion source is not mounted and can be inspected only through bounded
`telemetryos.code` reads. `tos_tag_trigger` separately manages
classifier-gated channel heartbeat subscriptions.

Approval defaults to risk-based at the operation manifest. A reviewed bundle
may declare `approval: never`; the current exception is Agent Wiki page
read/write authoring. The recoverable Wiki page soft-delete always requires
exact-action approval. Namespace, asset, publish-file, cascading move, generic
undo, and admin Wiki operations are unavailable, and admin-risk worker-tool
manifests are rejected globally. Every
execution remains job-scoped, hash-pinned, bounded, kill-switchable, and
audited regardless of approval policy.

Slack delivery is graduated rather than character-gated. Short and medium
answers use typed Block Kit directly. Document-shaped long-form synthesis uses
the strong/max worker to publish Markdown under Agent Wiki `artifacts`, then
returns a synopsis and the exact successful write URL as a typed artifact.
About 20,000 visible characters is the soft planning signal; failed writes
produce a compact Slack fallback and never a fabricated link. The pipeline
accepts a model-created artifact segment only when the harness observed the
same URL in a successful reviewed tool response from that worker attempt.

Checked-in settings stay fail-closed. The approved local development deployment
observes user-authorized conversations, defaults discoveries to `observe`, and
allows reactions/messages only in the `#tos-tag` assist channel.
