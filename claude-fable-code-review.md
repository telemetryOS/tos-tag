# Claude Fable adversarial code review

Date: 2026-07-30
Reviewer: Claude Fable, high effort, read-only source review
Initial verdict: `NO-GO`
Post-remediation status: pre-live gates pass; live Slack validation remains

## Review method

The installed Claude CLI was run against the repository with read-only
`Read`, `Grep`, and `Glob` access. Shell execution, edits, writes, and network
tools were disabled. The reviewer was asked to prioritize cross-channel
disclosure, authorization order, kill switches, leases, cancellation, delivery,
tenant isolation, retention, audit integrity, marketplace/tool safety, model
routing, and contradictions between code and documentation.

## Critical findings and corrections

### Private-channel disclosure classification was not derived from live policy

The live Slack normalizer cannot know a channel's restricted policy and left
`SlackEnvelope.Restricted` false. Context building trusted that stored flag, so
the restricted cross-channel redaction branch was dead for live-shaped events.

Correction:

- the decision worker resolves the channel policy before intelligence
  projection;
- it persists the resolved restricted class on both the observation and current
  message projection;
- context building independently derives restriction from the current channel
  policy map; and
- a live-shaped regression proves a policy-restricted source becomes only
  `active_incident: true` and cannot ground final prose.

### Cross-channel authorization happened after the database query

`buildContextPack` queried every observed organization channel and filtered
later. This allowed unenrolled, killed, stale, or otherwise unauthorized channel
identifiers and content to enter the pack-building process.

Correction:

- organization/workspace/channel state is resolved before `Recent`;
- only enrolled, fresh, non-killed channel IDs are sent to MongoDB;
- unknown organization/workspace scope fails closed; and
- a regression records the exact pre-query channel set and proves an
  unenrolled channel is never queried.

## High-severity findings and corrections

| Finding | Disposition |
| --- | --- |
| Organization kill switch was written but never read | `Resolve` and `ListChannels` now combine organization, workspace, and channel kill state. |
| Queued delivery ignored a newly activated kill switch | Delivery re-resolves enrollment, membership freshness, and kill state immediately before `Send`; denied records become abandoned/completed-undelivered. |
| Running model calls had no lease heartbeat or cancellation/policy abort | Harness execution now heartbeats at one-third lease duration, polls live job and scope state, calls `Harness.Abort`, and finalizes cancellation/release paths. |
| Expired model lease could lose output and leak admission reservations | Terminal failure, invalid render, cancellation, exhausted retry, and lost-lease paths now release reservations; long model work heartbeats. |
| `observe` mode still answered direct mentions | `observe` is now an absolute no-output check before hard mention/thread triggers, with regression coverage. |
| Slack renderer only rejected broadcast mentions | It now rejects generated user, channel, user-group, and all special entity mentions while continuing to validate safe HTTP(S) Slack links. |
| A redelivered original event could resurrect a deleted message | Projection ordering now stores Slack event time and mutation rank; stale originals cannot overwrite later edits/deletes in memory or MongoDB. |
| Runtime never appended audit receipts | The core now appends idempotent observation, decision, job, and delivery receipts. Sensitive management mutations append a receipt before proceeding. |
| Audit CAS cleanup could delete a receipt another writer had adopted | The unsafe compensating delete was removed; valid orphans are adopted only by the CAS recovery path. |
| Management job/delivery/decision lists crossed tenants and exposed lease tokens | Repositories now query by organization before decoding; job input/result and every lease/token field are omitted from public DTOs. |

## Additional corrections

- Channel defaults no longer masquerade as explicit model overrides. Phase and
  skill rules outrank them, compound rules must match every declared scope, and
  routing uses `ContextPackRevision.TotalTokens` rather than source count.
- Shared intelligence projection occurs only after scope authorization.
- Jobs and deliveries have absolute source-bounded expiry, TTL indexes, and
  claim-time expiry predicates.
- Executable tool manifests reject unknown risk strings. Tool scripts and
  marketplace bundles have immutable hashes, and execution rechecks the script
  bytes immediately before use.
- The full behavioral marketplace is no longer mounted automatically. Only
  `marketplaces.injectedSkills` enters disposable workers; an empty allowlist
  injects none.
- At the initial review checkpoint, disposable OpenCode workers received a
  generated policy that denied all built-in tools except behavioral skill
  loading. The post-review completion work below adds the sole project-local,
  server-side capability bridge for executable marketplace operations.
- The response model now receives structured, destination-safe context rather
  than only the triggering message. Restricted awareness sources and the
  internal gating prompt are omitted.
- Organization and workspace bootstrap mutation APIs were added so live scope
  can be configured without direct database edits; each mutation is audited.

## Post-review completion work

The review also exposed packages that were documented as product features
before they were wired. Those gaps were subsequently closed:

- OpenCode now receives one project-local `tos_tag_tool` backed by a
  loopback-only, job/attempt/lease/epoch/expiry/tool-allowlist capability;
- reviewed tool `SKILL.md` files are hash-verified and injected read-only while
  executable scripts and credentials remain server side;
- non-read operations use durable, independent, expiring, single-use approvals
  bound to canonical action bytes and visible in management;
- routines use Mongo state, execution-time scope authorization, idempotent jobs,
  a supervised scheduler, and management controls; and
- Slack delivery reconciles immutable per-part metadata before retrying after
  the accept-before-Mongo-completion crash window.

The on-demand conversational searcher remains intentionally out of the model
tool set because response jobs already receive its bounded, source-linked,
pre-authorized immutable projection. The local worker is process/environment
isolated, not a network namespace or hostile multi-tenant sandbox.

## Verification after remediation

- `go test ./...`: pass
- `go test -race ./...`: pass
- `go vet ./...`: pass
- command builds: pass
- deterministic behavior eval: `10/10`
- live local Mongo integration: pass
- `gosec v2.26.1`: `0` issues, `0` suppressions
- `govulncheck v1.3.0`: `0` reachable vulnerabilities
- real anonymous OpenCode provider/model route: pass
- real OpenCode custom-tool-to-reviewed-helper route: pass
- Docker image build: pass

No live Slack workspace, Slack credential, provider credential, or external
connector effect was used during this review.

## Independent verification-pass status

A second Claude Fable invocation was started after remediation, but the client
produced no output and did not return a verdict within the bounded review
window. It was terminated rather than treating an unavailable reviewer as an
approval. The initial `NO-GO` review and the locally reproduced regression
tests remain the review of record.
