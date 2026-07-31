# tos-tag implementation checklist

Status: pre-live implementation complete
Owner: implementation agents
Last updated: 2026-07-30

This checklist translates [architecture.md](architecture.md) into verifiable
work. A checked item means its implementation and named local verification are
present. It does not imply that a deferred live integration has been tested.

After the 2026-07-30 Fable code review, checked library-only items are not to be
read as runtime wiring. The pre-live runtime gaps were closed and are recorded
in [IMPLEMENTATION_STATUS.md](IMPLEMENTATION_STATUS.md). Every unchecked item
below depends on a real Slack installation and is deferred to that initiative.

## Initiative boundary

- [x] Treat [architecture.md](architecture.md) as the authoritative design.
- [x] Use Go 1.26 and the standalone service shape proven by
  `telemetryos-agent-wiki`.
- [x] Keep Slack behind project-owned ingress and delivery interfaces.
- [x] Use deterministic Slack stubs and fixtures for this initiative.
- [x] Defer Slack credentials, Socket Mode, Web API calls, app installation,
  channel enrollment, and live workspace testing to a future initiative.
- [x] Keep provider-backed model calls and external connector writes opt-in.
- [x] Keep README implementation claims synchronized as slices become runnable.
- [x] Record every skipped live gate in the final verification report.

## 1. Repository and build foundation

- [x] Create a Go 1.26 module with pinned direct dependencies.
- [x] Add `cmd/api` as the thin service entry point.
- [x] Add `cmd/admin` as a JSON-first operator CLI.
- [x] Add `Makefile` targets for format, test, race, vet, security, eval, build,
  and verify.
- [x] Add `.gitignore` for local configuration, secrets, test artifacts, and
  build output.
- [x] Add `runtime.env.example` with secret-free defaults.
- [x] Add a local `docker-compose.yml` for MongoDB only.
- [x] Add a multi-stage `Dockerfile` without embedded secrets.
- [x] Add `DESIGN.md` linking the implementation to architecture sections.
- [x] Add `SECURITY.md` documenting trust boundaries and reporting posture.
- [x] Add repository-local `AGENTS.md` consistent with `CLAUDE.md`.

## 2. Configuration and lifecycle

- [x] Implement configuration defaults and `TAG__*` environment overrides.
- [x] Validate listener, Mongo, retention, queue, context, classification, auth, and
  stub-mode invariants.
- [x] Reject unauthenticated management on non-loopback listeners.
- [x] Keep configuration/status redaction centralized and tested.
- [x] Construct the complete object graph without network side effects.
- [x] Start Mongo/indexes before dispatchers and HTTP readiness.
- [x] Start Slack ingress last; use the stub in this initiative.
- [x] Stop ingress first, then claims/workers, HTTP, background loops, and DB.
- [x] Bound every shutdown stage and join background goroutines.

## 3. Domain model and persistence

- [x] Define opaque organization, workspace, channel, observation, session,
  generation, job, attempt, delivery, receipt, and revision IDs.
- [x] Keep Mongo persistence models separate from public/boundary DTOs.
- [x] Add organization, workspace, channel, and participation-mode models.
- [x] Add observation and current-message projection models.
- [x] Add organization/channel receive counters and watermarks.
- [x] Add classifier decision models.
- [x] Add context-pack, situation-fact, restricted-signal, and summary models.
- [x] Add session, generation, job, attempt, and steering models.
- [x] Add durable Mongo-backed approval and external-action models, including
  single-use action-byte approvals and audited execution receipts.
- [x] Add model profile, route rule, and route decision models.
- [x] Add channel directive/note revision and active projection models.
- [x] Add marketplace immutable version and snapshot models.
- [x] Add tool manifest, secret reference, and ENV binding models.
- [x] Ensure tenant/scope fields are mandatory in repository methods.
- [x] Ensure all required unique, claim, search, and TTL indexes at startup.

## 4. Retention and deletion

- [x] Set raw Slack-stub envelope retention to 24 hours by default.
- [x] Store every normalized enrolled-channel message with a 30-day default
  absolute `expires_at` anchored to the original message timestamp.
- [x] Ensure edits and retries do not renew retention.
- [x] Query with `expires_at > now` rather than relying only on Mongo TTL lag.
- [x] Ensure derived records never outlive their earliest source.
- [x] Index source-to-derived fan-out records.
- [x] Implement idempotent edit/delete/expiry projection fan-out.
- [x] Retain only policy-approved content-free audit metadata after purge.
- [x] Add a retention janitor and reconciliation metrics/status.

## 5. Stubbed Slack boundary

