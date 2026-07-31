# CLAUDE.md

This file guides coding agents working in `tos-tag`.

## Current project state

tos-tag is a runnable Go control plane with a tested, code-complete pre-live
Slack system, including marketplace tools, approvals, routines, and management.
Live Slack is deliberately disabled by default and has not been validated.
Consult [IMPLEMENTATION_STATUS.md](IMPLEMENTATION_STATUS.md) and do not imply
that a live workspace or credential-bearing connector effect was tested unless
the current initiative produced that evidence. The opt-in OpenCode tests have
verified an anonymous `opencode/deepseek-v4-flash-free` provider route,
model-based gating, and a real model-initiated call through the reviewed tool
bridge.

Before implementation work, read these documents in order:

1. [architecture.md](architecture.md) — authoritative implementation
   architecture and invariants.
2. [research.md](research.md) — product research, source evidence, evaluated
   alternatives, and rationale.
3. [IMPLEMENTATION_STATUS.md](IMPLEMENTATION_STATUS.md) — verified gates and
   the exact live-integration boundary.
4. [README.md](README.md) — concise project orientation and operation.

If the documents conflict, `architecture.md` governs implementation. Update it
when a deliberate architecture decision changes. Keep `README.md` accurate for
users and contributors; keep research claims and implementation decisions
clearly separated.

## Mission

Build a standalone Go 1.26 Slack agent control plane that:

- observes every eligible message in every Slack channel where its bot is a
  member;
- treats channels as policy/context/memory scopes and threads as sessions;
- evaluates every eligible human message against bounded organization-wide
  context and decides conservatively whether to stay silent or act;
- runs admitted agent work through isolated, headless OpenCode workers;
- dynamically routes models by channel, routine, skill, and inference phase;
- materializes approved immutable skills from behavioral marketplaces;
- resolves reviewed executable helpers from a separate tool marketplace;
- shares authorized knowledge through conversational search while preserving
  thread-session and private-channel isolation;
- supports revisioned per-channel notes and prompt directives;
- keeps tool credentials outside OpenCode, with declared tool secrets injected
  only into the exact trusted subprocess, while credentialed model providers
  use an explicitly configured external OpenCode/provider boundary; and
- provides durable jobs, approvals, receipts, audit, usage, and a management UI.

## Non-negotiable architecture invariants

### Observe, decide, act

Every eligible Slack event is durably accepted as an observation before it is
acknowledged. An observation is not automatically a job.

```text
Slack event -> observation -> decision -> optional delivery/job -> authorized action
```

Keep ingestion, ambient decision, job execution, and Slack delivery as separate
durable boundaries.

### Channel scope, thread session

Never implement one unbounded model session per channel.

- `team_id + channel_id` identifies the channel observer and policy scope.
- `team_id + channel_id + root_thread_ts` identifies the thread session.
- A generation identifies a restart/branch boundary.
- One writer at a time owns a thread generation.
- Different threads may run concurrently.

The raw workspace stream must not become one permanent model session or be
copied wholesale into every prompt. Chat-gating builds an immutable, source-
linked organization `ContextPackRevision`, initially capped at 100k input
tokens, from the target thread/channel, fair-sampled recent organization
timeline, related evidence, situation facts, and rolling summaries. Every
eligible retained message remains a retrieval candidate even when it does not
fit one pack.

### Ambient behavior

Ambient response classification is tool-free and cannot send output directly.
It returns validated structured data and passes through admission policy.

Valid outcomes are:

```text
silent | react | reply_in_thread | reply_in_channel | start_background_job | escalate_for_approval
```

The gate is tool-free, returns structured action/evidence selection, and never
user-facing prose. It may use releasable cross-channel evidence and content-free
restricted signals. The admitted response job receives releasable evidence
only. Thread reply is the quiet default; channel reply requires higher
confidence, broad relevance, and explicit policy.

Silence is the default on errors, ambiguity, social chatter, repetition, and low
confidence. Direct mentions and active-thread replies are hard triggers unless
policy denies them.

Implement shadow mode before live `assist` or `proactive` modes. Always preserve
an immediate operator kill switch, cooldowns, confidence thresholds, and
response budgets.

Do not store or request hidden chain-of-thought. Persist structured reason codes,
confidence, rules/classifier version, context source IDs, resulting job/delivery,
and a receipt.

### Durable control plane, disposable worker

MongoDB and external systems are authoritative. OpenCode local state, worker
filesystems, process memory, and SSE events are disposable execution state.

