# tos-tag implementation checklist

Only mark an item complete when source and verification evidence exist.

## 1. Slack ingestion and scope

- [x] Persist normalized Slack events before acknowledgement.
- [x] Deduplicate concurrent callback shapes.
- [x] Handle messages, edits, deletes, threads, DMs, group DMs, private
  channels, public channels, and authorized Slack Connect context.
- [x] Recheck organization/workspace/channel enrollment and kill switches.
- [x] Enforce the explicit Slack output-channel allowlist.
- [x] Keep broad Slack scopes separate from runtime authorization.

## 2. Privacy and context

- [x] Select authorized channels before querying message content.
- [x] Keep private-channel, DM, and group-DM context destination-local.
- [x] Apply a second disclosure filter after querying.
- [x] Produce immutable source-linked context-pack revisions.
- [x] Cap and partition the default context budget at 100k tokens.
- [x] Exclude restricted and expired sources from response prompts.

## 3. Direct classifier

- [x] Use a direct stateless OpenAI Responses API call.
- [x] Keep the classifier tool-free and independent from full-agent state.
- [x] Enforce a Mongo-authoritative organization/workspace flood budget before
  context construction and classifier calls; fail closed without reactions,
  jobs, or output, and cover live messages plus classifier-gated heartbeats.
- [x] Preserve `TAG__CLASSIFIER__OPENAI_API_KEY` as control-plane-only; it may
  serve stateless classifier and memory-curator calls but never Codex or tools.
- [x] Choose silence, reaction, direct reply, channel/thread placement, model,
  strength, and effort with strict structured output.
- [x] Permit short social acknowledgement/banter without full-agent startup.
- [x] Route active-thread messages through the direct classifier before the
  full-agent fail-safe.
- [x] Test natural messages without evaluator outcome hints.
- [x] Carry trusted source author, channel name, and observation time through
  immutable context and into the classifier/worker envelope.
- [x] Admit conservative public cross-channel factual-alignment interventions;
  suppress opinion, stale, ambiguous, recently represented, and restricted
  conflicts.
- [x] Allow requester-named user mentions and evidence-derived user/channel
  mentions while rejecting model self-authorization, broadcast mentions, and
  user-group mentions.
- [x] Run the original 24 natural classifier cases against the real configured
  OpenAI provider, with expectations held only by the scorer (`26/26`
  including the two infrastructure invariants).
- [x] Re-run the expanded 33-message live provider matrix (`35/35`).
- [x] Re-run the 34-message live provider matrix (`36/36`) after adding the
  standalone product-plan comparison.
- [x] Expand the deterministic matrix to 37 natural messages (`39/39` with
  infrastructure invariants), covering Premium Trial retrieval, source-write
  redirection, and read-only code review.
- [x] Re-run the expanded 37-message live provider matrix (`39/39` including
  infrastructure invariants).
- [x] Add the ambiguous Premium-to-Enterprise plan-transition regression and
  pass the 38-message deterministic and live provider matrices (`40/40` with
  infrastructure invariants), without leaking evaluator intent.
- [x] Enforce assist/proactive initiative independently of the classifier,
  preserve rejected predictions for audit, recheck immediately before job
  admission, keep operator-created heartbeat triggers authorized, and pass the
  46-message deterministic and live provider matrices (`48/48` with
  infrastructure invariants) plus a zero-reply/zero-job `#tos-tag` canary.
- [x] Silently suppress source-write requests across ambient, direct-mention,
  and active-thread paths, and cover the TelemetryCode concept-post false
  positive caused by loose mutation-verb substring matching.
- [x] Require text-confirmed source mutation intent before applying the Linear
  redirect, keep ambient Agent Wiki report links silent, and suppress leading
  third-party handoffs in active Tag threads before classification (now `55/55`
  deterministic matrix with infrastructure invariants).
- [x] Auto-enable one-to-one DMs as hard assist participation surfaces:
  expose the DM fact to the direct classifier, preserve its normal full-agent
  escalation choice, and replace silent or reaction-only outcomes with a
  bounded classifier-direct reply.
