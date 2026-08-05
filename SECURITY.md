# Security

tos-tag processes workplace communications and is designed to execute tools.
Treat Slack content, model output, repository files, skills, tool results, and
marketplace artifacts as untrusted input.

## Current initiative

Checked-in configuration defaults to a deterministic Slack stub, shadow
classification, disabled Codex, disabled helper execution, and empty secrets;
fake inference remains available for tests and evals. Live Slack, credentialed
provider calls, and connector effects are opt-in integration modes. No Slack
token, provider credential, connector secret, or live customer content is
required or permitted in normal tests, fixtures, or behavioral evals.

The explicitly approved local development deployment may observe all
user-authorized conversations and bootstrap bounded history once. Durable,
content-free per-conversation completion state prevents full replay on restart.
Completed bot-joined channels receive bounded post-watermark repair; ambient
history remains resolved context and only human direct mentions can become
pending decisions. First-time history and
observe-only conversations never gain output authority from catch-up. Newly discovered
conversations begin observe-only; public/private channels become assist only
after an independent bot-token inventory or membership event confirms Tag has
joined. Leaving reverts the channel to observe before further execution or
delivery. DMs and group DMs are never auto-enabled, and an optional exact-ID
output allowlist can narrow joined-channel authority. This local authority does
not become a tracked default or production authorization.

Slack-authenticated bot, app, workflow, and assistant messages are retained
only as unverified destination-local context. They bypass classification,
reactions, agent admission, and delivery even when they mention Tag or post in
an active Tag thread. Offline recovery applies the same human-author check, and
classifier suppression protects any older pending integration records.

An operator may set a channel to `session_only` context history. That mode
fails closed against prior-session Slack history, cross-channel context,
durable memory recall, and situation-fact recall, and prevents new durable
memory or situation facts from being derived from the channel. It intentionally
disables offline direct-mention recovery for that destination while preserving
the minimum live operational records needed for delivery safety and audit.

## Non-negotiable boundaries

- A message, model, skill, or tool result may request an action; none authorizes
  it.
- Tenant and scope predicates are required before data retrieval.
- Ambient observations cannot authorize writes.
- Public cross-channel context may inform classification when policy allows;
  private channels, DMs, and group DMs are destination-local before and after
  the content query. Do not reveal even content-free awareness of another
  private conversation.
- Generated memory carries the same disclosure class as its source. Restricted
  memory is destination-local at the Mongo query and context post-filter;
  thread memory is also root-thread-local. Public memory alone never widens
  enrollment or output authority.
- Memory consolidation is an asynchronous, tool-free OpenAI call using strict
  structured output and `store: false`. It receives bounded recent human
  messages, never credentials, and cannot execute instructions found in them.
  Model-derived memory is not independent proof for consequential claims.
- Ambient alignment may cite only classifier-selected destination-safe public
  evidence. Human reports remain attributed rather than promoted to verified
  facts, recent participation never becomes a membership claim, and opinions,
  stale conflicts, restricted sources, and unverified agent output do not
  justify intervention.
- Workers receive no long-lived credentials or MongoDB connection string.
- Tool secrets may enter only the exact reviewed subprocess that declares them.
- Workers receive no direct Aion source mount, GitHub credential, or shell.
  `telemetryos.code` can refresh only the requested locally inventoried
  repository's validated `telemetryOS` origin into a server-owned immutable
  remote-default-branch snapshot. It exposes bounded freshness, exact and
  pinned offline semantic search/read results while rejecting arbitrary
  remotes/branches, traversal, symlinks, environment files, credential ledgers,
  and private tool state. Snapshots and Semble indexes are owner-only because
  they contain source.
- The code bundle is rejected at load and execution unless every operation is
  exactly `read` risk. Source edits, patches, commits, pushes, merges, and
  deploys have no worker approval path; the classifier redirects them to
  Linear bug or feature intake.
- `telemetryos.product-docs` remains a deterministic fixed-host reader: it
  constructs HTTPS GETs only for TelemetryOS docs/corporate hosts, rejects
  redirects and arbitrary URLs, has no credential environment, and returns
  bounded output through the normal job-scoped gateway. Separately, Codex may
  receive unrestricted first-party live web search when
  `TAG__CODEX__WEB_SEARCH_MODE=live`. Web content is untrusted, receives no
  credentials or private context authority, and shell/subprocess networking
  remains disabled. Each completed native web search produces a hashed audit
  receipt without persisting its raw query.
- `telemetryos.analytics` is GET-only and fixed to the production or QA
  TelemetryOS Gateway funnel routes. Its Site Analytics Token is written only
  to a mode-0600 helper-local curl config, never argv or worker context. The
  helper rejects arbitrary paths, headers, exports, internal-event inclusion,
  and visitor/session lookup, then removes direct identifiers and free-form
  customer/event content from returned JSON.
