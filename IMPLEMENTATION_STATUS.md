# tos-tag implementation status

Date: 2026-08-02
Version: `0.1.0-dev`
Scope: development Slack control plane, membership-managed assist with live
regression traffic constrained to `#tos-tag`

## Current verdict

The direct classifier, privacy-filtered context, durable jobs, Codex App Server
worker, typed Slack output, Slack-native Thinking Steps, reviewed tools, Slack-native approval/resume,
channel directives, source-linked durable memory, routines, triggers, logging,
audit, and persistent container workspace are implemented.

The full-agent runtime is now exclusively Codex App Server. The previous agent
runtime, its adapter, provider proxy, dependency, config variables, container
commands, tests, and active documentation have been removed.

## Classifier boundary

- The classifier still calls the OpenAI Responses API directly.
- It remains stateless and tool-free.
- A durable, atomic organization/workspace flood bucket runs before context or
  provider calls. The default is 1,000 eligible classifications per one-hour
  fixed window; exhaustion or bucket-store failure produces no reaction, agent
  work, or Slack output.
- Its key is still `TAG__CLASSIFIER__OPENAI_API_KEY` and is not exposed to
  Codex App Server.
- It chooses silence, reaction, direct social reply, placement, model profile,
  strength, and effort from natural Slack context.
- Active-thread messages reach this same direct classifier before any
  full-agent fallback; they are not automatically promoted to agent work.
- Short direct social replies bypass full-agent startup; substantive work is
  admitted to a durable job.

## Codex App Server boundary

- Config is `TAG__CODEX__ENABLED`, `TAG__CODEX__COMMAND`,
  `TAG__CODEX__HOME`, `TAG__CODEX__WORKER_ROOT`, and
  `TAG__CODEX__TIMEOUT`.
- One disposable `codex app-server --stdio` process is launched per job attempt.
- The client performs `initialize` / `initialized`, creates an ephemeral
  thread, and starts a turn with the resolved model and effort.
- Final agent messages are normalized from authoritative `item/completed`
  events; completion and cancellation use `turn/completed` and
  `turn/interrupt`.
- Skills are hash-verified and materialized read-only under `.agents/skills`.
- Shell, direct source mounts, file mutation, MCP, plugins, and subagents are
  disabled for Slack jobs. The turn is read-only and subprocess networking is
  disabled; first-party Codex web search is separately configurable and `live`
  in the local Slack runtime.
- The only model-visible tools are job-scoped `tos_tag_tool`, typed
  `tos_tag_wiki`, and `tos_tag_trigger` dynamic functions.
- Agent Wiki requests are semantic page operations. Go validates their fields,
  constructs reviewed CLI argv, persists only closed validation codes, and
  collapses corrected validation retries into one Slack progress step.
- Dynamic tool requests are executed by the Go client through the existing
  lease/steering/expiry-fenced gateway. The capability is not placed in the
  Codex process environment.
- Codex login/state comes from a private writable `CODEX_HOME`, separately from
  the direct classifier credential.

## Other implemented surfaces

- Socket Mode ingress with durable-before-ack observation and duplicate
  suppression.
- Message/edit/delete/thread handling and user-authorized context bootstrap,
  with durable per-conversation completion/live watermarks, context-only initial
  history, post-watermark recovery of missed direct messages in bot-joined
  channels, and proactive per-method Slack pacing.
- Destination-local privacy for private channels, DMs, and group DMs.
- Public cross-channel context for classifier decisions when authorized.
- Asynchronous Luna memory curation for changed channel/thread scopes, with
  source hashes, confidence, source-bound facts, source-limited expiry, and
  content-free usage accounting. The development effort is `medium`.
- Privacy-filtered memory recall into context packs, including public
  cross-channel recall, destination-local restricted recall, root-thread-local
  thread recall, and independently recalled public incident facts.
- Agent memory management UI/API for correction, pin/unpin, and content-erasing
  forget operations. Human corrections become pinned operator memory; model
  summaries remain explicitly derived context.
- Conservative ambient team-alignment decisions from recent destination-safe
  public facts, with trusted author/channel/time metadata and privacy-safe
  attribution.
- Mention, observe, assist, proactive, kill-switch, independently reconciled
  bot membership, membership freshness, and
  output-channel policy.
- Configurable concurrent leased jobs with per-thread one-writer generations.
- Native Block Kit Tables, sortable/paginated Data Tables, presentation-only
  Cards and Carousels, expanded AI sections, and typed message segments.
- Control-plane-owned full-agent context footer with resolved model, effort,
  provider-reported turn tokens, elapsed worker time, and compact successfully
  used capability categories; classifier-only
  replies remain unadorned.
- Durable delivery reconciliation and special-mention rejection.
- Slack Thinking Steps for admitted full-agent thread jobs, with durable stream
  timestamps, one rotating current-action card for validated skills and every
  native or reviewed tool call, same-message finalization, and ordinary-delivery
  fallback. Intentional reaction-only/direct classifier outcomes remain outside
  the stream path.
