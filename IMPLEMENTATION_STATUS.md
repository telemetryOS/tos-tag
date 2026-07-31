# tos-tag implementation status

Date: 2026-07-31
Version: `0.1.0-dev`
Scope: code-complete pre-live system plus bounded development Slack validation

## Current verdict

The pre-live system is code-complete and the bounded development Slack path has
now been exercised. Passive observation,
cross-channel context, conservative decisions, durable jobs, dynamic routing,
headless OpenCode, reviewed marketplace helpers, independent approvals,
scheduled routines, crash-safe delivery reconciliation, and management
mutations are implemented and locally verified. Public-channel Socket Mode,
message/edit/delete/thread ingestion, mention-only reply delivery, native Block
Kit posting, and delivery reconciliation are also live-verified. Private and
Slack Connect audience semantics, natural Slack-requested refresh timing,
mobile/web accessibility, and longer organic shadow precision evaluation
remain.

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
- Pre-query authorized-channel selection for 100k-capped context packs. Private
  messages are destination-local; every other private channel is excluded
  before context selection, including content-free derived awareness.
- Destination-safe context, channel directives, and reviewed notes are passed
  to the response harness in a structured JSON envelope; restricted context is
  omitted.
- Conservative direct tool-free classification, shadow ambient behavior,
  response admission, per-thread sessions, one-writer generations,
  finite-attempt jobs, heartbeat
  leases, cancellation/abort, and policy rechecks before model work and Slack
  delivery.
- Dynamic model profiles and deterministic phase/skill, routine, channel,
  workspace, organization, deployment, and explicit-override precedence. Jobs
  store the resolved model snapshot and route trace.
- The deployment default resolves to profile `chatgpt-luna-max`, provider
  `openai`, model `gpt-5.6-luna`, and variant `max`; OpenCode/provider execution
  remains separately disabled until explicitly enabled and authenticated.
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
- Response workers automatically combine every validated behavioral skill from
  `telemetryos-automation` in sibling `telemetryos-agent-skills` with every
  skill from the initially empty `base` plugin in sibling
  `tag-agent-skills`. Exact plugin selection, flat-name collision checks,
  read-only materialization, and script exclusion fail closed.
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
- `gosec v2.28.0`: `0` issues, `0` suppressions.
- `govulncheck v1.6.0`: `0` reachable vulnerabilities.
- Docker image `tos-tag:adversarial-review`: build pass.
- real disposable OpenCode route using
  `opencode/deepseek-v4-flash-free`: pass; provider/model reported by SSE.
- direct OpenAI Responses API classifier contract: pass against a deterministic
  HTTP transport with strict schema, no tools, source-linked evidence,
  reaction selection, and live-profile-constrained model/effort selection.
- credential-bearing live OpenAI classifier call: pending local key insertion.
- real disposable OpenCode custom-tool call through the reviewed helper bridge:
  pass; reasoning deltas were verified excluded from Slack output.

## Remaining work

No known non-Slack implementation item remains for the declared pre-live scope.
Credential-bearing connector effects remain deliberately disabled until an
operator configures a reviewed tool marketplace, explicit tool IDs, keystore
references, and any required approval.

## Active live Slack validation initiative

The development app is installed in the Telemetry workspace with credentials in
the owner-readable, gitignored local runtime fallback. `auth.test` verified the
bot installation, Socket Mode hello verified the expected app ID after multiple
local restarts, and public `#tos-tag` membership was verified and enrolled in
`observe` mode. The first two human test messages exposed a real
callback-pointer normalization defect. After the fix and logged restart, a
fresh public-channel message traversed Events API, normalized, persisted as a
non-duplicate observation, and was acknowledged in 45 ms. Its durable
classification decision was `silent` with `admission.channel_mode`, as required
by `observe`.

Live runs now emit correlated structured metadata for Socket Mode connection,
event normalization, acknowledgement, observation persistence, classification,
jobs, deliveries, and management actions. The owner-readable JSONL file is retained
under the workspace `.testruns` directory; tokens, raw envelopes, and message
text are excluded. Remaining live work includes natural Slack-requested refresh
timing, real private/Slack Connect audience semantics, mobile/web table
accessibility, and longer organic shadow precision before any ambient speech.

The project-owned Compose MongoDB is now the live-test authority on
`127.0.0.1:27018`; earlier Homebrew Mongo data on `27017` remains untouched as
historical evidence. A clean stop/start preserved policy and produced a fresh
verified Socket Mode hello. A controlled bot-authored lifecycle then persisted
real message, edit, threaded-reply, and delete events. A human direct mention
arrived through Slack's app-mention and channel-message subscriptions; one
event won admission, the concurrent copy was suppressed, one fake-model job
succeeded, and exactly one threaded Slack delivery completed.

The first live native table attempt exposed an SDK serialization defect: a
typed block round-trip reintroduced an invalid empty optional alignment. The
delivery adapter now preserves the renderer's validated block JSON exactly,
with a regression test. Slack accepted the retried native table, and a second
send with the same delivery ID reconciled to the original message timestamp
without posting again. The channel was returned to `observe` immediately after
the mention test.

Slack desktop exposed the native table as an accessible table with both column
headers, both data rows, and download/copy controls. The mention thread showed
exactly one bot reply. The live ingress now exposes the Socket Mode client's
connection generation while relying on `slack-go` for automatic refresh and
reconnect. A deterministic reconnect/retry regression proves that a failed
persistence attempt is not acknowledged and that Slack's retried duplicate is
durably recognized before acknowledgement; a natural Slack-requested refresh
still requires the long-running connection to rotate.

An `observe` channel can now record assist-style predictions when global shadow
mode is enabled while enforcing `admission.channel_mode` as the effective
silent decision. A controlled four-message live sample matched all four
expected labels (two silent and two would-reply-in-thread), produced no Slack
output, and left the retained job and delivery counts unchanged. A read-only
Slack inventory found no private or Slack Connect channel in which the bot is a
member, so those audience tests remain blocked on a dedicated test-channel
invitation rather than broadening enrollment or access automatically.