- Classifier-marked product answers cannot be delivered unless the same attempt
  successfully reads a full Primer Wiki page, public docs page, or corporate
  full-content source. Search results and model memory are not proof.
- Output destinations derive from admitted server state, never model output.
- Model output may select presentation-only Card and Carousel segments, but its
  strict schema exposes no action/button fields. Interactive controls, modal
  Alerts, approvals, and destinations remain control-plane-owned.
- Model-created Slack mentions are denied by default. The renderer accepts only
  exact user IDs named by the requester in the current message or exact
  user/channel IDs attached by the control plane from selected releasable
  evidence. Tag's invocation mention, broadcasts, user groups, unselected IDs,
  and self-authorized mentions are rejected.
- Every sensitive transition is fenced by live lease and kill-switch state.
- Audit receipts contain redacted metadata, not copies of secret/message data.

## Credential handling

- Keep live values only in ignored mode-`0600` `runtime.env`, the private Codex
  home, or the encrypted organization keystore. Never place them in examples,
  tracked config, Compose interpolation, prompts, tool argv, Slack blocks,
  fixtures, screenshots, or diagnostic artifacts.
- The user OAuth token is read-only context-ingestion authority; the app-level
  and bot-user tokens operate Socket Mode and bot actions. Possessing any token
  does not bypass enrollment, participation, membership, destination, approval,
  or kill-switch checks.
- The direct OpenAI classifier key is control-plane-only and is never reused for
  Codex App Server. Codex authenticates through its private persisted home.
- A Mongo-authoritative organization/workspace flood bucket is charged before
  context construction or direct classification. Exhaustion and bucket-store
  failures fail closed without reactions, agent work, or Slack output, limiting
  provider-cost exposure during accidental or hostile message floods.
- The memory curator is also control-plane-only. It may use a separately
  configured key or the existing control-plane classifier key, but neither is
  passed to Codex or helper subprocesses.
- Rotate development Slack and provider credentials before production use, and
  immediately after suspected disclosure.

## Tool and approval boundary

Every reviewed operation declares an exact ID, risk class, approval policy,
timeout, output limit, permitted environment names, and immutable script hash.
If approval is omitted, the conservative risk-based default applies: `write`
and `destructive` suspend and require an independent exact-action Slack
approval. Admin-risk worker operations are rejected at manifest load and denied
again by the executor. Only source-reviewed manifests can opt out. Agent Wiki
page read/write operations are the current explicit `never` exception; the
separately typed recoverable page soft-delete always requires approval.
Namespace, asset, publish-file, cascading move, activity, generic undo, and
admin Wiki operations are unavailable. All permitted calls remain constrained
by job-scoped capabilities, the selected tool/version/operation, exact argv,
environment allowlists, kill switches, bounds, and tamper-evident execution
receipts. The model cannot alter approval policy at runtime.

When approval applies, inline document bodies are included in the canonical
action hash and Slack cards replace them with a byte count and digest. Wiki
read/write operations do not require approval, but the same exact body is committed by the
execution audit receipt without exposing it in broad audit listings.
Long-form delivery may invoke that reviewed Wiki write proactively, but it does
not gain namespace, asset, file-publish, admin, or arbitrary-write authority.
The Slack response may link only the HTTPS URL returned by a successful write;
failed or unavailable publication must not produce a guessed URL or success
claim. Artifact segments are control-plane checked against successful reviewed
tool results from the same disposable attempt, so model instructions alone
cannot authorize a fabricated artifact link.

## Logging and retention

Correlate observations, classifier decisions, deliveries, jobs, tool calls,
approvals, triggers, and routines without logging raw Slack envelopes, message
text, prompts, model/provider bodies, secrets, tool credentials, lease tokens,
or unbounded results. Keep owner-readable JSONL diagnostics outside Git, retain
durable audit receipts in MongoDB, and honor TTL/source deletion across derived
messages, context packs, prompts, and delivery state.

Generated memory expires no later than its retained sources. Operator
correction pins a reviewed record; forgetting erases its summary, facts, model
metadata, and source IDs while retaining only a content-free source hash and
scope tombstone to prevent immediate relearning. Memory API mutations require
management authentication/CSRF and append audit receipts. Memory prompts and
results never enter the SSE activity feed or broad logs.

The authenticated management activity feed is separately constrained: require
an explicit organization on API and SSE requests, never fan one tenant's record
to another tenant, cap the in-memory replay window, and include source Slack
text only as a bounded public-message excerpt on an explicit classifier record.
Replace restricted-conversation text before publication. Codex activity may
name lifecycle/RPC methods and statuses but must never include prompts, model
output, provider bodies, dynamic-tool arguments/results, or credentials.

Report security issues privately to the repository owners. Do not include live
credentials, private Slack excerpts, or customer data in an issue.