- [x] Define normalized Slack envelope, message mutation, and delivery DTOs.
- [x] Implement an in-memory deterministic ingress stub.
- [x] Support duplicate, edit, delete, reconnect, and ack fixtures.
- [x] Acknowledge only after durable acceptance or confirmed duplicate.
- [x] Implement an in-memory deterministic delivery stub with retry injection.
- [x] Record returned synthetic message timestamps.
- [x] Prevent model-selected channel/thread destinations.
- [x] Observe self-authored output but suppress it as a trigger.
- [x] Expose stub state through authenticated management/status APIs.

## 6. Slack rich-message contract

- [x] Version and inject the immutable Slack `mrkdwn` output prompt fragment.
- [x] Define ordered `mrkdwn_text`, `table`, and `artifact` result segments.
- [x] Validate Slack-native links, emphasis, inline code, and fenced code.
- [x] Require variables, commands, paths, model IDs, issue keys, UUIDs, and
  other identifiers to use inline backticks.
- [x] Render structured tables as native Block Kit table blocks.
- [x] Enforce 100 rows, 20 cells per row, and 10,000 aggregate cell characters.
- [x] Split oversized results with an accessible text summary.
- [x] Keep model prose inside the validated `mrkdwn` segment boundary; escape
  server-authored link labels, use typed table/artifact fields, and reject all
  generated Slack entities.
- [x] Preserve source ordering across text, tables, and artifacts.
- [x] Reject malformed segments without changing destination.

## 7. Observation and intelligence pipeline

- [x] Idempotently accept observations by `team_id + event_id`.
- [x] Atomically allocate channel and organization receive sequences.
- [x] Resolve scope asynchronously; stale/unknown scope fails closed.
- [x] Maintain current message projections across edits and deletes.
- [x] Append every eligible message to the organization intelligence timeline.
- [x] Project source-linked active incident/status facts.
- [x] Keep restricted projections destination-local and exclude other private
  channels before context queries.
- [x] Track projector watermarks and bounded lag.
- [x] Fair-sample channels so one noisy channel cannot monopolize context.
- [x] Enqueue bounded late high-signal reconsiderations without recursion.

## 8. Context packs and conversational search

- [x] Build immutable context packs for a fixed organization watermark.
- [x] Enforce the 100,000-token hard cap and partition ceilings.
- [x] Use deterministic selection and stable source ordering.
- [x] Preserve source IDs, versions, token counts, disclosure class, and hash.
- [x] Exclude deleted, superseded, expired, or unauthorized sources.
- [x] Keep other private-channel content and derived awareness out of context
  packs and final response evidence.
- [ ] Extend the implemented pre-query enrolled/restricted/membership channel
  filter with live requester and complete destination-audience membership from
  Slack before live deployment.
- [x] Fail stale membership closed to current-channel-only or deny.
- [x] Prevent unauthorized channel names, counts, notes, and snippets leaking.
- [x] Supply admitted OpenCode jobs with the source-linked, capped, immutable
  authorized context projection; retain on-demand search as an optional later
  optimization rather than a second disclosure path.

## 9. Classifier and response admission

- [x] Implement hard trigger and hard suppression rules as pure functions.
- [x] Implement `observe`, `mention`, `assist`, and `proactive` channel modes.
- [x] Implement `silent`, `react`, `reply_in_thread`, `reply_in_channel`,
  `start_background_job`, and `escalate_for_approval` outcomes.
- [x] Keep the direct classifier tool-free and validate strict structured output.
- [x] Default errors, ambiguity, chatter, and low confidence to silence.
- [x] Preserve direct mentions as hard triggers unless policy denies them.
- [x] Make thread reply the default and channel reply a higher threshold.
- [x] Implement shadow mode before live ambient speech.
- [x] Require an explicit global `live` classifier mode before `assist` or
  `proactive` channel decisions can produce ambient output.
- [x] Enforce kill switch, cooldown, response budget, and concurrency limits.
- [x] Validate selected evidence IDs and destination disclosure at admission.
- [x] Use one observation-level output guard across decision revisions.
- [x] Record explainable reason codes without hidden chain-of-thought.

## 10. Sessions, jobs, and delivery

- [x] Derive one session from team/channel/root-thread and separate generations.
- [x] Enforce one writer per session generation.
- [x] Implement typed job states and validated transitions.
- [x] Claim using lease owner/token/expiry/version and finite attempts.
- [x] Fence heartbeat, completion, tool, and delivery operations.
- [x] Distinguish retryable pre-effect loss from reconciliation-required loss.
- [x] Implement queue, cancel, interrupt, restart, and branch semantics.
- [x] Persist results before creating final delivery.
- [x] Retry delivery without rerunning jobs.
- [x] Mark completed-but-undelivered outcomes explicitly.
- [x] Implement deterministic echo work for the stubbed first vertical slice.