- [x] Persist content-free classifier input/output/context/failure dimensions,
  record deterministic avoided calls by reason, and expose organization-scoped
  daily efficiency totals with estimates kept distinct from exact provider
  token usage.
- [x] Fall back conservatively on timeout or malformed output.

## 4. Durable jobs and concurrency

- [x] Persist immutable context/model-route snapshots on admitted jobs.
- [x] Use finite attempts, CAS leases, heartbeats, steering epochs, and expiry.
- [x] Enforce one writer per Slack thread generation.
- [x] Permit configurable concurrency across independent work.
- [x] Keep admission capacity separate from the leased worker-pool size and
  default both to eight concurrent development jobs.
- [x] Abort and revoke on lease, policy, membership, generation, or kill-switch
  change.

## 5. Codex App Server migration

- [x] Remove the former runtime adapter and provider proxy.
- [x] Remove the former CLI dependency and container command.
- [x] Remove old environment variables and validation branches.
- [x] Add `CodexConfig` and documented `TAG__CODEX__*` variables.
- [x] Launch one disposable `codex app-server --stdio` per attempt.
- [x] Implement `initialize` then `initialized` handshake.
- [x] Create an ephemeral thread with resolved model and developer instructions.
- [x] Start a turn with classifier-selected reasoning effort.
- [x] Normalize final `item/completed` and `turn/completed` events.
- [x] Capture `thread/tokenUsage/updated` and render trusted full-agent execution
  metadata as a final context block without decorating classifier-only replies.
- [x] Implement `turn/interrupt` cancellation.
- [x] Bound protocol diagnostics without logging provider bodies.
- [x] Authenticate from private `CODEX_HOME`, separately from classifier key.
- [x] Pin Codex CLI `0.146.0` in the development image.
- [x] Add a real opt-in App Server compatibility test.

## 6. Worker isolation

- [x] Use a clean `HOME`, XDG roots, temp directory, and process environment.
- [x] Materialize one read-only `AGENTS.md` worker policy.
- [x] Materialize skill snapshots at `.agents/skills`.
- [x] Disable shell, file mutation, MCP, plugins, and subagents.
- [x] Use read-only Codex turns with subprocess networking disabled.
- [x] Expose configurable first-party web search and enable unrestricted live
  search explicitly in the local Slack runtime.
- [x] Keep the Aion source tree out of the worker and expose source only through
  a reviewed bounded read capability.
- [x] Keep Slack, Mongo, classifier, and connector credentials out of workers.
- [x] Terminate the process group and delete disposable roots.

## 7. Skills and dynamic tools

- [x] Load the complete `base` plugin from `tag-agent-skills`.
- [x] Reject missing manifests, hash drift, and flat-name collisions.
- [x] Exclude behavioral helper scripts and executable plugin surfaces.
- [x] Register `tos_tag_tool`, typed `tos_tag_wiki`, and `tos_tag_trigger` as
  App Server dynamic tools.
- [x] Reject generic Wiki argv, construct reviewed Wiki arguments in Go,
  persist sanitized validation codes, and collapse self-corrected attempts into
  one Slack progress step.
- [x] Handle `item/tool/call` in the Go control plane.
- [x] Keep attempt capabilities out of the Codex environment and prompt.
- [x] Recheck lease, steering, expiry, tenant, channel, and allowlist on calls.
- [x] Execute only reviewed manifest operations with exact argv/ENV bounds.
- [x] Resolve connector secrets only in the exact helper subprocess.
- [x] Publish source-derived Wiki bodies without a shared worker filename and
  summarize document-sized bodies safely in exact-action approval cards.
- [x] Provide `telemetryos.code` on-demand default-branch freshness, immutable
  snapshots, list/fixed/semantic search/read/version operations, and reject
  arbitrary remotes/branches, traversal, symlinks, environment files,
  credential ledgers, and private tool state.
