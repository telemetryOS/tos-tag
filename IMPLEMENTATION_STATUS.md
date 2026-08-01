# tos-tag implementation status

Date: 2026-08-01
Version: `0.1.0-dev`
Scope: development Slack control plane, output constrained to `#tos-tag`

## Current verdict

The direct classifier, privacy-filtered context, durable jobs, Codex App Server
worker, typed Slack output, reviewed tools, Slack-native approval/resume,
channel directives, routines, triggers, logging, audit, and persistent container
workspace are implemented.

The full-agent runtime is now exclusively Codex App Server. The previous agent
runtime, its adapter, provider proxy, dependency, config variables, container
commands, tests, and active documentation have been removed.

## Classifier boundary

- The classifier still calls the OpenAI Responses API directly.
- It remains stateless and tool-free.
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
- Shell, direct source mounts, file mutation, web, MCP, plugins, and subagents
  are disabled for Slack jobs. The turn is read-only and network-disabled.
- The only model-visible tools are job-scoped `tos_tag_tool` and
  `tos_tag_trigger` dynamic functions.
- Dynamic tool requests are executed by the Go client through the existing
  lease/steering/expiry-fenced gateway. The capability is not placed in the
  Codex process environment.
- Codex login/state comes from a private writable `CODEX_HOME`, separately from
  the direct classifier credential.

## Other implemented surfaces

- Socket Mode ingress with durable-before-ack observation and duplicate
  suppression.
- Message/edit/delete/thread handling and user-authorized context backfill.
- Destination-local privacy for private channels, DMs, and group DMs.
- Public cross-channel context for classifier decisions when authorized.
- Conservative ambient team-alignment decisions from recent destination-safe
  public facts, with trusted author/channel/time metadata and privacy-safe
  attribution.
- Mention, observe, assist, proactive, kill-switch, membership freshness, and
  output-channel policy.
- Configurable concurrent leased jobs with per-thread one-writer generations.
- Native Block Kit tables and typed message segments.
- Durable delivery reconciliation and special-mention rejection.
- Slack-native exact-action approval and fresh-worker resume.
- `/tag-directive` Slack modal with revisioned Mongo persistence and audit.
- Scheduled routines and classifier-gated heartbeat trigger subscriptions.
- Complete behavioral plugins from `telemetryos-agent-skills` and
  `tag-agent-skills`, including the `team-alignment` worker behavior.
- Reviewed Linear, Wiki, OTel, DLA, optional Mongo, and bounded source-code
  helper bundles with encrypted environment bindings where required.
- Wiki inline-body publication for source-derived documents, with the complete
  body committed in the audit receipt. Wiki capability is page CRUD only:
  read/write authoring is trusted without per-action approval, recoverable page
  soft-delete always requires approval, and namespace/admin/general-destructive
  surfaces are unavailable.
- `telemetryos.code` provides only bounded list/search/read operations below a
  server-owned Aion root and rejects traversal, symlinks, runtime environment
  files, credential ledgers, and private tool state.
- Correlated redacted file logging, usage records, audit chains, TTL cleanup,
  and management endpoints.
- Persistent Compose workspace/home/Mongo with disposable per-job roots.
- Graduated response delivery: short/medium answers remain Slack-native, while
  genuinely document-sized expository work is published to Agent Wiki
  `artifacts` by a strong/max worker and linked only from a successful write;
  failed publication falls back to a compact Slack answer without a guessed
  link. Artifact segments are rejected unless the URL has successful
  same-attempt reviewed-tool provenance.

## Verification evidence

Current migration evidence:

- full `make verify`: pass, including all Go packages, the race detector, vet,
  and `35/35` deterministic behavioral evals;
- opt-in direct OpenAI classifier eval: `35/35`, with `25` real provider calls,
  eight pre-provider hard suppressions, and approximately `1.26s` mean case
  latency; natural Slack text contained no evaluator outcome, placement,
  reaction, model, effort, or method hints;
- `gosec`: `0` issues across `81` files and `19,460` lines;
- `govulncheck`: no called vulnerabilities (one vulnerable required module is
  not reached by the program);
- real installed Codex CLI `0.146.0` App Server smoke: pass;
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
  `12-14s`, the standard/medium native table in `19s`, and the strong/max live
  OTel investigation in `197s`. Redacted logs confirmed zero selected evidence
  for the private-context refusal and typed `table` output for the comparison;
- latest reviewed source-access case: pass. The classifier completed in
  approximately `4.3s`, selected `gpt-5.6-luna` at medium effort, a thread, and
  `hammer_and_wrench`; the job made `13` paired read-only
  `telemetryos.code` calls and delivered a native four-column, three-row table
  in approximately `86s` end to end;
- live active-thread social reply: pass. The direct classifier handled
  `Appreciate the clear matrix, Tag!` in `4.00s`, selected a
  `white_check_mark` reaction and direct `You're welcome!` thread reply, and no
  Codex job was enqueued;
- live Tag-authored Wiki publication: pass. A max-effort Codex worker read the
  current tos-tag architecture through reviewed source access, published an
  18,017-byte `artifacts` page through the no-prompt Wiki write policy, and
  posted the revision-1 Wiki link and summary in the originating Slack thread;
- latest user-authorized context sync: `378` conversations discovered, `527`
  bounded messages imported, and `1` inaccessible conversation skipped without
  failing the sync; newly discovered conversations remained observe-only and
  private/DM context remained destination-local;
- tracked source/config dependency search for the removed runtime: clean.

The host runtime is live on the migrated binary. A fresh persistent container
home intentionally has no copied host credential; an operator must authenticate
it once with `make container-codex-login` before using containerized full-agent
jobs. The Compose volume then retains that login without exposing it to the
classifier or application environment.