## 11. Dynamic model routing and fake inference

- [x] Define named profiles independent of agent principal and access.
- [x] Implement override, phase/skill, routine, channel, workspace,
  organization, and deployment precedence.
- [x] Apply provider, data, capability, context, and availability constraints
  before preferences.
- [x] Implement explicit validated fallback chains.
- [x] Snapshot routing policy for a job while rechecking live hard denies.
- [x] Record effective profile, provider, model, variant, and reason.
- [x] Provide deterministic fake classification and response model adapters.
- [x] Add route-preview API/UI/CLI behavior.
- [x] Keep real provider tests opt-in and disabled by default.

## 12. OpenCode and worker boundary

- [x] Define the project-owned Harness interface and normalized event types.
- [x] Implement a deterministic fake Harness for normal tests and evals.
- [x] Implement the pinned OpenCode HTTP/SSE adapter behind explicit opt-in.
- [x] Cover health, session, prompt, model selection, events, permission,
  abort, and malformed responses with a fake HTTP server.
- [x] Keep OpenCode local state non-authoritative.
- [x] Define WorkerManager and a safe fake worker.
- [x] Implement local disposable worker provisioning without host secrets.
- [x] Keep real OpenCode and sandbox compatibility smoke tests opt-in.
- [x] Verify cancellation terminates process groups and revokes capabilities.
- [x] Wire the `tos_tag_tool` OpenCode custom tool to the job-scoped server-side
  gateway while denying every other built-in capability by default.

## 13. Policy, approvals, gateways, and tools

- [x] Implement pure deny-wins policy evaluation over structured inputs.
- [x] Ensure ambient observation is never an authorized write requester.
- [x] Define task capabilities derived only from server-side state.
- [x] Fence capabilities by attempt lease, steering epoch, and expiry.
- [x] Define immutable approval bytes and canonical argument hashing.
- [x] Replace the in-memory-only approval contract with durable Mongo state and
  wire independent, expiring, single-use approval into the action gateway.
- [x] Validate tool ID, version, operation, destination, and risk.
- [x] Execute exact argv rather than model-supplied shell strings.
- [x] Inject only declared ENV into the exact tool subprocess.
- [x] Prevent the worker from receiving provider, Slack, Mongo, or tool secrets.
- [x] Bound subprocess time, output, files, and environment.
- [x] Keep all real external tool execution disabled by default.

## 14. Marketplaces, notes, directives, and routines

- [x] Parse behavioral marketplace catalogs without executing content.
- [x] Validate `SKILL.md`, references, paths, symlinks, sizes, and hashes.
- [x] Disable hooks and executable OpenCode plugins by default.
- [x] Resolve deterministic immutable skill snapshots and collisions.
- [x] Materialize only authorized skills into a read-only worker view.
- [x] Automatically select and inject the complete `telemetryos-automation`
  and `base` behavioral plugins from separate configured repositories, while
  allowing an empty base plugin and failing on missing plugins or skill-name
  collisions.
- [x] Parse and validate executable tool bundles separately.
- [x] Keep tool executable bytes outside the OpenCode filesystem contract.
- [x] Resolve the configured tool-ID allowlist into immutable tool skills and
  pass only that subset to each worker and its fenced gateway scope.
- [x] Implement revisioned channel directives with activation/rollback.
- [x] Implement source-linked channel notes with pending human review.
- [x] Keep notes as delimited data, never instruction authority.
- [x] Replace the in-memory-only routine scheduler with Mongo state and wire its
  restart-safe idempotent loop and management surface into `core.Core`.
- [x] Prevent routine and authored-message loops.

## 15. Audit, usage, and management surfaces

- [x] Canonically encode redacted receipt metadata.
- [x] Append a per-organization CAS-serialized hash chain.
- [x] Use retention-epoch HMAC commitments instead of public content hashes.
- [x] Verify in-memory chains and detect tampering.
- [x] Track model/tool/worker/delivery usage without sensitive labels.
- [x] Serve liveness, version, readiness, and redacted status routes.
- [x] Serve authenticated JSON management endpoints.
- [x] Serve an embedded server-rendered overview and stub management page.
- [x] Serve dedicated channels, observations,
  decisions, context, jobs, routes, marketplaces, notes, directives, retention,
  and audit pages.