One active thread generation receives one disposable local worker containing a
loopback-only OpenCode server and a clean home/XDG tree. The implemented local
boundary is process/environment isolation, not a network namespace or hostile
multi-tenant sandbox. A worker crash or idle teardown must be recoverable from
tos-tag-owned state.

OpenCode owns the prompt/model/tool loop, coding tools, provider adapters, and
context compaction. tos-tag owns Slack, tenancy, identity, policy, model routing,
tool credentials, jobs, memory, plugins, routines, approvals, audit, budgets,
and worker lifecycle. Credentialed model auth belongs to the explicitly
configured external OpenCode/provider boundary; tos-tag does not copy those
credentials into a disposable local worker.

### Stable identity, dynamic model

Keep these concepts separate in types, stores, routes, and UI:

```text
AgentPrincipal
ModelProfile
InstructionProfile
SkillSnapshot
ToolSnapshot
SecretBinding
AccessBundle
```

Changing a model must not change agent identity, memory scope, ownership,
permissions, or receipts.

### Policy outside the model

A model, prompt, repository, Slack message, tool result, or skill may request an
action. None can authorize it.

Evaluate policy using stable structured inputs: principal, requester/routine,
scope, operation, destination, method, path, repository, credential reference,
data class, model profile, cost, and current membership. Explicit denies and
hard constraints win.

Ambient observation is not an authorized requester for write operations.

### Secrets outside OpenCode and general workers

Never place raw Slack, repository, GitHub, or connector credentials—or provider
credentials managed outside the configured provider boundary—in:

- the OpenCode or general worker environment;
- prompts or model context;
- repository files or global Git configuration;
- OpenCode/MCP configuration visible inside the worker;
- tool arguments or results;
- logs, traces, receipts, artifacts, fixtures, or screenshots.

OpenCode receives only short-lived, task-scoped model/tool capabilities. After
live policy passes, the tool runner may inject only manifest-declared ENV values
into the exact pinned, trusted tool subprocess for one call. Launch it outside
OpenCode with a minimal environment, exact argv rather than `bash -c`, isolated
process visibility, constrained egress, private temporary storage, output/time
caps, redaction, and teardown. Never expose that environment to arbitrary shell
commands or the worker's process namespace.

The untrusted worker never supplies authoritative principal, requester, policy,
snapshot, destination, or credential identity. Gateways derive those fields
from the task capability and live server state. Every model/tool call verifies
the attempt's current lease/fencing token, steering epoch, cancellation state,
and kill switch.

### Default-deny execution and network

OpenCode permissions are defense in depth, not a sandbox. The local pre-live
worker enforces a clean filesystem/environment, default-deny OpenCode tools,
loopback control, process-group cancellation, bounded wall time, and controlled
artifact export. Deployments that admit hostile multi-tenant content or
credentialed effects must additionally provide non-root execution, resource
limits, and network/egress isolation outside this process worker.

Never mount the host Docker socket or inherit the control-plane environment.

### Behavioral and executable marketplaces are distinct

Only the control plane may sync and install marketplaces. Pin immutable
revisions and calculate content hashes.

- Treat behavioral `SKILL.md` content as untrusted instructions. Validate
  referenced files and materialize only the resolved `SkillSnapshot`.
- Keep worker skill directories read-only.
- Never execute helper scripts from the behavioral marketplace. Migrate or
  register them as reviewed tool bundles.
- Require every executable tool bundle to contain model-facing `SKILL.md`, an
  enforced manifest, an exact helper entrypoint, and contract tests.
- Validate tool operation/argument schemas, ENV declarations, destinations,
  dependencies, limits, paths, symlinks, hashes, risk, and exit/output contracts.
- Bind scopes only to exact immutable content hashes. A changed mutable upstream
  ref creates a degraded marketplace event and requires explicit promotion.
- Never execute marketplace tests in the control-plane namespace; run them only
  in CI or a disposable no-secret, default-deny sandbox.
- Resolve and snapshot skill-to-tool dependencies at job admission; fail closed
  on missing, conflicting, unapproved, or unbound requirements.
- Disable hooks until a reviewed adapter exists.
- Disable executable OpenCode JavaScript/TypeScript plugins by default.
- Treat Codex, Claude, Cursor, and marketplace manifests as catalog metadata,
  not executable OpenCode configuration.
- Never let a worker update its own marketplace or install dependencies from
  the network.

### Receipts and idempotency