- Slack-native exact-action approval and fresh-worker resume.
- `/tag-directive` Slack modal available to every authenticated workspace user
  for an enrolled, enabled channel, plus management-UI creation, revisioned
  Mongo persistence, and audit.
- Standard five-field cron routines and classifier-gated heartbeat trigger
  subscriptions with explicit IANA timezones, legacy interval compatibility,
  and a combined management Automation view/editor.
- Complete behavioral `base` plugin from `tag-agent-skills`, including
  read-only code, Linear, Wiki, OTel, and `team-alignment` worker behavior.
- Reviewed Linear, Wiki, OTel, DLA, optional Mongo, and bounded source-code
  helper bundles with encrypted environment bindings where required.
- Wiki inline-body publication for source-derived documents, with the complete
  body committed in the audit receipt. Wiki capability is page CRUD only:
  read/write authoring is trusted without per-action approval, recoverable page
  soft-delete always requires approval, and namespace/admin/general-destructive
  surfaces are unavailable.
- `telemetryos.code` provides only bounded list/search/read operations below a
  server-owned Aion root and rejects traversal, symlinks, runtime environment
  files, credential ledgers, and private tool state. Bundle load and execution
  independently enforce that every code operation is read-only; mutation
  requests are redirected to Linear bug/feature intake with no approval path.
- Classifier-marked product answers are rejected unless the same worker attempt
  successfully reads a full Primer Wiki page, public docs page, or corporate
  full-content source. Search/index/web/Slack context and model memory are not
  sufficient retrieval evidence.
- The injected `telemetryos-documentation` skill reads the live public
  `llms.txt` index for discovery, then fetches an exact indexed guide or API
  reference page and supplies its human documentation link.
- Correlated redacted file logging, usage records, audit chains, TTL cleanup,
  and an activity-first management UI. Its organization-scoped SSE feed pairs
  bounded public Slack excerpts with classifier outcomes and shows payload-free
  Codex protocol lifecycle; restricted content remains hidden.
- Persistent Compose workspace/home/Mongo with disposable per-job roots.
- Graduated response delivery: short/medium answers remain Slack-native, while
  genuinely document-sized expository work is published to Agent Wiki
  `artifacts` by the strong Sol-medium worker and linked only from a successful write;
  failed publication falls back to a compact Slack answer without a guessed
  link. Artifact segments are rejected unless the URL has successful
  same-attempt reviewed-tool provenance. References to existing Wiki pages use
  exact human HTTPS URLs returned by the reviewed `get` or `url` read operation;
  every reviewed `get` includes that URL in its full page envelope, and bare
  Wiki slugs are rejected before rendering.

## Verification evidence

Current migration evidence:

- full verification components: pass, including all Go packages, the race
  detector, vet, security scans, and the expanded `49/49` deterministic
  behavioral eval;
- latest opt-in direct OpenAI classifier baseline: `48/48`, with `38` real provider calls
  and approximately `1.84s` mean case
  latency; it predates the ambient Wiki report-link regression, whose original
  provider decision and deterministic correction are covered locally. Natural
  Slack text contained no evaluator outcome, placement, reaction, model,
  effort, or method hints;
- live `#tos-tag` assist-initiative canary: the direct provider recommended
  the then-current strong/max background route for an unmentioned declarative synthetic incident,
  while the runtime recorded effective `silent` with
  `policy.unsolicited_assist_work`; Slack had zero replies and Mongo had zero
  jobs for the observation;
- `gosec`: `0` issues across `81` files and `19,907` lines;
- `govulncheck`: no called vulnerabilities (one vulnerable required module is
  not reached by the program);
- real installed Codex CLI `0.146.0` App Server smoke: pass;
- real authenticated live-web App Server smoke: pass in `6.7s`, with a native
  web-search event against an external IANA page;
- authenticated `gpt-5.6-luna` turn at low effort: pass;
- experimental dynamic-tool registration: pass;
- structured Slack output and authoritative final-event normalization: pass;
- latest direct App Server smoke latency: approximately `4.0s`;
- rebuilt development image: pass; `codex-cli 0.146.0` and
  `codex app-server --help` both verified inside the image;
- live `#tos-tag` end-to-end migration matrix: pass. Natural-message cases
  covered irrelevant-message silence, short direct in-channel social replies,
  stable non-urgent metric reaction-only behavior, mixed social/work requests,
  destination-safe private-context refusal, thread placement for deeper work,
  classifier-selected model/effort, the full configured reaction set, native
  tables, approval/resume, and three independent
  concurrent Codex workers without output outside the allowed channel;
- latest seven-message adversarial wave: pass. Seven natural messages were sent
  concurrently; direct social completed in about `5s`, light/low channel work in
  `12-14s`, the standard/medium native table in `19s`, and the then-current strong/max live
  OTel investigation in `197s`. Redacted logs confirmed zero selected evidence
  for the private-context refusal and typed `table` output for the comparison;