- [x] Pin Semble and its local embedding model, keep query-time semantic search
  offline, and retain source-bearing indexes only in owner-only server state.
- [x] Enforce the code tool's permanent read-only boundary at both bundle load
  and execution, and silently suppress source mutation intent instead of
  starting a worker or approval flow.
- [x] Provide a credential-free `telemetryos.product-docs` read operation that
  permits only the public docs index/pages and corporate full-content source.
- [x] Provide a read-only `telemetryos.analytics` operation with fixed Gateway
  funnel endpoints, a server-side Site Analytics Token, bounded pagination,
  and direct-identifier/free-form-content filtering.
- [x] Inject the `marketing-funnel-chain`, `marketing-funnel-review`,
  `marketing-account-journey`, and draft-only `marketing-unstall-draft`
  behavioral skills.
- [x] Inject `product-knowledge` so named product claims require retrieval and
  route by source authority and audience.
- [x] Inject `telemetryos-documentation` so customer documentation questions
  discover an exact page through `llms.txt`, read it, and link its indexed URL.
- [x] Reject classifier-marked product delivery unless the same attempt
  successfully completes a full Primer Wiki page, docs page, or corporate
  full-content read.
- [x] Preserve safe classifier admission when invalid evidence IDs are pruned,
  including an unmentioned standalone product-plan comparison.

## 8. Approvals, routines, and directives

- [x] Persist canonical exact-action approvals.
- [x] Render Slack-native approval blocks and update them after decision.
- [x] Default non-read risk to an independent allowlisted approver, with
  source-reviewed per-operation exceptions; bounded bug/feature Linear intake
  and Agent Wiki read/write authoring are trusted without per-action approval,
  generic Linear writes remain gated, recoverable page soft-delete is always
  gated, admin-risk worker operations are invalid, and all calls remain fully
  audited.
- [x] Verify Linear create/update title and description writes through a fresh
  issue read-back, tolerating only line-ending and terminal-newline
  normalization without printing authored content.
- [x] Consume approvals once and resume a fresh fenced attempt.
- [x] Implement `/tag-directive` modal load/save for every authenticated
  workspace user in an enrolled, enabled channel, plus explicit management-UI
  creation, revision history, activation, and audit.
- [x] Implement channel-bound `/tag-status` with an ephemeral native Block Kit
  table for participation, directive, availability, membership, and scope.
- [x] Implement `/tag-automations` as a channel-only ephemeral list with an
  audited, stale-write-aware Slack modal editor; migrate automation indexes and
  backfill legacy channel scope from durable sessions before scheduling.
- [x] Persist and reauthorize standard five-field cron routines with explicit
  IANA timezones while advancing legacy interval records safely.
- [x] Persist classifier-gated cron heartbeat subscriptions and manage them in
  the combined Automation view without exposing workspace/session IDs.

## 9. Slack output and delivery

- [x] Define and prompt `slack-output/v3`.
- [x] Support header, mrkdwn, context, divider, native table, sortable/paginated
  Data Table, presentation-only Card/Carousel, image, and artifact.
- [x] Normalize recoverable model-created table row-width mismatches before
  renderer validation without dropping surplus cell content.
- [x] Degrade unsupported model-created Slack link targets to their visible
  label while preserving renderer validation for surviving HTTP(S) links.
- [x] Keep model Card/Carousel output non-interactive; reserve actions for the
  control plane and Alerts for modal surfaces.
- [x] Keep short/medium answers in Slack and route genuinely document-sized
  expository results through a successful Agent Wiki `artifacts` write before
  returning a concise synopsis and exact tool-returned link.
- [x] Reject model-created artifact segments unless the exact HTTPS URL was
  produced by a successful reviewed tool call in the same worker attempt.
- [x] Prefer actual resolved HTTPS links for provided Wiki references, permit
  unresolved slugs as internal text rather than failing the whole answer, and
  forbid reconstructed or fabricated opaque page URLs.
- [x] Reject model-generated approvals, notices, actions, destinations, and
  special mentions.
