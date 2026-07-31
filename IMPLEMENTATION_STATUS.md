# tos-tag implementation status

Date: 2026-07-30
Version: `0.1.0-dev`
Scope: code-complete pre-live system with Slack stubbed

## Current verdict

The pre-live system is code-complete with Slack stubbed. Passive observation,
cross-channel context, conservative decisions, durable jobs, dynamic routing,
headless OpenCode, reviewed marketplace helpers, independent approvals,
scheduled routines, crash-safe delivery reconciliation, and management
mutations are implemented and locally verified. Only live Slack installation
and workspace-specific behavior validation remain.

An independent Claude Fable code review initially returned `NO-GO`. The
confirmed privacy, authorization, kill-switch, cancellation, lease, tenant,
projection-ordering, Slack-rendering, routing, retention, audit, and tool-bundle
integrity findings were corrected and regression-tested. See
[claude-fable-code-review.md](claude-fable-code-review.md).

## Implemented and wired

- Slack Socket Mode and Web API adapters plus deterministic ingress/delivery
  stubs; observations are durable before acknowledgement.
- MongoDB-backed observations and current-message projections with absolute
  source retention, stale-event ordering, edit/delete handling, and 30-day
  default TTL.
- Organization/workspace/channel enrollment, restricted-channel classification,
  participation modes, membership freshness, and effective organization,
  workspace, and channel kill switches.
- Pre-query authorized-channel selection for 100k-capped context packs. A
  restricted cross-channel source contributes only a content-free awareness
  signal and never response evidence.
- Destination-safe context, channel directives, and reviewed notes are passed
  to the response harness in a structured JSON envelope; restricted context is
  omitted.
- Conservative tool-free gating, shadow ambient behavior, response admission,
  per-thread sessions, one-writer generations, finite-attempt jobs, heartbeat
  leases, cancellation/abort, and policy rechecks before model work and Slack
  delivery.
- Dynamic model profiles and deterministic phase/skill, routine, channel,
  workspace, organization, deployment, and explicit-override precedence. Jobs
  store the resolved model snapshot and route trace.
- Fake, external, and disposable local-worker OpenCode harness adapters. The
  worker starts one loopback-only `opencode serve` process with a clean
  home/XDG environment, receives no host secrets, materializes only an explicit
  skill allowlist, and denies all built-in tools except skill loading and the
  project-local capability bridge.
- A project-local `tos_tag_tool` is the sole additional worker capability. It
  calls a loopback-only gateway fenced by job lease, attempt, steering epoch,
  expiry, organization, and an explicit tool-ID allowlist. Tool `SKILL.md`
  instructions are hash-verified and mounted read-only; scripts remain server
  side.
- Durable Mongo approvals show the exact canonical action, require an
  independent approver for non-read risk, expire, and are consumed once.
- Durable Mongo routines reauthorize channel scope at run time, enqueue
  idempotent ordinary jobs, survive restart, and are managed from the UI.
- Behavioral-skill marketplace validation and immutable hashes. Executable tool
  bundles are separate, enforce reviewed script hashes and declared risk/ENV/
  argv/time/output contracts, and resolve secrets only inside the exact helper
  subprocess.
- Revisioned channel directives and reviewed source-linked notes.
- Jobs and deliveries expire no later than their context source boundary and
  are excluded from claims before TTL cleanup.
- Tenant-filtered management queries that omit job input/result and all lease
  tokens; write-only encrypted keystore references never render secret values.
- CAS-serialized per-organization audit chains with idempotent runtime receipts
  for observations, decisions, jobs, deliveries, and fail-closed receipts for
  sensitive management requests.
- Mandatory Slack `mrkdwn` output validation, native tables, safe links, inline
  identifiers, code blocks, and rejection of generated user/channel/group or
  special mentions.
- Multipart Slack delivery checks immutable `tos_tag_delivery` metadata before
  each post, skipping already-accepted parts after a worker crash.
- The embedded UI supports organization/workspace/channel bootstrap, dynamic
  model profiles and rules, directives, notes, keystore values, routines, and
  tool approvals.

## Deliberate implementation boundaries

- `core/conversationalsearch` implements requester/principal/audience
  intersection, but the current response path uses the equivalent authorized
  context-pack projection rather than exposing an on-demand search tool.
- `core/policy` remains the pure deny-wins evaluator used by gateway policy;
  mutable scope policy is persisted by the organization/channel stores.
- On-demand conversational search is not a second model tool: each response job
  receives the equivalent immutable, source-linked, pre-authorized context
  projection. This avoids a second disclosure path during generation.

## Verification evidence from this review

- `gofmt`: no drift.
- `go test ./...`: pass.
- `go test -race ./...`: pass.
- `go vet ./...`: pass.
- `go build -buildvcs=false ./cmd/api ./cmd/admin ./cmd/eval`: pass.
- behavioral eval `cross-channel-behavior/v2`: `10/10`, every metric `1.0`.
- live local Mongo integration: pass, including the new stale-original,
  restricted-projection, TTL, tenant, and concurrent audit-CAS regressions.
- `gosec v2.26.1`: `0` issues, `0` suppressions.
- `govulncheck v1.3.0`: `0` reachable vulnerabilities.
- Docker image `tos-tag:adversarial-review`: build pass.
- real disposable OpenCode route using
  `opencode/deepseek-v4-flash-free`: pass; provider/model reported by SSE.
- real tool-free cross-channel gating decision using
  `opencode/deepseek-v4-flash-free`: pass with source-linked evidence.
- real disposable OpenCode custom-tool call through the reviewed helper bridge:
  pass; reasoning deltas were verified excluded from Slack output.

## Remaining work

No known non-Slack implementation item remains for the declared pre-live scope.
Credential-bearing connector effects remain deliberately disabled until an
operator configures a reviewed tool marketplace, explicit tool IDs, keystore
references, and any required approval.

## Deferred live Slack initiative

No Slack app was installed, no Slack credential was configured, and no live
Slack message was read or sent. Live work still includes app installation,
Socket Mode reconnect and acknowledgement timing, real public/private/Slack
Connect audience semantics, message/edit/delete/thread retries, Block Kit
rendering, shadow precision, and explicit approval before ambient speech.