- latest reviewed source-access case: pass. The classifier completed in
  approximately `4.3s`, selected `gpt-5.6-luna` at medium effort, a thread, and
  `hammer_and_wrench`; the job made `13` paired read-only
  `telemetryos.code` calls and delivered a native four-column, three-row table
  in approximately `86s` end to end;
- Premium Trial regression root cause: the classifier correctly selected
  authoritative product retrieval, but the first worker completed with zero
  tool calls and improvised a weak answer; a user prompt then caused a second
  worker to retrieve the Primer pricing page. The new delivery gate makes that
  zero-retrieval result non-deliverable.
- live active-thread social reply: pass. The direct classifier handled
  `Appreciate the clear matrix, Tag!` in `4.00s`, selected a
  `white_check_mark` reaction and direct `You're welcome!` thread reply, and no
  Codex job was enqueued;
- live Slack Thinking Steps finalization: pass. A natural product follow-up in
  `#tos-tag` opened a timeline in `480ms`, showed safe Agent Wiki milestones,
  and finalized the validated Block Kit answer in the same Slack message
  (`1785706647.008769`) without a fallback post. The classifier took `2.61s`;
  the full answer completed in approximately `32s`. Brief in-channel work stays
  direct because Slack requires `thread_ts` for streamed agent messages;
- live Tag-authored Wiki publication: pass. A max-effort Codex worker read the
  current tos-tag architecture through reviewed source access, published an
  18,017-byte `artifacts` page through the no-prompt Wiki write policy, and
  posted the revision-1 Wiki link and summary in the originating Slack thread;
- previous user-authorized context sync: `378` conversations discovered, `527`
  bounded messages imported, and `1` inaccessible conversation skipped without
  failing the sync. The current policy additionally reconciles bot membership:
  joined public/private channels derive assist, other conversations stay
  observe-only, and private/DM context remains destination-local;
- 2026-08-03 offline direct-message recovery: pass. With the prior `#tos-tag`
  watermark at 03:25 UTC, startup recovered the human `@tag` question posted at
  15:49 UTC while the runtime was unavailable, classified it, ran one max-effort
  worker, and durably delivered the answer in the original thread. A subsequent
  startup recovered zero messages, confirming the watermark/idempotency guard;
- restart-wide context replay has been removed: first-time bootstrap stays
  resolved context, completed bot-joined channels scan only after their durable
  watermark, missed human mentions re-enter the decision queue, and all ambient
  catch-up remains context-only;
- 2026-08-03 self-authored ingress proof: pass. A live classifier-only reply was
  echoed by Slack, stored as `authorized` / `resolved` destination-local context,
  acknowledged without decision admission, and produced no classifier activity
  record or provider call;
- 2026-08-03 live acknowledgement and rendering matrix: pass after one fix.
  Natural `#tos-tag` probes covered classifier-only social reply, irrelevant
  silence, source-write redirection, light/low in-channel work, standard/medium
  product retrieval, native table delivery, destination-local privacy refusal,
  reaction-only acknowledgement, and the then-current strong/max operational routing. The
  observed emoji set was `white_check_mark`, `speech_balloon`, `thinking_face`,
  `warning`, `rotating_light`, and `eyes`. Threaded work opened Thinking Steps
  roughly one second after classification. A malformed product-comparison table first
  failed with `table_row_shape`; model-boundary row normalization was added,
  and the natural retry completed as `mrkdwn_text + table + mrkdwn_text` with
  two reviewed product reads and same-message stream finalization. A subsequent
  read-only source review exposed a disposable local-path link as
  `mrkdwn_unsafe_link`; unsupported model-created link targets now degrade to
  their visible label while valid HTTP(S) links retain normal validation;
- 2026-08-03 implementation-plan follow-up retry: pass. The first natural
  follow-up was incorrectly admitted as a light/low channel reply. The new
  natural eval and policy correction route implementation and migration plans
  to standard/medium threaded work. The live retry received a classifier emoji,
  opened Thinking Steps in `559ms`, showed two read-only source milestones, and
  finalized the same threaded message in `1m 4s` with durable delivery. The
  complete direct provider matrix passed `48/48` at `1.84s` mean classifier
  latency;
- 2026-08-03 `#tos-tag` context hygiene: the channel policy is now
  `session_only`. Historical import and offline catch-up are skipped; context is
  restricted to same-channel events observed since process startup; durable
  memory and incident facts are neither recalled nor newly derived. The exact
  `TEST-*` purge removed 12 observations, 10 message projections, 8 incident
  facts, 1 durable summary, 10 derivation links, and 338 context-pack revisions;
  all four retrieval-text checks returned zero afterward;
- tracked source/config dependency search for the removed runtime: clean.

The host runtime is live on the migrated binary. A fresh persistent container
home intentionally has no copied host credential; an operator must authenticate
it once with `make container-codex-login` before using containerized full-agent
jobs. The Compose volume then retains that login without exposing it to the
classifier or application environment.