- [x] Add CSRF protection to every mutation.
- [x] Keep SSE best-effort with durable refetch as the source of truth.
- [x] Never render stored secret values.

## 16. Tests and evaluation suites

- [x] Unit-test IDs, keys, state machines, leases, policy, routing, retention,
  rendering, context selection, and audit canonicalization.
- [x] Integration-test Mongo indexes, dedupe, counters, claims, fencing, TTL
  predicates, projection fan-out, and concurrent CAS.
- [x] Contract-test Slack stub ingest/ack/delivery/retry behavior.
- [x] Contract-test Slack rich-message prompt injection and rendering.
- [x] Contract-test fake OpenCode HTTP/SSE behavior.
- [x] Adversarial-test cross-channel disclosure and stale membership.
- [x] Adversarial-test forged identity, destination, snapshot, and credential
  fields.
- [x] Adversarial-test prompt attempts to turn reads into writes.
- [x] Adversarial-test marketplace traversal, symlink, hook, and executable
  content.
- [x] Prove secrets are absent from worker ENV, argv, output, logs, receipts,
  fixtures, and artifacts.
- [x] Build deterministic behavioral eval fixtures for chatter silence,
  mentions, questions, incidents, restricted evidence, and reply mode.
- [x] Build alert-to-support cross-channel and late-alert evals.
- [x] Score speak precision, silence recall, evidence grounding, disclosure,
  reply placement, dedupe, and context-cap compliance.
- [x] Make eval thresholds fail the build on regressions.
- [x] Keep live Slack/provider/OpenCode/tool evals separately opt-in.

## 17. Verification and handoff

- [x] Run `gofmt` and verify no formatting drift.
- [x] Run `go test ./...`.
- [x] Run `go test -race ./...`.
- [x] Run `go vet ./...`.
- [x] Run `gosec` when available.
- [x] Run `govulncheck` when available.
- [x] Run deterministic evals and publish their score artifact.
- [x] Build both commands and the container image where available.
- [x] Verify a clean restart against stubbed Slack and persistent Mongo.
- [x] Update README from design-only status to the exact implemented scope.
- [x] Produce a final gap list mapped to unchecked checklist items and correct
  earlier code-complete overclaims after adversarial review.
- [x] Explicitly state that no live Slack installation or message was used.

## Active live Slack validation initiative

These remain opt-in live gates and must be checked only after direct evidence:

- [x] Create/review the Slack app manifest; the development installation is
  intentionally broad while runtime authorization remains default-deny.
- [x] Configure Slack App-Level and Bot User OAuth tokens in the gitignored,
  owner-readable local `runtime.env` fallback and validate the bot credential
  with `auth.test` without logging secret values.
- [x] Install into the dedicated development workspace and start the local
  control plane in Socket Mode with shadow classification.
- [x] Verify Socket Mode connect and reconnect hello events against the expected
  Slack App ID without logging credentials.
- [x] Add correlated redacted lifecycle logging for Socket Mode, normalization,
  acknowledgements, persistence, decisions, jobs, deliveries, and management
  actions, with an owner-readable JSONL local test artifact.
- [x] Validate a real public-channel human message through Events API,
  normalization, durable non-duplicate observation persistence, 45 ms
  acknowledgement, and the expected `observe`-mode silent decision.
- [x] Deterministically exercise reconnect generation plus the durable retry
  contract: failed persistence is not acknowledged; a retried duplicate is
  durably recognized and acknowledged.
- [ ] Observe a natural Slack-requested Socket Mode refresh in the long-running
  development connection; Slack normally rotates it only every few hours.
- [x] Validate public-channel bot membership and allowlisted audience policy.
- [ ] Validate private-channel and Slack Connect membership/audience semantics.
- [x] Validate live message, edit, delete, mention, thread, job, and threaded
  delivery behavior in the enrolled public development channel.
- [x] Validate native Block Kit table API acceptance, accessible fallback text,
  and duplicate-delivery reconciliation against the development workspace.
- [x] Inspect the native Block Kit table in Slack desktop: headers, rows,
  download, and copy controls are exposed accessibly; verify the mention thread
  contains exactly one bot reply.
- [ ] Inspect native Block Kit accessibility in Slack mobile and web clients.
- [x] Run a controlled four-message observe-only shadow sample in the real test
  channel: all four expected labels matched, effective decisions remained
  silent, and job/delivery counts did not change.
- [ ] Run a longer shadow-mode precision evaluation on organic channel traffic.
- [x] Approve and exercise a bounded transition to live `mention` mode for the
  enrolled test channel, then return it to `observe`; `assist` remains disabled.