- [x] Validate Block Kit before posting.
- [x] Persist, lease, retry, reconcile, and deduplicate deliveries.
- [x] Distinguish channel replies from thread replies according to classifier
  placement.
- [x] Title each newly created one-to-one DM full-agent session once through
  control-plane-owned `assistant.threads.setTitle`, after durable job enqueue,
  with bounded sanitized request text, likely-secret fallback, DM-only
  validation, duplicate suppression, and cosmetic failure handling.
- [x] Use immediate reactions for admitted answer acknowledgement and native
  assistant thread status for every full-agent thread job. Rotate generic
  lifecycle text, replace it with one safe tool-specific status per call,
  refresh long-running work, create no plan-mode stream or task-card pills, and
  finish with an ordinary durable validated reply that clears the status.

## 10. Persistence, audit, and operations

- [x] Use MongoDB for production-authoritative state.
- [x] Enforce tenant filters and omit inputs/results/lease tokens from broad
  management listings.
- [x] Encrypt organization-scoped keystore values.
- [x] Record append-only content-committed audit receipts.
- [x] Retain normalized Slack messages indefinitely, omit their expiry field,
  remove the message TTL index, and migrate the obsolete index at startup.
- [x] Bound new context independently with a configurable 30-day-default
  lookback; keep raw observations, context packs, and derived state on their
  separate TTL or source-validity boundaries.
- [x] Apply TTL and source-linked expiry to derived state.
- [x] Curate changed channel/thread memory asynchronously with
  `gpt-5.6-luna` medium effort and strict stateless structured output.
- [x] Persist source hashes, confidence, source-bound facts, model/effort,
  privacy scope, revision, and source-limited expiry for durable memory.
- [x] Recall public, private, and thread memory through destination-safe query
  and post-query filters; recall only destination-safe incident projections.
- [x] Provide audited management controls to review, correct, pin/unpin, and
  forget memory, erasing content while retaining a relearning tombstone.
- [x] Emit correlated structured logs without message text or secrets.
- [x] Make the management home page an organization-scoped live activity feed
  with bounded SSE replay, explicit public classifier message/result records,
  restricted-content redaction, and payload-free Codex protocol lifecycle.
- [x] Persist workspace, code, skills, Codex login, logs, and Mongo in Compose.
- [x] Keep per-job workers disposable and omit the host Docker socket.
- [x] Override a host `TAG_AION_DEVELOPER_PATH` after loading `runtime.env` so
  the container-reviewed code root remains `/workspace/code`.
- [x] Override host snapshot/index/model/GitHub-config paths with private
  container-owned paths and install the pinned semantic runtime in the image.

## 11. Verification

- [x] Unit-contract JSON-RPC request/response and final-event handling.
- [x] Unit-test clean environments, skill hashes, process teardown, and secret
  sentinel boundaries.
- [x] Real authenticated App Server handshake and model turn.
- [x] Real App Server dynamic-tool schema registration.
- [x] Full `make verify` after final migration sweep.
- [x] Rebuilt development image with pinned Codex CLI.
- [x] End-to-end natural Slack message through classifier, App Server, typed
  renderer, and delivery in `#tos-tag`.
- [x] Live regression of irrelevant-message silence, direct social reply,
  reactions, channel/thread placement, model/effort routing, native tables,
  reviewed source reads, exact-action approval/resume, and independent
  concurrent workers.
- [x] Broad user-authorized context sync with observe-only enrollment,
  independent bot-membership reconciliation, optional joined-channel assist,
  and destination-local private/DM disclosure checks.
- [x] Persist per-conversation Slack bootstrap completion and live watermarks,
  keep first-time history context-only, catch up completed bot-joined channels
  after downtime, requeue only human direct mentions, and proactively pace
  exceptional Web API reads.
- [x] Support operator-selected `session_only` context for noisy test channels:
  skip backfill/catch-up, isolate context to same-channel messages from the
  current process, and suppress durable memory and incident-fact derivation.
- [x] Final exhaustive tracked-tree search confirms no stale runtime surface.