Use stable idempotency keys for observations, ambient decisions, jobs, routine
runs, Slack deliveries, approvals, and external actions. Do not claim exactly-once
behavior where an external target cannot provide or reconcile it.

Persist the durable result before final Slack delivery. A Slack delivery retry
must not rerun model/tool work.

Security-relevant transitions append canonical redacted receipt metadata through
compare-and-swap on the organization audit-chain head. Required receipt failure
fails the transition closed. Content commitments use organization/epoch-keyed
HMAC rather than brute-forceable public hashes. Receipts must not become a hidden
copy of deleted or secret content.

## Planned repository structure

Follow the package layout in
[architecture.md](architecture.md#111-repository-layout). Important boundaries:

- `cmd/api` is a thin service entry point.
- `cmd/admin` is the JSON-first `tos-tagctl` entry point.
- `core.New` constructs and validates without network side effects.
- `core.Start` and `core.Stop` own ordered lifecycle.
- `core/slack` handles transport, not response policy.
- `core/deliveries` owns the immutable Slack-output prompt fragment, typed
  `mrkdwn_text`/`table`/`artifact` result schema, rendering, and retryable
  delivery records.
- `core/observer` owns observations, cursors, and ambient decisions.
- `core/jobs` owns durable job transitions and leases.
- `core/opencode` is the HTTP/SSE harness adapter.
- `core/modelrouter` is deterministic and provider-policy aware.
- `core/modelgateway` and `core/toolgateway` are credential boundaries.
- `core/toolrunner` launches pinned helpers outside OpenCode; `core/keystore`
  resolves write-only secret ENV bindings.
- `core/workers` abstracts Docker or stronger isolation.
- `core/skillmarketplaces`, `plugins`, and `skills` own behavioral catalogs and
  materialization; `core/toolmarketplaces` and `tools` own executable catalogs,
  manifests, dependency resolution, and snapshots.
- `core/conversationsearch`, `notes`, and `directives` own shared retrieval and
  channel knowledge without merging live sessions.
- `core/audit` owns receipts and integrity; `core/usage` owns accounting.
- `models/` contains MongoDB persistence documents only.
- `types/` contains public and external boundary DTOs only.
- `routes/` contains small route packages and no domain persistence logic.

Do not leak BSON IDs, lease fields, or persistence concerns into Slack,
OpenCode, gateway, or public HTTP DTOs.

## TelemetryOS service conventions

Pattern the service after `telemetryos-agent-wiki`:

- Go 1.26 and one module;
- `orale.Load("tag")` with defaults, config files, then `TAG__*` environment
  variables/flags;
- `blackbox` structured logging;
- `go-shared/tel` OpenTelemetry;
- `go-shared/buildmeta` and `go-shared/dotroutes`;
- `navaros` over plain `net/http`;
- MongoDB driver v1 with `otelmongo`;
- indexes ensured during startup;
- server-rendered `html/template` views with `go:embed` assets;
- one root router, resource route packages, and shared dependencies;
- domain sentinel errors translated at transport boundaries; and
- coordinated bounded shutdown.

Do not add NATS, Zephyr, Gateway registration, Redis/Valkey, Temporal,
Kubernetes, a separate SPA, or another fleet dependency without measured need
and an explicit architecture decision.

## Go implementation rules

- Prefer small, project-owned interfaces defined by their consumers.
- Constructors validate and wire; `Start` performs I/O.
- Pass `context.Context` through every blocking or external operation.
- Bound all queues, outputs, retries, timeouts, process counts, and payloads.
- Use typed enums and validated transition functions for durable state machines.
- Claims and transitions match ID, expected state/version, and lease token.
- A stale lease holder must not complete, publish, call a model, or invoke a
  tool. Bound retries with `max_attempts`; possible post-side-effect lease loss
  enters reconciliation rather than automatic retry.
- Wrap errors with operation context without including secrets or raw payloads.
- Keep policies pure and table-testable.
- Keep provider-specific behavior inside model catalog/gateway/OpenCode adapters.
- Keep Slack-specific rendering outside domain and orchestration packages.
- Prefer standard library behavior unless an existing TelemetryOS dependency is
  the established convention.
- Do not create abstractions solely for hypothetical future flexibility.

## Persistence rules

MongoDB is authoritative for current projections, coordination, and append-only
receipt history.

Every durable record includes `organization_id` and applicable workspace,
channel, thread, generation, job, and attempt scope. Every query includes the
tenant/scope filter.

Use unique indexes and compare-and-swap rather than assuming single-process
execution. At minimum preserve the indexes described in
[architecture.md](architecture.md#108-critical-indexes).

Conversational search and organization context selection must compute the
authorized channel set before issuing a
MongoDB query. Intersect agent-principal scope, explicit requester or routine
owner Slack visibility, complete destination-audience visibility, organization
sharing/quote-out policy, active bot search authority, and any narrower job
restriction. Ambient gating without an explicit requester may use content safe
for the complete destination audience plus separately labeled restricted
signals; the response job receives releasable sources only. Stale Slack
membership fails closed. Do not leak unauthorized channel names, notes, counts,
or snippets.

Persist organization observation watermarks and immutable context-pack source/
token/disclosure metadata. Context selection must be deterministic for a fixed
revision, respect the exact configured token cap, prevent noisy-channel
monopolization, and retain provenance for edit/delete invalidation. High-signal
events may reconsider a bounded window of unanswered targets, but the output
guard and trigger/target key prevent duplicate replies.

Channel notes and directives are separate revisioned documents. Notes are
source-linked, explicitly delimited reference data and never instruction
authority. Agent-authored note revisions remain `pending_review` and absent from
prompt/search context until human activation. The active channel directive is
snapshotted into new jobs after immutable safety and organization/agent
instructions; it cannot override policy. Activation is an access-expanding
confirmed/audited action. An admitted generation retains its snapshot until
restart or branching.

Store every normalized message from an enrolled Slack channel in MongoDB. Use a
30-day default rolling TTL for normalized observations and `channel_messages`,
anchored to the original event/message time; an edit, reaction, retry, or
reindex must not renew retention. Raw encrypted Slack envelopes and materialized
prompt payloads default to 24 hours. Set an absolute `expires_at` and use a TTL
index with `expireAfterSeconds: 0`, but never rely on MongoDB's asynchronous TTL
worker for correctness: every read/search/context query must also require
`expires_at > now`.

Separate retention still applies to transcripts, human-approved notes/curated
memory, model/tool payloads, artifacts, and redacted audit metadata. A derived
search document, context segment, summary, signal, transcript excerpt, or cache
must never expire later than its earliest source. Message edits update current
projections; deletion or expiry fans out immediately through the derivation
index to search, transcript, memory, note, artifact, and generated-response
copies. Receipts may retain only the content-free fact and keyed commitment
metadata permitted by policy.

## Slack rules

- Use Socket Mode for the internal experiment.
- Manage WebSocket refresh/reconnect through `slack-go/slack`.
- Acknowledge only after durable insert or confirmed duplicate.
- Dedupe by `team_id + event_id`.
- Do not assume affinity or ordering across Socket Mode connections.
- Subscribe initially to public/private channel messages and direct mentions
  with only the required history/app-mention scopes.
- Treat message edits, deletions, hidden subtypes, thread broadcasts, other bot
  output, and channel lifecycle events explicitly.
- Ignore tos-tag's own Slack output as a trigger while retaining the observation
  where policy allows.
- Send output through durable delivery records; do not post directly from the
  model event callback.
- Derive output channel/thread from the admitted job. Never accept an arbitrary
  model-selected Slack destination.
- Inject the immutable Slack `mrkdwn` output contract into every Slack-destined
  agent generation, including progress, approval, correction, routine, and
  final-result messages. Channel directives may change voice but cannot disable
  this output contract. Keep the contract in a project-owned, versioned prompt
  fragment and test its presence; do not rely on a marketplace skill or model
  to remember it.
- Require Slack-native links: `<https://example.com|descriptive label>`, never
  GitHub Markdown `[label](url)`.
- Use `*bold*`, `_italic_`, and `~strikethrough~` where they improve scanning;
  do not emit double-asterisk bold and assume Slack will translate it.
- Enclose variables, ENV names, literal values, commands, flags, paths, model
  names, codes, issue keys, UUIDs, job IDs, and identifiers in single backticks.
- Use triple-backtick code blocks for multiline code, commands, logs, JSON, or
  other literal output.
- When a table is appropriate, return a complete structured table for the Slack
  renderer to emit as a native Block Kit `table` block. Use an aligned fenced
  table only for literal terminal-style output or as a renderer fallback; never
  emit an unaligned pipe table or HTML table into a `mrkdwn` text object.
- Normalize results as ordered `mrkdwn_text`, `table`, and `artifact` segments.
  A table must be a typed segment with rows and column settings, not table text
  hidden inside a prose segment.
- Enforce native table limits of 100 rows, 20 cells per row, and 10,000
  aggregate cell characters. Split larger results or attach them as an artifact
  with a readable summary and link.
- Keep narrative prose outside code blocks and use short paragraphs, headings,
  lists, links, and emphasis appropriately.
- Validate and Slack-escape untrusted `mrkdwn` in the renderer without damaging
  intentional links, mentions, code spans, or code blocks. Enforce Slack and
  Block Kit size limits, splitting output or attaching a file when necessary.

DMs, MPIMs, files, reactions, canvases, Slack Connect, guest channels, and
workspace search are separate feature scopes. Do not silently enable them.

## Model routing rules

Use named profiles. Validate them against the live/pinned catalog. Do not assume
variant names such as `medium` or `xhigh` are universal.

Route preference precedence:

1. authorized one-job/step override;
2. exact skill or inference/tool-adjacent phase;
3. routine/event subscription;
4. channel;
5. workspace;
6. organization; and
7. deployment fallback.

Data policy, provider allow/deny, capabilities, context size, credentials,
budget, and live safety constraints outrank preference rules.

Snapshot the routing-policy revision at job admission. Recheck live revocations,
provider disablement, and hard budgets on every call. Fallback only through an
approved chain and record every effective route.

## OpenCode rules

- Use a pinned, compatibility-tested OpenCode version.
- Integrate from Go through project-owned HTTP/OpenAPI and SSE types; do not add
  a TypeScript sidecar solely for the SDK.
- Run one OpenCode server per isolated worker on a private control-plane-only
  network/interface with no published host or public port.
- Select the resolved model and generated agent/profile on each prompt.
- Normalize SSE events before domain persistence or Slack rendering.
- Treat permission events as requests to tos-tag policy.
- Do not use OpenCode local storage as the only copy of transcripts, jobs,
  memory, results, or audit.
- Expose conversational search and marketplace helpers through compact
  tos-tag-owned tool contracts. OpenCode never receives MongoDB credentials,
  keystore values, or arbitrary executable paths.
- Materialize only the admitted behavioral skills. Do not mount a whole
  marketplace and rely on hidden names as an authorization boundary.
- Abort and terminate process groups on cancellation and timeout.
- Keep real provider-backed tests explicit and opt-in.

ACP may be added only after it satisfies the same model routing, permission,
cancellation, event correlation, isolation, and recovery contracts.

## Management web interface

Use server-rendered templates and embedded assets. Add browser JavaScript only
for progressive enhancement such as live SSE refresh.

- Durable HTTP/JSON state is authoritative; SSE is best effort.
- Every mutation requires auth, CSRF, live authorization, and audit.
- Confirm destructive or access-expanding actions.
- Never display stored secrets.
- Show shadow and live ambient decisions with structured reasons.
- Show the organization situation board, intelligence watermark, context-pack
  token partitions/sources/disclosure, gating reply mode, and reconsideration
  history without exposing unauthorized content.
- Provide model route, effective channel directive, `SkillSnapshot`, and
  `ToolSnapshot` previews before publishing policy.
- Provide revision history, preview, activation, and rollback for channel
  directives; source/provenance and revision history for channel notes.
- Keep tool definitions in the executable marketplace. The UI may manage
  installs, promotion, scope bindings, and manifest-declared write-only secret
  ENV bindings, but it must not be a tool-schema or shell-script editor.
- Search testing must display the effective authorized channel set without
  revealing channels the operator/requester cannot see.
- Keep management commands on authenticated HTTP handlers, not SSE.

## Required testing

For each change, run the narrowest relevant test first, then the repository
verification chain when available:

```text
go test ./...
go test -race ./...
go vet ./...
gosec
govulncheck
```

Add tests for the invariant affected by the change. Important suites include:

- event duplicate/reorder/edit/delete/reconnect behavior;
- ambient hard rules, shadow/live modes, budgets, and silence-on-error;
- deterministic 100k context-pack budgeting, organization watermarks,
  noisy-channel fairness, situation/summary invalidation, and prompt-cache-
  independent correctness;
- alert-to-support correlation, reply-mode admission, restricted-signal versus
  releasable-evidence separation, and late-event reconsideration dedupe;
- channel/thread/session derivation and single-writer concurrency;
- Mongo leases, stale writers, retries, and idempotency;
- private-channel isolation in every retrieval path;
- conversational-search requester and destination-audience authorization before
  query, stale-membership fallback, result caps, source links, message
  edits/deletes, and restricted-signal fallback;
- channel-note pending-review/sharing/provenance and directive
  precedence/confirmation/snapshot behavior;
- model route precedence, constraints, fallback, and provider options;
- OpenCode/general-worker no-secret/default-deny behavior, live lease/steering
  fencing, forged-identity rejection, and proof that only
  the exact tool child receives its declared ENV values;
- tool gateway allow/ask/deny, SSRF, mandatory-proxy/no-direct-route egress,
  redirect denial, exact-argument approval, destination, and idempotency;
- behavioral marketplace traversal/symlink/script/hook/executable denial and
  materialization;
- executable marketplace manifest/hash/dependency/ENV/argv/egress/output/exit
  validation, promotion, rollback, and teardown;
- approval expiry and single use;
- Slack delivery retry without job replay;
- Slack `mrkdwn` contract injection and rendering for links, emphasis, inline
  identifiers, fenced code, native Block Kit tables, safe escaping, message
  splitting, and attachment/code-table fallback;
- audit canonicalization, chain verification, and tamper detection; and
- management auth, CSRF, role checks, and secret non-rendering.

Live Slack, model-provider, sandbox, and connector tests must report exact scope,
credentials source by reference only, cleanup, and anything not verified. Never
fabricate live validation.

## Implementation and change sequence

Follow the phases in [architecture.md](architecture.md#17-incremental-delivery-plan).
Do not skip directly to write tools, coding workflows, or proactive ambient
behavior.

The wired local implementation slice is:

1. service/config/logging/telemetry/Mongo lifecycle;
2. health/version/status and authenticated management shell;
3. Slack Socket Mode durable observation ingest;
4. message projections, dedupe, cursors, edits, and deletes;
5. atomic receive sequencing, asynchronous scope resolution, bounded sequence
   gaps, and observation output guards;
6. organization observation timeline, situation projector, immutable 100k
   context packs, and bounded retroactive-correlation queue;
7. revisioned/confirmed channel directives and human-reviewed source-linked
   channel notes;
8. deterministic/fake cross-channel shadow gating, durable admission, and the
   durable kill switch;
9. finite-attempt durable jobs for admitted responses;
10. CAS-serialized Slack delivery records and audit receipts; and
11. restart/replay/concurrency tests.

The OpenCode response and tool-free gating boundaries, behavioral/tool
marketplace allowlists, job-scoped tool gateway, encrypted keystore, durable
approvals and routines, authorized context packs, notes/directives, usage, and
management mutation sections are implemented. Consult
[IMPLEMENTATION_STATUS.md](IMPLEMENTATION_STATUS.md) for current verification.
Do not enable ambient speech before the live shadow evaluation and operator
approval gates pass.

## Documentation discipline

- Keep `README.md` honest about what is implemented and runnable.
- Update `architecture.md` for deliberate boundary, state, security, or
  dependency changes.
- Preserve source/evidence distinctions in `research.md`.
- Record significant settled changes as ADR rows or dedicated ADR files.
- Document skipped checks and unresolved risks.
- Avoid claims of Claude Tag compatibility or endorsement.

## Stop conditions

Stop and request direction before:

- expanding Slack scopes or enabling DMs/workspace-wide search;
- introducing a new infrastructure dependency or frontend toolchain;
- changing the channel/thread session boundary;
- placing a long-lived credential in a worker;
- enabling unrestricted egress;
- executing a helper from the behavioral marketplace, or enabling an
  unreviewed/unpinned tool bundle, hook, or executable plugin;
- exposing a tool secret to OpenCode, a general worker environment, arbitrary
  shell, prompt, result, log, or UI;
- broadening conversational search beyond the authorized pre-query channel
  intersection;
- allowing ambient messages to authorize writes;
- weakening private-channel isolation, approval, receipt, or audit requirements;
- making a destructive migration or deleting retained data; or
- selecting a public project name or license without user direction.

## Completion standard

Before reporting implementation work complete:

1. Identify the exact files and architectural slice changed.
2. Confirm no unrelated user work was overwritten.
3. Run and report the relevant tests and verification gates.
4. Distinguish source/build verification from installed/configured/live checks.
5. Confirm security and scope invariants affected by the change.
6. Update documentation and architecture decisions when behavior changed.
7. Report blockers, skipped live tests, cleanup, and remaining risk plainly.
