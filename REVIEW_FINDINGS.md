# tos-tag full source code review — prioritized findings

Date: 2026-08-04. Scope: the entire non-test Go source tree (~26,350 lines) as it stood in the dirty working tree on `main` at commit 05f7c99 plus uncommitted changes.

## Provenance and methodology

1. **Review:** 13 parallel slice reviewers (Claude Fable 5, xhigh effort) each read one coherent package group completely and reported evidence-backed findings — 113 raw findings.
2. **Adversarial verification:** 12 parallel verifier batches (Fable 5) re-read the cited code with a refute-by-default mandate — 87 confirmed, 9 refuted, 17 lowest-severity passed through unverified.
3. **Cross-model adversarial review:** the 32 high+medium findings were independently re-verified against source by Codex (GPT-5.x, high effort, read-only) — 26 AGREE, 6 ADJUST, 0 DISPUTE, plus 1 missed high-severity finding (NEW-1) that was subsequently re-verified by hand.

## How to work a finding

- Line numbers reference the 2026-08-04 working tree and may drift; re-locate the cited code with `rg` before editing.
- Read `CLAUDE.md` and `AGENTS.md` first. Fail-closed defaults, privacy/destination-locality boundaries, and redacted logging are deliberate contracts — fixes must preserve them.
- Several findings note that the in-memory test-double store diverges from the Mongo production store (F3, F17, F34, F42, F43, F46, F88, F105). When fixing one of these, align the two implementations and add a shared conformance test rather than patching one side.
- Flip **Status** to `fixed (<commit>)` when done. Run `make verify` before completion; preserve unrelated dirty changes in this worktree.

---

## P0 — Fix first (high severity, dual-model confirmed)

### NEW-1 — Offline direct messages are never recovered as actionable work

- **Severity:** high | **Category:** bug | **Location:** `core/slack/context_sync.go:181`
- **Status:** fixed (7456162)

Discovery includes IM conversations (`listChannels` requests types `public_channel, private_channel, im` at `core/slack/context_sync.go:383`), but the catch-up set only admits public/private channels where the bot is a member: `if (channel.IsChannel || channel.IsGroup) && botMembership[channel.ID]` at `core/slack/context_sync.go:181`. No other DM recovery path exists (`rg IsIM` finds only discovery/naming/privacy uses). Even if an IM reached recovery, a normal top-level DM is actionable only when it contains an explicit mention or belongs to an existing Tag thread (`core/pipeline/pipeline.go:398-411`). This contradicts the documented contract — CLAUDE.md promises "durable one-time context bootstrap and proactively paced post-watermark catch-up for direct messages missed while offline" — so DM requests sent while the process is down are silently lost across restarts.

**Evidence excerpt (working tree, 2026-08-04):**
```go
// Only bot-joined channels can produce Slack output. Restrict the
// frequent missed-event repair pass to those channels so hundreds of
// observe-only conversations do not create a polling workload.
if (channel.IsChannel || channel.IsGroup) && botMembership[channel.ID] {
	catchUpChannels = append(catchUpChannels, channel)
}
```

**Suggested fix:** Include IM conversations in `catchUpChannels` (they are inherently bot-reachable and few, so the polling-workload rationale for the restriction does not apply), and ensure recovered top-level DM messages take the same actionable path as live DM ingress rather than requiring an explicit mention. Alternatively, if DM catch-up is deliberately out of scope, correct CLAUDE.md/architecture.md — but the documented behavior is the more defensible contract.

**Origin:** found by Codex during the adversarial pass (missed by the primary review); independently re-verified against `context_sync.go:171-183`, `context_sync.go:383`, and a repo-wide search for any alternate DM recovery path.

### F2 — CatchUp permanently wedges on over-budget channels and aborts all later channels

- **Severity:** high | **Category:** bug | **Location:** `core/slack/context_sync.go:247`
- **Status:** fixed (7456162)

When a channel's missed-message count exceeds its per-pass budget (min(MessagesPerChannel=100, fairShare)), backfillChannel returns complete=false and CatchUp returns an error immediately, skipping every remaining channel in run.catchUpChannels. The watermark is deliberately retained, but the budget is identical on every retry (startup and each 5-minute refresh recompute the same fairShare), so no progress is ever possible: the un-advanced watermark makes the gap window grow each tick, keeping the channel over budget forever for any channel averaging more than ~75 messages per lookback window. One overnight outage on a busy channel therefore permanently disables missed-event recovery for that channel and every channel after it in iteration order, silently dropping missed direct mentions once they age past the 7-day lookback.

**Evidence excerpt (working tree, 2026-08-04):**
```go
if !complete {
	return stats, fmt.Errorf("catch up Slack context channel %s exceeded its safe message bound; prior watermark retained", channel.ID)
}
```

**Suggested fix:** On an incomplete channel, log and `continue` to the remaining channels instead of returning, and advance the watermark to the timestamp of the last contiguously imported message so successive passes make forward progress.

**Fable verifier:** Verified CatchUp (context_sync.go:246-248) returns an error on any over-budget channel skipping all remaining catchUpChannels, Advance only runs on complete so the watermark freezes, and budget=min(MessagesPerChannel=100,fairShare) is recomputed identically every refresh tick, so a channel exceeding ~75 roots per lookback window wedges permanently and disables recovery for every channel after it.

**Codex adversarial verdict: AGREE** — CatchUp exits the entire pass when one channel exceeds its budget, before advancing that channel's watermark or visiting later channels (core/slack/context_sync.go:217-249). Because backfillChannel returns incomplete whenever its fixed budget is exhausted (core/slack/context_sync.go:402-504), a sufficiently busy early channel can permanently wedge recovery.

### F3 — MongoQueue.Transition silently drops mutate updates to ResolvedModel/RouteTrace

- **Severity:** high | **Category:** bug | **Location:** `core/jobs/mongo_queue.go:144`
- **Status:** fixed (7456162)

Transition applies the caller's mutate func to an in-memory Job but persists only a fixed field set (state, result, failure_reason, available_at, approval_id, approved_action_hash, progress_message_ts). MemoryQueue.Transition persists the entire mutated job, so tests pass. Production caller pipeline.go:1468 relies on this for routine/heartbeat jobs: `Transition(..., StateRunning, func(current *jobs.Job) { current.ResolvedModel, current.RouteTrace = resolved, trace })`. Under Mongo the resolved model is never stored, and the caller reassigns `job` from the returned document, zeroing job.ResolvedModel; the very next gate `ModelRouter.Allowed(job.ResolvedModel)` returns false for an empty ProfileID, so every Mongo-backed routine/heartbeat job hard-fails with model_hard_deny (or would run with an empty model if the gate were absent).

**Evidence excerpt (working tree, 2026-08-04):**
```go
set := bson.M{
	"state":                string(to),
	"result":               current.Result,
	"failure_reason":       current.FailureReason,
	"available_at":         current.AvailableAt,
	"approval_id":          current.ApprovalID,
	"approved_action_hash": current.ApprovedActionHash,
	"progress_message_ts":  current.ProgressMessageTS,
```

**Suggested fix:** Persist resolved_model and route_trace in the Transition $set (or diff the mutated Job against the fetched one and persist all changed fields) so the Mongo implementation matches the MemoryQueue contract.

**Fable verifier:** Verified MongoQueue.Transition's $set (mongo_queue.go:144-153) omits resolved_model/route_trace while MemoryQueue persists the whole mutated job, routines/triggers enqueue with empty ResolvedModel, CanTransition permits Running→Running, and Registry.Allowed with empty ProfileID returns false, so every Mongo-backed (production, core.go:85) routine/heartbeat job fails with model_hard_deny at pipeline.go:1473.

**Codex adversarial verdict: AGREE** — Mongo Transition applies the mutator in memory but persists only a fixed field subset that omits ResolvedModel and RouteTrace (core/jobs/mongo_queue.go:129-174). The pipeline then uses the returned, still-empty model and immediately applies the model allowlist (core/pipeline/pipeline.go:1457-1475), so routine and heartbeat jobs can be hard-denied after successful routing.

---

## P1 — Medium severity (29, Codex-reviewed)

### Pipeline & jobs

#### F6 — Final delivery enqueue failure after job succeeds silently loses the response

- **Severity:** medium | **Category:** bug | **Location:** `core/pipeline/pipeline.go:1571`
- **Status:** fixed (7456162)

In processOneJob the job is transitioned to StateSucceeded first, and only then is the final Slack delivery enqueued. If Deliveries.Enqueue fails (transient Mongo error), the error is only logged and nothing ever retries: reconcileJobs handles only StateWaitingApproval, expired StateQueued, StateNeedsReconciliation, and StateRetryWait, and no other reconciler re-enqueues deliveries for succeeded jobs (verified: the only StateSucceeded consumer is MarkCompletedUndelivered). The user-visible answer is permanently lost in a system whose stated contract is durable delivery, even though job.Result is persisted and recovery would be possible.

**Evidence excerpt (working tree, 2026-08-04):**
```go
_, _, err = p.deps.Deliveries.Enqueue(ctx, deliveries.Spec{ ... })
	if err != nil {
		jobLogger.WithCtx(blackbox.Ctx{"error_type": fmt.Sprintf("%T", err)}).Error("final Slack delivery enqueue failed")
```

**Suggested fix:** Enqueue the final delivery before (or atomically with) the StateSucceeded transition, or add a reconcile pass that re-enqueues the persisted job.Result for succeeded jobs with no matching delivery record (idempotency key job.ID+"/final" already makes this safe).

**Fable verifier:** Verified the StateSucceeded transition (pipeline.go:1548) precedes Deliveries.Enqueue (1566) whose failure is only logged (1571-1572), and reconcileJobs (1288-1379) handles only WaitingApproval, expired Queued, NeedsReconciliation, and RetryWait, so a transient enqueue failure permanently loses the persisted answer despite the durable-delivery contract.

**Codex adversarial verdict: AGREE** — The job is transitioned to succeeded and its admission released before final delivery enqueue, whose failure is only logged (core/pipeline/pipeline.go:1548-1575). Reconciliation handles waiting, expired, reconciliation-needed, and retrying jobs but never re-enqueues delivery for succeeded jobs (core/pipeline/pipeline.go:1288-1379), so the response can be permanently lost.

#### F7 — Observation output guard is acquired after enqueue and losing it only warns

- **Severity:** medium | **Category:** bug | **Location:** `core/pipeline/pipeline.go:822`
- **Status:** fixed (7456162)

decideObservation enqueues the job (line 796) or direct-reply delivery (line 730) first and calls Observations.MarkOutput afterwards; when the compare-and-set guard is already held (won == false) the code merely logs a warning while the freshly enqueued job/delivery remains queued and will deliver. The jobs idempotency key is observation.PublicID + "/" + outcome, so any second decision for the same observation with a different outcome (revision-2 reconsiderLateQuestions, or a revision-1 retry after CompleteDecision fails where the non-deterministic classifier picks a different outcome) produces a second job and a duplicate Slack response. The guard therefore never actually prevents double output.

**Evidence excerpt (working tree, 2026-08-04):**
```go
won, err := p.deps.Observations.MarkOutput(ctx, observation.PublicID, string(job.ID), "")
	...
	if !won {
		p.deps.Logger.Warnf("observation output guard already held observation=%s", observation.PublicID)
```

**Suggested fix:** Check/claim the output guard before enqueueing, or on won == false cancel the just-enqueued job/abandon the delivery instead of only warning.

**Fable verifier:** Verified MarkOutput is called only after Jobs.Enqueue/Deliveries.Enqueue with won==false merely warning (pipeline.go:750-756, 822-828), ClaimPending re-claims processing observations with expired leases (observer/mongo_store.go:118-124), and the job idempotency key embeds the outcome (pipeline.go:806), so a re-decision picking a different outcome enqueues a duplicate job nothing cancels; LateCandidates does honor output_produced, so the guard is only ineffective on the retry/race path, supporting medium.

**Codex adversarial verdict: ADJUST** — The enqueue-before-MarkOutput ordering creates a real duplicate-output window (core/pipeline/pipeline.go:729-756, 796-827). However, the claimed revision-2 path cannot occur after a successful mark because LateCandidates explicitly requires output_produced:false; duplication requires enqueue to succeed while marking fails. Corrected fix: an atomic output reservation or outcome-independent idempotency (core/observer/mongo_store.go:292-328).

#### F8 — Transient Jobs.Get error during harness heartbeat is misclassified as revocation

- **Severity:** medium | **Category:** bug | **Location:** `core/pipeline/pipeline.go:1932`
- **Status:** fixed (7456162)

On every heartbeat tick inside runHarness, any error from Jobs.Get (a momentary Mongo blip) is treated identically to a confirmed state change: the Codex session is aborted and errExecutionRevoked is returned. In processOneJob the errExecutionRevoked branch then Cancels the running job outright (no requeue, no failure notice), or if the follow-up Get also fails, leaves it to lease-expire into needs_reconciliation requiring operator intervention. A healthy long-running agent job is permanently killed by one transient read error, unlike the Heartbeat write error just below, which flows to the requeue path.

**Evidence excerpt (working tree, 2026-08-04):**
```go
current, getErr := p.deps.Jobs.Get(ctx, job.ID)
if getErr != nil || current.State != jobs.StateRunning {
	_ = p.deps.Harness.Abort(context.Background(), session.ID)
	return types.SlackResult{}, errExecutionRevoked
```

**Suggested fix:** Only return errExecutionRevoked when Get succeeds and shows a non-running state; on getErr return a plain retryable error (like the Heartbeat error path) so the job requeues instead of being cancelled.

**Fable verifier:** Verified the heartbeat tick (pipeline.go:1931-1936) treats any Jobs.Get error as errExecutionRevoked, whose branch in processOneJob (1503-1515) Cancels a still-running job with no requeue or failure notice (or leaves it to lease-expire into needs_reconciliation), while the adjacent Heartbeat write error (1944-1947) flows to the requeue path — an asymmetry that kills healthy long-running jobs on one transient read error and is not fail-closed-by-design since aborting the attempt plus requeue would preserve safety.

**Codex adversarial verdict: AGREE** — The heartbeat treats any Jobs.Get error identically to confirmed non-running state and aborts the harness as revoked (core/pipeline/pipeline.go:1931-1947). The caller then cancels only if a follow-up read succeeds but releases admission regardless (core/pipeline/pipeline.go:1496-1514), so a transient Mongo read error can terminate valid work without retry.

#### F9 — runHarness early returns after CreateJobSession leak a provisioned Codex worker

- **Severity:** medium | **Category:** bug | **Location:** `core/pipeline/pipeline.go:1792`
- **Status:** fixed (7456162)

CreateJobSession (line 1774) provisions a full disposable Codex app-server process and workspace. The approval-resume validation block (missing repository, stale action hash, consumed/unapproved, json.Marshal failure) and the Prompt error return at line 1803 then return without calling Harness.Abort. Worker termination otherwise happens only inside Events' goroutine (defer terminate) or Abort, so on these paths the process and workspace stay alive until wall-time expiry, and the session entry stays registered. These validations also need no worker at all, so provisioning before them is wasted work.

**Evidence excerpt (working tree, 2026-08-04):**
```go
approval, approvalErr := p.deps.Approvals.GetContext(ctx, job.OrganizationID, job.ApprovalID)
if approvalErr != nil || approval.ActionHash != job.ApprovedActionHash || approval.ConsumedAt.After(time.Time{}) || approval.ApprovedAt.IsZero() {
	return types.SlackResult{}, errors.New("approved action no longer matches the resumable job")
```

**Suggested fix:** Perform the approval-resume validation before CreateJobSession, and add a defer/explicit Abort(session.ID) for every error return between session creation and the Events loop.

**Fable verifier:** Verified that runHarness's approval-resume error returns at pipeline.go:1789/1793/1797 (and the caller's requeue path) never call Harness.Abort, while worker_codex.go/local.go show cleanup of the sessions map, tool capability, and workspace root happens only via terminate (Events defer/Abort/Close), so the provisioned app-server process runs until the 5-minute wall-time SIGKILL and the workspace directory and session entry are never reclaimed.

**Codex adversarial verdict: AGREE** — After creating a worker session, approval validation and Prompt errors return directly without calling Abort (core/pipeline/pipeline.go:1766-1804). The session is already registered by then, while normal termination is attached to Events, explicit abort, or later prompt failures (core/harness/worker_codex.go:144-208, 303-372).

#### F10 — containsIncident substring match on "down" fires on download/markdown/shutdown

- **Severity:** medium | **Category:** bug | **Location:** `core/pipeline/pipeline.go:2396`
- **Status:** fixed (7456162)

containsIncident uses bare strings.Contains, so any message containing "download", "markdown", "shutdown", "showdown", or "downstream" is treated as an incident signal. That both promotes such messages from other channels to PartitionEvidence priority 90 in every context pack (line 1109) and, worse, triggers reconsiderLateQuestions after each such message — a LateCandidates Mongo query plus up to 10 full revision-2 decideObservation passes, each rebuilding a 500-message context pack and potentially making flood-charged provider classifier calls. Everyday phrases like "convert it to markdown" incur real cross-channel classification cost.

**Evidence excerpt (working tree, 2026-08-04):**
```go
lower := strings.ToLower(text)
return strings.Contains(lower, "incident") || strings.Contains(lower, "outage") || strings.Contains(lower, "down")
```

**Suggested fix:** Match on word boundaries (e.g., a precompiled regexp like \b(incident|outage|down)\b or token comparison over strings.Fields) so substrings of unrelated words do not trigger incident handling.

**Fable verifier:** Re-verified pipeline.go:2396 uses bare strings.Contains on "down" (matching download/markdown/shutdown), which at line 1109 promotes such cross-channel messages to PartitionEvidence priority 90 in every context pack and at line 586 fires reconsiderLateQuestions (LateCandidates query plus up to 10 revision-2 decideObservation passes), while the classifier's own containsIncident (service.go:977) deliberately uses " down" showing this copy is the sloppy one.

**Codex adversarial verdict: AGREE** — containsIncident uses an unrestricted substring match for "down" (core/pipeline/pipeline.go:2394-2396), so ordinary words such as "download" trigger incident reconsideration. Every matching human message invokes the cross-channel late-question path (core/pipeline/pipeline.go:581-587, 921-943).

#### F30 — Job claim/recover/heartbeat queries cannot use any defined index

- **Severity:** medium | **Category:** inefficiency | **Location:** `core/jobs/mongo_queue.go:78`
- **Status:** fixed (7456162)

The only claim-shaped index is job_claim {organization_id, state, available_at, lease.expires_at} (database.go:134), but RecoverExpired filters on {state, lease.expires_at} and Claim filters on {state, available_at, expires_at, $expr} — both without organization_id, so neither can use the index prefix and both are full collection scans. Get/Heartbeat/Cancel/ResumeFromApproval all filter on public_id, which has no index on the jobs collection at all. Claim also runs RecoverExpired's two UpdateMany scans on every call, and each worker (up to 64) polls Claim every 250ms (config Jobs.Poll default), multiplying the scans.

**Evidence excerpt (working tree, 2026-08-04):**
```go
if _, err := collection.UpdateMany(ctx, bson.M{
	"state":            string(StateLeased),
	"lease.expires_at": bson.M{"$lte": now},
}
```

**Suggested fix:** Add a {state, available_at, lease.expires_at} (org-less) claim index and a unique public_id index for jobs in RequiredIndexes, and move RecoverExpired to its own timer instead of running inside every Claim.

**Fable verifier:** Verified database.go:133-136 defines only the org-prefixed job_claim index while RecoverExpired ({state, lease.expires_at}) and Claim ({state, available_at, expires_at, $expr}) omit organization_id, no public_id index exists on jobs for Get/Heartbeat/Cancel/ResumeFromApproval, and Claim runs RecoverExpired's two UpdateMany scans on every 250ms poll (config.go:262) per worker.

**Codex adversarial verdict: AGREE** — Each claim first runs an expired-lease recovery query and then a claim query whose filters do not include the organization prefix used by the principal compound index (core/jobs/mongo_queue.go:75-104, core/database/database.go:133-136). Frequent operations also query public_id without a corresponding index (core/jobs/mongo_queue.go:117, 192-216).

#### F32 — Unfiltered Queue.List decodes every job document 4x/second for reconciliation

- **Severity:** medium | **Category:** inefficiency | **Location:** `core/jobs/mongo_queue.go:298`
- **Status:** fixed (7456162)

MongoQueue.List runs Find with an empty filter and no limit, decoding every job across all organizations and all states (including terminal succeeded/failed/cancelled docs that live until their TTL, typically 24h+). Its only production consumer is reconcileJobs (pipeline.go:1291), which runs on the Jobs.Poll ticker (250ms default) but only acts on waiting_approval, expired queued, retry_wait, and needs_reconciliation jobs. The interface offers no state-filtered listing, so the full collection is fetched and materialized four times per second.

**Evidence excerpt (working tree, 2026-08-04):**
```go
func (q *MongoQueue) List(ctx context.Context) ([]Job, error) {
	return q.list(ctx, bson.M{})
}
```

**Suggested fix:** Add a ListByStates(ctx, states []State) method (indexed on state) and use it from reconcileJobs, or at minimum restrict List's filter to the non-terminal states reconciliation acts on.

**Fable verifier:** Verified MongoQueue.List (mongo_queue.go:297-299) runs an unfiltered, unlimited Find sorted on created_at, and reconcileJobs (pipeline.go:1293) calls it on the 250ms Jobs.Poll ticker (pipeline.go:1261-1272, config.go:262), decoding every job document including terminal docs retained until their 24h+ TTL, four times per second.

**Codex adversarial verdict: AGREE** — Queue.List loads every job without filter or limit (core/jobs/mongo_queue.go:297-322). Reconciliation invokes it on every configured 250 ms poll and then examines only a few exceptional states (core/pipeline/pipeline.go:1261-1379, core/config/config.go:260-264), causing collection-size-dependent work four times per second.

#### F41 — Non-atomic mark-completed-then-decrement permanently leaks admission slots

- **Severity:** medium | **Category:** bug | **Location:** `core/admission/mongo.go:109`
- **Status:** fixed (7456162)

Both reconcileExpired and Complete first mark reservations completed and only then decrement the state's active counter in a separate write. If the process crashes or Mongo returns a transient error between the two writes, the reservations are already completed:true so reconcileExpired will never count them again, and the channel's active counter stays permanently inflated — eventually every Admit is denied with ErrConcurrency and the channel goes silent with no self-heal. In Complete the decrement error is fully swallowed (`_, _ =`), so this state corruption is invisible. The reconcile decrement also lacks the `active: {"$gt": 0}` clamp used elsewhere, so a bulk `-result.ModifiedCount` can drive `active` negative.

**Evidence excerpt (working tree, 2026-08-04):**
```go
result, err := m.db.Collection(models.CollectionAdmissionReservations).UpdateMany(ctx,
	bson.M{"state_id": stateID, "completed": false, "expires_at": bson.M{"$lte": now}},
	bson.M{"$set": bson.M{"completed": true, ...}},
)
...
_, err = m.db.Collection(models.CollectionAdmissionStates).UpdateOne(ctx, bson.M{"_id": stateID}, bson.M{"$inc": bson.M{"active": -result.ModifiedCount}})
```

**Suggested fix:** Make the pair idempotent: derive `active` from a count of incomplete reservations (or run both writes in a transaction / decrement per-reservation with a reconciled flag), and log the swallowed decrement error in Complete.

**Fable verifier:** Verified reconcileExpired and Complete mark reservations completed:true before a separate active decrement whose failure is swallowed, and rg confirms no other code ever recomputes admission_states.active, so a transient error permanently leaks a concurrency slot until manual DB repair; only the negative-counter sub-claim is unreachable since each reservation flips completed exactly once.

**Codex adversarial verdict: AGREE** — Both completion and expiry reconciliation first mark reservations completed and only afterward decrement the admission state in a separate operation (core/admission/mongo.go:98-116). A crash or transient failure between those writes makes the reservation permanently unrecoverable while leaving active inflated, and Complete discards the decrement error entirely.

### Slack ingress

#### F19 — Catch-up cannot recover thread replies to roots older than the watermark

- **Severity:** medium | **Category:** bug | **Location:** `core/slack/context_sync.go:448`
- **Status:** fixed (7456162)

backfillChannel fetches thread replies only for roots discovered in the post-watermark conversations.history window (`for _, root := range roots`). Slack channel history does not include non-broadcast thread replies, so a reply posted during downtime into a thread whose root predates the watermark is never seen — yet CatchUp still advances the watermark past it (line 249), permanently skipping it. This defeats the documented recovery case in pipeline.RecoverContextEnvelope ("a reply in an already active Tag thread is accepted into the normal durable decision queue"): an active Tag thread's root always predates the outage, so in-thread mentions/continuations missed while offline are silently lost.

**Evidence excerpt (working tree, 2026-08-04):**
```go
repliesComplete := true
for _, root := range roots {
```

**Suggested fix:** During catch-up, also enumerate recently-active threads (e.g., track known Tag-session thread roots from Mongo, or use conversations.replies on threads with latest_reply after the watermark) before advancing the watermark, or at minimum document that in-thread gap repair is unsupported and stop advertising it in RecoverContextEnvelope.

**Fable verifier:** backfillChannel only fetches replies for roots found in the post-watermark conversations.history window (which excludes non-broadcast thread replies and pre-watermark roots), CatchUp advances the watermark regardless (context_sync.go:249), and RecoverContextEnvelope's documented active-Tag-thread admission (pipeline.go:376-404) is therefore unreachable for downtime replies since active session roots predate the outage; no other code path recovers them.

**Codex adversarial verdict: AGREE** — Catch-up fetches replies only for roots returned by the post-watermark history query (core/slack/context_sync.go:402-502). Since non-broadcast replies to older roots are absent from that history window, advancing the watermark after completion (core/slack/context_sync.go:249) permanently loses offline continuations in existing Tag threads.

### Codex harness

#### F24 — emit/complete send on events channel while holding session mutex — deadlock chain

- **Severity:** medium | **Category:** bug | **Location:** `core/harness/worker_codex.go:77`
- **Status:** fixed (7456162)

codexWorkerSession.emit performs a channel send on the 128-buffered s.events while holding s.mu (complete() does the same). If the Events forwarder stalls (pipeline event loop does Mongo appendReceipt writes inline) or exits on ctx.Done, the buffer can fill and emit blocks forever holding the lock. Every other path then wedges on s.mu: notification() (blocking the App Server readLoop), session.publish inside terminate() (so the workspace is never terminated), and Abort() at line 363 — which the pipeline calls synchronously on ctx.Done, permanently wedging that pipeline job goroutine and leaking the Codex worker process, tool capability, and goroutines.

**Evidence excerpt (working tree, 2026-08-04):**
```go
func (s *codexWorkerSession) emit(event Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.events <- event
}
```

**Suggested fix:** Do not send while holding the mutex: check s.closed under the lock, release it, then send guarded by a select on a per-session done channel (closed by fail/complete), so a stalled consumer can never block fail/terminate/Abort.

**Fable verifier:** emit/complete send on the 128-buffered events channel while holding s.mu and notification is invoked synchronously from the App Server readLoop, so after the Events forwarder exits on ctx.Done a still-streaming turn fills the buffer, emit blocks holding the mutex, and terminate (publish before client.close), Abort (session.mu.Lock at line 363, then client.call with context.Background awaiting a response the blocked readLoop can never deliver), and the pipeline job goroutine all wedge permanently, leaking the worker process and workspace.

**Codex adversarial verdict: AGREE** — Session emit, fail, and complete hold the session mutex while sending into a bounded event channel (core/harness/worker_codex.go:71-104, 177). If the buffer fills while downstream event handling blocks, abort and termination also need that mutex and can deadlock (core/harness/worker_codex.go:303-372, 398-412).

#### F25 — Token usage decodes only tokenUsage.last, under-reporting multi-request turns

- **Severity:** medium | **Category:** bug | **Location:** `core/harness/worker_codex.go:466`
- **Status:** fixed (7456162)

The thread/tokenUsage/updated handler decodes only the `last` bucket (the most recent model request), ignoring the `total` bucket the protocol also carries (visible in the test fixture at worker_codex_test.go:109, which sends both). The pipeline (pipeline.go:1912-1917) assigns rather than accumulates these values, so for any turn with tool calls — the normal full-agent path, where each tool round-trip is a separate model request — the Slack footer and usage metrics report only the final request's tokens instead of the turn total. Since threads are ephemeral single-turn, `total` is exactly the correct turn usage.

**Evidence excerpt (working tree, 2026-08-04):**
```go
TokenUsage struct {
	Last struct {
		InputTokens           int64 `json:"inputTokens"`
		OutputTokens          int64 `json:"outputTokens"`
```

**Suggested fix:** Decode tokenUsage.total (or additionally decode it and emit it as the authoritative cumulative usage) so per-turn usage reflects all model requests, and keep the pipeline's overwrite semantics.

**Fable verifier:** Handler at worker_codex.go:460-484 decodes only tokenUsage.last while the fixture (worker_codex_test.go:109) shows the protocol also sends total, and pipeline.go:1912-1917 overwrites rather than accumulates, so multi-request tool-using turns report only the final request in the footer and usage metering (pipeline.go:1968, 1990).

**Codex adversarial verdict: AGREE** — The app-server notification exposes both last and cumulative total usage, but the worker emits only last (core/harness/worker_codex.go:460-484). The pipeline subsequently overwrites its footer counters with each update (core/pipeline/pipeline.go:1912-1917), under-reporting turns that involve multiple model requests.

#### F26 — Prompt early-validation errors leak the provisioned worker and session forever

- **Severity:** medium | **Category:** bug | **Location:** `core/harness/worker_codex.go:223`
- **Status:** fixed (7456162)

Prompt's early returns (non-openai provider at line 220, empty model/RequestID at line 223) return an error without calling w.terminate(sessionID), unlike all later stage failures (thread/start, turn/start) which do. The pipeline (pipeline.go:1802-1804) also returns on Prompt error without calling Abort, and runHarness has no deferred cleanup. Result: the already-provisioned `codex app-server` process keeps running until wall-time kill, the registered tool-bridge capability stays live until lease expiry, and the entry in w.sessions is never removed (terminate is the only remover), growing unboundedly across recurrences.

**Evidence excerpt (working tree, 2026-08-04):**
```go
if strings.TrimSpace(model) == "" || strings.TrimSpace(prompt.RequestID) == "" {
	return errors.New("Codex prompt model and request ID are required")
}
```

**Suggested fix:** Call w.terminate(sessionID) before returning from every Prompt error path (or validate the model/request-ID before CreateJobSession provisions anything).

**Fable verifier:** Prompt's early returns at worker_codex.go:220/223 skip w.terminate unlike every later failure path, pipeline.go:1802-1804 returns without Abort and processOneJob requeues without cleanup, and operator-editable model profiles only require a non-empty provider (router.go:46), so a non-openai profile leaks the provisioned app-server, live capability, and a permanent w.sessions entry per attempt.

**Codex adversarial verdict: AGREE** — Prompt returns on provider mismatch or empty model/request after looking up the live session, without terminating it (core/harness/worker_codex.go:211-224). Later thread-creation and turn-start failures do terminate (core/harness/worker_codex.go:253-294), confirming these validation branches are an unhandled leak.

### Deliveries & rendering

#### F1 — Blind fence trimming corrupts plain-text answers starting/ending with a code block

- **Severity:** medium | **Category:** bug | **Location:** `core/deliveries/model_output.go:21`
- **Status:** fixed (7456162)

ParseModelOutput unconditionally strips a leading and trailing "```" before checking whether the output is JSON. A plain-text fallback answer that legitimately ends with a fenced code block (e.g. "Run this:\n```go\ncode\n```") or begins with one loses one fence marker, leaving an odd fence count. The mrkdwn segment then fails validateMRKDWN's unbalanced-fence check at render time, pipeline.processOneDelivery retries with reason invalid_render and delay 0 until attempts are exhausted, and the job is marked completed-undelivered — a valid answer is silently destroyed.

**Evidence excerpt (working tree, 2026-08-04):**
```go
raw = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(raw, "```json"), "```"), "```"))
```

**Suggested fix:** Only strip the fence wrapper when the text both starts with ``` (optionally ```json) and ends with ```, i.e. when the whole payload is a single fenced block wrapping JSON; leave prose containing fences untouched.

**Fable verifier:** Verified TrimSuffix/TrimPrefix at model_output.go:21 strips one fence from prose that legitimately starts/ends with a code block, leaving an odd fence count that validateMRKDWN (renderer.go:982) rejects; however the failure surfaces at processOneJob's pre-delivery Render check (pipeline.go:1536), failing the job with an interactive failure notice rather than the claimed silent invalid_render delivery loop, so the answer is lost but not silently.

**Codex adversarial verdict: AGREE** — decodeModelOutput independently strips leading and trailing code fences before attempting JSON parsing or plaintext fallback (core/deliveries/model_output.go:16-29). A normal prose answer ending in a fenced block therefore loses its closing fence and fails the renderer's balanced-fence check (core/deliveries/renderer.go:964-991).

#### F16 — Rich-text cell tokenizer mangles underscores/asterisks in literal content

- **Severity:** medium | **Category:** bug | **Location:** `core/deliveries/renderer.go:1331`
- **Status:** fixed (7456162)

renderRichTextElements toggles a style whenever a marker character has any later occurrence in the cell, with no word-boundary or code-span awareness. A table cell like "user_id and channel_id" (two underscores) is routed into this path by hasInlineSlackFormatting even when typed raw_text, and renders as "user" + italic "id and channel" + "id" — both underscores are eaten. Backtick-quoted identifiers per the output prompt fare no better because the underscore still toggles italic inside the code span. Identifier-heavy table cells are a common real payload, so displayed content is corrupted.

**Evidence excerpt (working tree, 2026-08-04):**
```go
if active[styleName] || strings.Contains(text[index+1:], string(text[index])) {
				flush()
				active[styleName] = !active[styleName]
```

**Suggested fix:** Suspend style-marker processing inside code spans, require Slack's word-boundary semantics for _ and * toggles (marker adjacent to non-space on the inner side and space/punctuation on the outer side), and never reinterpret cells the model explicitly typed raw_text.

**Fable verifier:** Verified renderRow (renderer.go:1240-1250) routes raw_text-typed cells with any repeated marker into renderRichTextElements, whose toggle at 1330-1336 has no word-boundary or code-span awareness, so "user_id and channel_id" loses both underscores and gains spurious italics — diverging from Slack's own mrkdwn semantics for common identifier-heavy cells.

**Codex adversarial verdict: ADJUST** — The renderer really does promote raw_text whenever paired _, *, ~, or backticks are detected, and its tokenizer ignores delimiter boundaries and code-span context (core/deliveries/renderer.go:1236-1278, 1291-1347). However, blanket removal of promotion would contradict the existing intentional formatted-raw_text contract (core/deliveries/renderer_test.go:266-294). Corrected fix: boundary-aware Slack parsing or a deliberate contract migration to rich_text.

#### F18 — Unlabeled Slack links bypass the unsafe-link scheme validation

- **Severity:** medium | **Category:** bug | **Location:** `core/deliveries/renderer.go:985`
- **Status:** fixed (7456162)

validateMRKDWN only iterates slackLinkPattern matches, which require the labeled `<url|label>` form. A bare angle-bracket link such as `<file:///etc/hosts>` or `<slack://...>` matches neither slackLinkPattern nor slackEntityPattern, so it passes validation and is delivered as mrkdwn where Slack renders angle-bracket URLs as links. The explicit "unsafe Slack link" rule (https/http with a host) is therefore trivially bypassed by omitting the label, undermining a renderer whose purpose is strict output validation.

**Evidence excerpt (working tree, 2026-08-04):**
```go
for _, match := range slackLinkPattern.FindAllStringSubmatch(text, -1) {
		parsed, err := url.Parse(match[1])
```

**Suggested fix:** Also match unlabeled `<scheme:...>` tokens (excluding the @/#/! entity forms) and apply the same https/http+host validation to them.

**Fable verifier:** slackLinkPattern (<([^>|]+)\|([^>]+)>) requires a label, slackEntityPattern only matches @/#/! forms, and no other validateMRKDWN or render-path check inspects unlabeled <scheme:...> tokens, so a bare <file:///x> or <slack://...> passes the explicit unsafe-link rule and is delivered verbatim in mrkdwn where angle brackets are Slack link syntax.

**Codex adversarial verdict: AGREE** — MRKDWN validation recognizes only labelled Slack links of the form <url|label> and special entities (core/deliveries/renderer.go:36-41, 964-991). Bare angle-bracket links such as <file:///...> or <slack://...> therefore bypass scheme validation entirely.

### Classifier

#### F13 — isClearlyExternalPublicSourceQuestion only recognizes OpenAI

- **Severity:** medium | **Category:** ai-slop | **Location:** `core/classifier/openai_classifier.go:1011`
- **Status:** fixed (7456162)

The function name and its use in withProductKnowledgePolicyCorrections promise a general 'external public source' carve-out, but the implementation returns false unless the text literally contains 'openai'. A question about any other vendor's pricing page (AWS, Stripe, Slack) whose provider-assigned topic contains 'pricing'/'billing' passes isObviousProductKnowledgeQuestion and is force-rewritten into TelemetryOS Primer/product-doc retrieval, which is exactly the misrouting this carve-out was added to prevent. This looks like an eval-specific patch that silently under-generalizes.

**Evidence excerpt (working tree, 2026-08-04):**
```go
if !containsAny(lower, "openai", "open ai") {
	return false
}
```

**Suggested fix:** Detect the external-source shape generically (a named non-TelemetryOS vendor/site plus pricing/page keywords) instead of hardcoding one vendor, or rename and document the OpenAI-only scope.

**Fable verifier:** Verified isClearlyExternalPublicSourceQuestion:1011 hard-requires "openai" despite the general external-source comment, and withProductKnowledgePolicyCorrections wholesale rewrites any question with a provider topic containing pricing/billing into forced TelemetryOS Primer retrieval, with openai_classifier_test.go:324 showing the carve-out was patched for exactly one observed vendor case.

**Codex adversarial verdict: AGREE** — The external-public-source exception explicitly returns false unless the text mentions OpenAI (core/classifier/openai_classifier.go:1009-1021). Meanwhile, generic provider pricing, billing, or product wording is classified as product knowledge (core/classifier/openai_classifier.go:1046-1066), incorrectly routing questions about AWS, Stripe, and similar external products into TelemetryOS retrieval.

#### F15 — MongoDecisionStore.list loads the entire decisions collection unbounded

- **Severity:** medium | **Category:** inefficiency | **Location:** `core/classifier/store.go:136`
- **Status:** fixed (7456162)

list() runs Find with no limit or projection and cursor.All()s every decision document (each embedding two full ClassificationDecision structs) into memory. The /status endpoint (server.go:242-258) calls Jobs.List, Deliveries.List, and Decisions.List solely to report len() counts, so every status request decodes the whole ever-growing decisions collection. There is also no TTL index on decisions in database.go, so this cost grows without bound over the deployment's life.

**Evidence excerpt (working tree, 2026-08-04):**
```go
cursor, err := s.db.Collection(models.CollectionDecisions).Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}}))
```

**Suggested fix:** Add a Count method (CountDocuments) to DecisionStore for /status, and give List/ListOrganization a limit parameter or server-side cap.

**Fable verifier:** Verified MongoDecisionStore.list (store.go:135) runs an unfiltered unlimited Find with cursor.All, /status (server.go:242-258) decodes the whole collection just for len(), and CollectionDecisions has only the unique revision index in database.go with no TTL and no retention-janitor coverage, so the cost grows without bound.

**Codex adversarial verdict: AGREE** — The Mongo decision repository performs an unbounded, oldest-first Find followed by cursor.All (core/classifier/store.go:124-150). The status handler calls it merely to compute a count (core/server/server.go:241-260), and the decision collection has neither a limiting query nor TTL index (core/database/database.go:128-130).

### Server & admin UI

#### F21 — Learned-notes admin page can never list any notes

- **Severity:** medium | **Category:** bug | **Location:** `core/server/server.go:732`
- **Status:** fixed (7456162)

The listNotes handler forwards channel_id straight from the query string, and the management UI (generic loadScoped path) only ever sends organization_id. Both Repository implementations require an exact channel match: the production MongoStore.ListNotes builds the filter bson.M{"organization_id": org, "channel_id": ""} (unlike scopeFilter, which omits an empty channel_id, and unlike ListDirectives, which aggregates across channels when channel_id is empty), and the memory store looks up s.notes[scopeKey(org, "")]. So GET /admin/api/notes?organization_id=X always returns empty, and the 'Learned channel notes' page permanently shows 'No learned notes have been proposed yet' even when notes exist.

**Evidence excerpt (working tree, 2026-08-04):**
```go
values, err := s.deps.ChannelConfig.ListNotes(r.Context(), r.URL.Query().Get("organization_id"), r.URL.Query().Get("channel_id"))
```

**Suggested fix:** Make ListNotes treat an empty channel_id as org-wide (use scopeFilter in the Mongo store and the prefix-aggregation branch in the memory store, mirroring ListDirectives), or have the handler/UI iterate channels.

**Fable verifier:** The notes page uses the generic loadScoped which sends only organization_id (index.html:1314-1331), MongoStore.ListNotes filters on exact channel_id "" (mongo.go:205-207, unlike scopeFilter/ListDirectives) and the memory store reads s.notes[org+"/"], while ProposeNote requires a non-empty channel, so GET /admin/api/notes always returns empty and the page can never show notes.

**Codex adversarial verdict: AGREE** — The UI's scoped loader supplies only organization_id, and listNotes passes the absent channel as an empty string (core/server/templates/index.html:1314-1331, core/server/server.go:727-734). Both repositories interpret that as an exact empty-channel scope instead of organization-wide aggregation (core/channelconfig/mongo.go:205-207, core/channelconfig/store.go:164-168), so ordinary channel notes never appear.

#### F22 — Overview 'Recent agent work' shows the oldest jobs, and tenant lists are unbounded oldest-first

- **Severity:** medium | **Category:** bug | **Location:** `core/server/templates/index.html:1975`
- **Status:** fixed (7456162)

jobs/deliveries/decisions ListOrganization (Mongo) sort created_at ascending with no limit, so /admin/api/jobs returns the entire per-org collection oldest-first. The dashboard heading says 'The latest admitted jobs and their outcome' but jobValues.slice(0,6) takes the first six rows, i.e. the six OLDEST jobs ever created. The jobs/decisions/deliveries data pages have the same problem: page 1 of the table shows the oldest records, and summary cards labeled 'in this result window' actually count all-time data. As the org accrues decisions (one per considered message) every page load ships the whole collection to the browser.

**Evidence excerpt (working tree, 2026-08-04):**
```go
for (const job of jobValues.slice(0,6)) {
```

**Suggested fix:** Sort created_at descending and add a SetLimit in the ListOrganization queries (or sort/limit in the server handlers before rendering), so 'recent' views actually show the newest records and payloads stay bounded.

**Fable verifier:** Jobs/deliveries/decisions Mongo ListOrganization all sort created_at ascending with no limit, the handlers pass rows through unmodified, and the overview panel headed 'The latest admitted jobs' takes jobValues.slice(0,6) with no client-side sort, so it shows the six oldest jobs and every page load ships the whole per-org collection.

**Codex adversarial verdict: AGREE** — Jobs, deliveries, and decisions are each listed without a limit and sorted oldest-first (core/jobs/mongo_queue.go:308-322, core/deliveries/mongo_queue.go:121-135, core/classifier/store.go:135-149). The overview then slices the first six records (core/server/templates/index.html:1972-1985), displaying the oldest entries while labelling them recent.

#### F23 — Unauthenticated /.status performs three unbounded full-collection scans per request

- **Severity:** medium | **Category:** inefficiency | **Location:** `core/server/server.go:242`
- **Status:** fixed (7456162)

The status handler calls Jobs.List, Deliveries.List, and Decisions.List — each a Find(bson.M{}) that decodes every document in the collection — only to compute len(...) for the counts map. /.status is outside the /admin prefix, so it bypasses bearer auth; any monitoring poller (or any client) triggers three full scans and full-document decodes per hit, and cost grows without bound with the decisions collection (one record per considered message).

**Evidence excerpt (working tree, 2026-08-04):**
```go
jobList, jobErr := s.deps.Jobs.List(r.Context())
	deliveryList, deliveryErr := s.deps.Deliveries.List(r.Context())
	decisionList, decisionErr := s.deps.Decisions.List(r.Context())
```

**Suggested fix:** Add Count methods (Mongo CountDocuments/EstimatedDocumentCount) to the three queues and use them in the status handler instead of listing and decoding everything.

**Fable verifier:** /.status is registered outside the /admin prefix so the authenticate middleware skips it, and the handler runs Jobs.List, Deliveries.List, and Decisions.List — each Find(bson.M{}) with full-document decode — just to compute len(), with the decisions collection absent from both the TTL indexes and the retention janitor so it grows one record per considered message.

**Codex adversarial verdict: AGREE** — Authentication bypasses every path outside /admin, including /.status (core/server/server.go:119-130, 199-213). That unauthenticated handler performs three unbounded collection reads just to calculate counts (core/server/server.go:241-260), creating an externally triggerable database-amplification path.

### Memory & intelligence

#### F4 — Curator permanently blocks relearning of forgotten memory scopes

- **Severity:** medium | **Category:** bug | **Location:** `core/memory/curator.go:112`
- **Status:** fixed (7456162)

Mongo Forget sets origin="operator" and status=forgotten while retaining scope_key/source_hash as the documented relearning tombstone (architecture.md:128: "materially changed source content may be learned again"). The curator's skip gate checks current.Origin == "operator" without checking Status, so after an operator forgets a channel- or thread-scope record, the curator skips that scope on every future run and durable memory generation for that channel/thread is silently disabled forever. Both PutGenerated implementations deliberately guard with `existing.Status == StatusActive && (existing.Pinned || existing.Origin == "operator")` (mongo.go:90, memory_store.go:82), confirming the curator gate diverged from the intended contract.

**Evidence excerpt (working tree, 2026-08-04):**
```go
if err == nil && (current.SourceHash == batch.SourceHash || current.Pinned || current.Origin == "operator") {
```

**Suggested fix:** Mirror the store semantics in the curator gate: skip only when current.SourceHash matches, or when current.Status == StatusActive and (current.Pinned || current.Origin == "operator").

**Fable verifier:** Verified Forget (mongo.go:165-179, memory_store.go:131-143) sets origin=operator with status=forgotten while FindScope has no status filter, so the curator gate at curator.go:112 skips the scope forever regardless of changed SourceHash, diverging from both PutGenerated guards (Status==StatusActive && ...) and the documented relearning tombstone in architecture.md:128; scoped impact keeps this medium rather than high.

**Codex adversarial verdict: AGREE** — The curator skips any existing operator-origin memory regardless of status or whether the new source hash differs (core/memory/curator.go:101-114). Forget changes the entry to operator-origin and retains its source hash while erasing content (core/memory/mongo.go:165-178), preventing the documented relearning of materially changed sources.

#### F35 — PutGenerated check-then-write race can overwrite operator-corrected memory

- **Severity:** medium | **Category:** bug | **Location:** `core/memory/mongo.go:113`
- **Status:** fixed (7456162)

PutGenerated reads the existing record via FindScope (line 88) to detect pinned/operator records, then performs an unguarded upsert whose filter is only {organization_id, scope_key}. An operator Correct or SetPinned that lands between the read and the write (management API runs concurrently with the curator goroutine, and multiple control-plane replicas widen the window) is silently replaced by model-generated content, violating the rule that operator memory is authoritative reviewed data. The revision counter can also regress since the write blindly $sets record.Revision computed from the stale read.

**Evidence excerpt (working tree, 2026-08-04):**
```go
err = s.db.Collection(models.CollectionSummaries).FindOneAndUpdate(ctx, bson.M{"organization_id": record.OrganizationID, "scope_key": record.ScopeKey}, update, options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)).Decode(&saved)
```

**Suggested fix:** Fold the guard into the update filter (e.g. add $nor: [{pinned: true}, {origin: "operator", status: "active"}, {source_hash: record.SourceHash}]) and treat no-match as changed=false, making the pinned/operator protection atomic.

**Fable verifier:** PutGenerated's pinned/operator guard is a Go-side check on a FindScope read (mongo.go:88-95) followed by an unguarded upsert filtered only on {organization_id, scope_key} whose $set of the full Record (non-omitempty pinned/origin/revision bson fields) would clobber a concurrent operator Correct/SetPinned with origin=model, pinned=false, and a stale revision, since the curator goroutine and management handlers share the store with no lock.

**Codex adversarial verdict: AGREE** — PutGenerated reads the current memory, computes a replacement, and performs an unconditional scope-key upsert (core/memory/mongo.go:79-117). A concurrent correction or pin between those operations can therefore be overwritten despite their operator authority (core/memory/mongo.go:120-162).

#### F36 — Projector issues 5 Mongo round trips per message, including 3 always-empty deletes on new messages

- **Severity:** medium | **Category:** inefficiency | **Location:** `core/intelligence/projector.go:92`
- **Status:** fixed (7456162)

Project runs for every observed Slack message (pipeline.go:660) and unconditionally executes DeleteMany on situation_facts, restricted_signals, and derivations, plus a CountDocuments session-only check and a message FindOne, before deciding anything. For ordinary new, non-incident messages (the overwhelming majority) the three deletes always match nothing, so every observed message pays 5 sequential Mongo round trips on the hot path. The delete-then-recreate step is only meaningful for mutation events (edit/delete) or replays of a previously projected observation.

**Evidence excerpt (working tree, 2026-08-04):**
```go
if _, err := p.db.Collection(models.CollectionSituationFacts).DeleteMany(ctx, sourceFilter); err != nil {
```

**Suggested fix:** Gate the three DeleteMany calls on mutation events (observation.MutationTargetTS != "" or delete/edit event types), keeping the upserts idempotent for plain new-message replays.

**Fable verifier:** projector.go:92-112 unconditionally issues three DeleteMany (always-empty for new messages: facts/signals keyed on a fresh message_ts and derivations keyed on the brand-new observation PublicID), a CountDocuments, and a FindOne before deciding anything, and pipeline.go:660 runs Project for every first-revision non-integration observed message on the classification critical path, so the waste is real and only needed for edit/delete/replay events.

**Codex adversarial verdict: AGREE** — Every projected message unconditionally deletes situation state, restricted situation state, and derivations before determining whether the message can produce new facts (core/intelligence/projector.go:83-118). This path runs for every authorized revision-one message (core/pipeline/pipeline.go:656-663), imposing multiple needless Mongo round trips on ordinary traffic.

#### F34 — MemoryStore projection/claim semantics silently diverged from MongoStore

- **Severity:** medium | **Category:** ai-slop | **Location:** `core/observer/memory_store.go:172`
- **Status:** fixed (7456162)

The memory test double and the Mongo production store implement different projection rules for the same events: on a newer event the memory store overwrites AuthorID/BotID when non-empty and preserves Subtype when the envelope's is empty, while the Mongo pipeline freezes author_id/bot_id forever via $ifNull and unconditionally overwrites subtype (clearing it to "") via choose(). MemoryStore.ClaimPending also omits the Mongo store's expires_at filter and orders by cross-organization OrganizationReceivedSeq instead of received_at. Tests exercising the memory store therefore validate behavior the production store does not have.

**Evidence excerpt (working tree, 2026-08-04):**
```go
if envelope.UserID != "" {
	current.AuthorID = envelope.UserID
}
...
if envelope.Subtype != "" {
	current.Subtype = envelope.Subtype
}
```

**Suggested fix:** Pick one projection semantics (Mongo's frozen-author/overwrite-subtype or memory's) and align both stores, and add the expires_at/ordering rules to MemoryStore.ClaimPending; a shared pure helper computing the projected document would prevent re-divergence.

**Fable verifier:** Verified memory_store.go:172-180 overwrites non-empty AuthorID/BotID and preserves empty Subtype on newer events while mongo_store.go:196-198 freezes author_id/bot_id via $ifNull and unconditionally overwrites subtype, and MemoryStore.ClaimPending (lines 100-126) omits Mongo's expires_at filter and received_at-first ordering (mongo_store.go:118-127), so the test double validates semantics production does not have.

**Codex adversarial verdict: AGREE** — The memory store ignores expiry during claims and orders by organization sequence, whereas Mongo filters expiry and sorts by receipt time plus sequence (core/observer/memory_store.go:100-126, core/observer/mongo_store.go:113-134). Projection semantics also diverge: Mongo freezes author/bot after insertion and can replace subtype with empty, while memory updates only nonempty values (core/observer/mongo_store.go:167-213, core/observer/memory_store.go:150-183).

### Core, config & audit

#### F5 — /tag-mode audit receipt is never persisted: missing RetentionEpoch, error swallowed

- **Severity:** medium | **Category:** bug | **Location:** `core/core.go:325`
- **Status:** fixed (7456162)

The participation-mode-change handler appends an audit receipt without a RetentionEpoch, but audit.MongoChain.Append (core/audit/mongo.go:41) rejects any request with an empty RetentionEpoch: 'organization, type, and retention epoch are required'. Every other Append call site in the repo passes time.Now().UTC().Format("2006-01"). Because the return is discarded with `_, _ =`, the append fails silently on every invocation, so operator mode changes via /tag-mode leave no audit trail at all — an unlogged security-relevant policy mutation. Additionally, the idempotency key embeds types.NewID("mode"), a fresh random ID per call, which makes the idempotency key pointless.

**Evidence excerpt (working tree, 2026-08-04):**
```go
_, _ = auditChain.Append(ctx, audit.AppendRequest{
	OrganizationID: request.OrganizationID, Type: "channel_policy.mode_command", ActorID: request.UserID,
	ResourceID: request.ChannelID, IdempotencyKey: "channel-mode/" + request.ChannelID + "/" + types.NewID("mode"),
```

**Suggested fix:** Add RetentionEpoch: time.Now().UTC().Format("2006-01") like all other call sites, log (redacted) on append failure instead of discarding the error, and build a deterministic idempotency key (e.g. channel + new revision) instead of a random ID.

**Fable verifier:** Verified the /tag-mode handler (core.go:325-329) omits RetentionEpoch, auditChain is a MongoChain (core.go:152) whose Append rejects empty RetentionEpoch (audit/mongo.go:41), and the error is discarded with '_, _ =', so mode-change receipts are never persisted and the random types.NewID idempotency key is real but moot; an unconditionally missing audit record with no functional or privilege bypass supports medium.

**Codex adversarial verdict: AGREE** — /tag-mode persists the policy before attempting an audit append, omits RetentionEpoch, and ignores the append error (core/core.go:309-329). Mongo audit validation rejects records without that field (core/audit/mongo.go:40-43), so successful mode changes consistently lack their intended audit receipt.

#### F38 — Audit content-commitment HMAC key is random per process and never persisted

- **Severity:** medium | **Category:** bug | **Location:** `core/core.go:148`
- **Status:** fixed (7456162)

core.New generates a fresh random 32-byte key on every startup and feeds it to audit.NewMongoChain. That key is used only for Chain.commit(), which produces the ContentCommitment stored on durable Mongo receipts (e.g., the Wiki execution receipt that 'commits the complete body' per the repo contract). Because the key is never persisted or configurable, no commitment can ever be re-verified after a restart, and multiple instances sharing the database commit under different keys — the commitment provides no verifiable binding, defeating its purpose while the chain hash (unkeyed sha256) is the only property that survives restarts.

**Evidence excerpt (working tree, 2026-08-04):**
```go
auditKey := make([]byte, 32)
if _, err := rand.Read(auditKey); err != nil {
	return nil, fmt.Errorf("create audit commitment key: %w", err)
}
auditChain, err := audit.NewMongoChain(db, auditKey)
```

**Suggested fix:** Source the audit commitment key from configuration/keystore (like Keystore.MasterKey) so commitments are verifiable across restarts and instances, or drop ContentCommitment if only intra-process integrity is intended.

**Fable verifier:** core.go:148-152 generates a random per-process key used only by Chain.commit for ContentCommitment on durable Mongo receipts (audit/mongo.go:69), no verification path or key persistence exists while Verify checks only the unkeyed chain hash, and the docs (README.md:630, SECURITY.md:139, architecture.md:369) describe commitments as binding the exact body — a property that is unverifiable after any restart, unlike the configured keystore MasterKey pattern.

**Codex adversarial verdict: AGREE** — Core generates a fresh random audit commitment key on every process construction (core/core.go:148-155), while content commitments are HMACs derived from that key (core/audit/audit.go:75-79, 146-169). Mongo verification checks receipt hashes and chain linkage but cannot reopen old content commitments after restart (core/audit/mongo.go:145-169), defeating durable commitment verification.

#### F39 — Three copy-pasted inline scope authorizers have silently diverged

- **Severity:** medium | **Category:** bug | **Location:** `core/core.go:142`
- **Status:** fixed (7456162)

The routines authorizer (lines 140-146), approvals authorizer (196-207), and triggers authorizer (345-353) each re-implement Resolve + Enrolled + KillSwitch + a 24h membership-freshness window inline, and the copies have diverged: the routines authorizer omits the WorkspaceEnabled, participation-mode, bot-membership, and output-channel-allowlist checks that the triggers authorizer enforces for the same kind of background job admission; the approvals authorizer checks WorkspaceEnabled but triggers does not. The 24h freshness constant is duplicated three times. Even if downstream delivery-time rechecks compensate, routines and triggers currently apply different authorization for equivalent background work, and future edits to one copy will not propagate.

**Evidence excerpt (working tree, 2026-08-04):**
```go
if err != nil || !policy.Enrolled || policy.KillSwitch || !policy.MembershipRefreshedAt.After(time.Now().UTC().Add(-24*time.Hour)) {
	return fmt.Errorf("routine scope denied")
}
```

**Suggested fix:** Extract one shared policy-authorization helper (fresh-membership + enrolled + kill-switch + workspace-enabled) with explicit per-caller additions (approver set, participation mode, output allowlist), and make routines/triggers use the same baseline.

**Fable verifier:** Verified the three inline authorizers diverge exactly as claimed (routines at core.go:140-146 lacks WorkspaceEnabled/participation/membership/allowlist, triggers at 345-353 lacks WorkspaceEnabled, approvals at 196-207 has it, 24h constant duplicated), and while delivery-time authorizedOutputPolicy (pipeline.go:1251) prevents output leakage, a routine on an observe-mode channel still enqueues and executes full-agent work whose delivery is then denied, so the divergence is incoherent drift rather than deliberate design.

**Codex adversarial verdict: ADJUST** — The claimed WorkspaceEnabled divergence is false because Resolve folds a disabled workspace into KillSwitch; the approval check is merely redundant (core/orgconfig/store.go:419-439, core/core.go:196-207). The real issue: routine jobs are rechecked for participation and membership before execution, but unlike triggers their output allowlist is checked only at progress/delivery time, after worker execution (core/pipeline/pipeline.go:1446-1482, 1584-1585, 1693-1699).

#### F40 — socket_mode validation does not require slack.botUserId though live behavior silently degrades without it

- **Severity:** medium | **Category:** bug | **Location:** `core/config/config.go:466`
- **Status:** fixed (7456162)

Validate enforces OrganizationID, an 'A'-prefixed AppID, TeamID, xapp/xoxb token prefixes, and even the optional xoxp prefix for socket_mode, but never checks BotUserID. With BotUserID empty, live ingress silently disables mention detection (core/slack/live.go:857: `base.IsMention = botUserID != "" && strings.Contains(base.Text, "<@"+botUserID+">")`) and drops all bot-membership change events (live.go:517/529), so Tag never responds to mentions and never derives membership-based assist. runtime.env.example lists TAG__SLACK__BOT_USER_ID alongside the required live variables. Given this package's stated fail-closed startup-validation purpose, a missing BotUserID should fail startup rather than yield a silently inert live deployment.

**Evidence excerpt (working tree, 2026-08-04):**
```go
if cfg.Slack.OrganizationID == "" || !strings.HasPrefix(cfg.Slack.AppID, "A") || cfg.Slack.TeamID == "" {
	return fmt.Errorf("Slack organizationId, appId, and teamId are required for socket_mode")
}
```

**Suggested fix:** In the socket_mode case, require a non-empty (U/W-prefixed) cfg.Slack.BotUserID, or resolve it via auth.test at startup and fail if unavailable.

**Fable verifier:** config.go:462-474 validates every other socket_mode field but not BotUserID, and with it empty live.go:857 disables message-event mention detection (making mentions race-dependent since canonicalMessageEventID dedupes the app_mention and message callbacks) and live.go:517-529 drop all bot membership events with no runtime auth.test fallback — silent degradation the strict fail-closed validator should reject, though the finding overstates impact because the app_mention case (live.go:838) still sets IsMention unconditionally and context-sync conversations.list reconciliation works without BotUserID.

**Codex adversarial verdict: ADJUST** — Socket-mode validation does omit BotUserID (core/config/config.go:462-474), which disables generic-message mention recognition and membership processing (core/slack/live.go:517-530, 857). However, app_mention events are explicitly marked as mentions regardless of that ID (core/slack/live.go:835-840), so the deployment is degraded rather than wholly unable to answer mentions; requiring or resolving the bot ID remains the correct fix.

#### F31 — Observation hot-path queries (ClaimPending, CompleteDecision, MarkOutput) are unindexed

- **Severity:** medium | **Category:** inefficiency | **Location:** `core/observer/mongo_store.go:118`
- **Status:** fixed (7456162)

ClaimPending filters on {expires_at, decision_state/decision_lease_expires_at} with a sort on {received_at, organization_received_seq}; the only decision index is decision_claim {organization_id, decision_state, decision_lease_expires_at} (database.go:120) whose organization_id prefix is absent from the filter, and neither received_at nor organization_received_seq is indexed, so each 250ms decision poll scans and sorts the whole observations collection (which retains every observed Slack message for the retention window). CompleteDecision (line 142), MarkOutput (line 293), and SetRestricted (line 218) filter on public_id, which also has no index on observations.

**Evidence excerpt (working tree, 2026-08-04):**
```go
filter := bson.M{
	"expires_at": bson.M{"$gt": now},
	"$or": bson.A{
		bson.M{"decision_state": "pending"},
		bson.M{"decision_state": "processing", "decision_lease_expires_at": bson.M{"$lte": now}},
```

**Suggested fix:** Add observations indexes matching the actual queries: a unique public_id index and an org-less {decision_state, decision_lease_expires_at, received_at} claim index (or include organization_id in the ClaimPending filter/iteration).

**Fable verifier:** Verified ClaimPending (mongo_store.go:113-135) filters without organization_id so the org-prefixed decision_claim index (database.go:120) cannot serve it, sorts on unindexed received_at/organization_received_seq, is polled every 250ms (pipeline.go:545-560), and CompleteDecision/SetRestricted/MarkOutput all filter on public_id which has no observations index.

**Codex adversarial verdict: ADJUST** — The claim query is not wholly unindexed because expires_at has a TTL index (core/observer/mongo_store.go:113-134, core/database/database.go:118-121). It still lacks a compound index matching decision state, lease, expiry, and sort order, while several mutation paths query unindexed public_id (core/observer/mongo_store.go:137-153, 216-232, 292-303), so the underlying MEDIUM inefficiency remains.

---

## P2 — Low severity (56 findings, Fable-verified; not Codex-reviewed except F20)

### F20 — listBotMembership enumerates all workspace public channels and hard-fails startup past MaxChannels

- **Severity:** medium | **Category:** bug | **Location:** `core/slack/context_sync.go:276`
- **Status:** fixed (7456162)

conversations.list with types public_channel returns every non-archived public channel in the workspace regardless of bot membership, and listBotMembership counts them all against ContextSyncMaxChannels (default 500). In a workspace with more than 500 public channels, Discover errors, which aborts Core.Start entirely (core.go:557-561) and fails every 5-minute refresh tick. The full-workspace enumeration buys nothing: contextChannel reads `botMembership[channel.ID]` where a missing key already means false, so only bot-member channels need listing. It also re-paginates the entire workspace channel list every 5 minutes.

**Evidence excerpt (working tree, 2026-08-04):**
```go
page, next, callErr := s.botAPI.GetConversationsContext(ctx, &slackapi.GetConversationsParameters{
	Cursor: cursor, Types: []string{"public_channel", "private_channel"}, Limit: limit, ExcludeArchived: true, TeamID: s.options.TeamID,
})
```

**Suggested fix:** List only the bot's memberships via users.conversations with the bot token (identical resulting map semantics since missing entries default to false), which shrinks both the API workload and the MaxChannels failure domain to channels the bot actually joined.

**Fable verifier:** listBotMembership's bot-token conversations.list with public_channel returns every non-archived public workspace channel regardless of membership, counts them against the 500-channel default and hard-fails Discover which aborts Core.Start (core.go:557-561) and every 5-minute refresh, while contextChannel only reads botMembership[channel.ID] where a missing key already means false, so a members-only users.conversations listing would be semantically identical and far smaller.

**Codex adversarial verdict: ADJUST (downgraded to low)** — Rejecting a truncated inventory is deliberate fail-closed behavior: Discover promises a complete bounded inventory, and the test suite explicitly rejects pagination beyond the configured bound (core/slack/context_sync.go:141-145, context_sync_test.go:429-440). The workspace-wide bot conversations.list remains an avoidable scalability cost (core/slack/context_sync.go:266-293), but the fix should change the membership source, not weaken the defensive failure.

### F11 — reconcileJobs scans the entire jobs collection every 250ms poll tick

- **Severity:** low | **Category:** inefficiency | **Location:** `core/pipeline/pipeline.go:1293`
- **Status:** fixed (7456162)

reconcileJobLoop ticks at Config.Jobs.Poll (default 250ms per config.go) and each tick calls Jobs.List(ctx), which is implemented in MongoQueue as an unfiltered Find(bson.M{}) that decodes every job document — including all terminal succeeded/failed/cancelled jobs retained for audit — into memory. Reconciliation only cares about four narrow states (waiting_approval, expired queued, needs_reconciliation, ripe retry_wait), so this is an ever-growing full-collection scan and decode roughly four times per second.

**Evidence excerpt (working tree, 2026-08-04):**
```go
all, err := p.deps.Jobs.List(ctx)
// MongoQueue: func (q *MongoQueue) List(ctx context.Context) ([]Job, error) { return q.list(ctx, bson.M{}) }
```

**Suggested fix:** Add a queue method that filters by the reconcilable states (state $in [waiting_approval, queued, needs_reconciliation, retry_wait]) server-side, and/or lengthen the reconcile interval independently of the hot claim poll.

**Fable verifier:** Confirmed reconcileJobLoop ticks at the 250ms Jobs.Poll default and reconcileJobs calls Jobs.List which is Find(bson.M{}) decoding every job when only four states matter, but the "ever-growing" claim is refuted by the job_expiry TTL index (database.go:136) and 24h-bounded job expiries, so this is a real but bounded inefficiency.

### F12 — Policy corrections hardcode reactions that can violate the configured allowlist

- **Severity:** low | **Category:** bug | **Location:** `core/classifier/openai_classifier.go:674`
- **Status:** fixed (7456162)

Many post-decode policy corrections hardcode reaction names (speech_balloon at line 394, thinking_face at 414/460/941, eyes at 738, warning at 1080/1251, white_check_mark at 1100/1111) that are never checked against the operator-configurable allowlist. validateRecommendation then rejects any non-allowlisted reaction, failing the whole classification. The config path (config.go:405 cfg.ReactionEmojis = splitNonEmpty(raw)) lets an operator narrow the list, and NewOpenAIClassifier only validates emoji-name shape, so a narrowed allowlist turns every policy-corrected decision into a classifier error: ambient traffic goes silent and mentions drop to the generic fallback.

**Evidence excerpt (working tree, 2026-08-04):**
```go
if !slices.Contains(reactions, decision.Reaction) {
	return errors.New("classification reaction is not allowlisted")
}
```

**Suggested fix:** Have corrections pick reactions via a helper that falls back to an allowlisted emoji (like withDefaultReaction does), or require the seven semantic emojis to be present at construction time.

**Fable verifier:** Verified the corrections hardcode speech_balloon/thinking_face/eyes/warning/white_check_mark before validateRecommendation:674 rejects non-allowlisted reactions, and TAG__CLASSIFIER__REACTION_EMOJIS (config.go:404) can narrow the list with only shape validation at construction, causing ambient silence and generic mention fallback (service.go:238-259); however it cannot manifest under checked-in defaults and degrades to designed fail-closed fallbacks, so severity is lowered.

### F14 — Social-reply selection implemented three times with diverged behavior

- **Severity:** low | **Category:** ai-slop | **Location:** `core/classifier/openai_classifier.go:1109`
- **Status:** fixed (7456162)

Social direct-reply text/reaction mapping exists in three places: directSocialFallback (service.go:545) with a full greeting/farewell/thanks mapping, the top-level branch of withAddressedSocialPolicyCorrections (lines 1129-1152) with a partial mapping, and the active-thread branch which hardcodes 'Happy to help!' with white_check_mark for every social message. That diverges behaviorally: a greeting like 'morning tag' in an active thread gets 'Happy to help!' plus white_check_mark, while the classifier instructions in this same file mandate speech_balloon for greetings and the other two implementations reply 'Morning!'.

**Evidence excerpt (working tree, 2026-08-04):**
```go
DirectReply:        "Happy to help!",
...
Reaction:           "white_check_mark",
```

**Suggested fix:** Replace both branches of withAddressedSocialPolicyCorrections with calls to directSocialFallback (passing activeThread) so one mapping owns social replies.

**Fable verifier:** Verified all three diverged implementations: directSocialFallback (service.go:545-566) maps "morning" to "Morning!"+speech_balloon, the addressed-channel branch (openai_classifier.go:1129-1152) has a partial mapping, and the active-thread branch (1104-1113) hardcodes "Happy to help!"+white_check_mark for every social message lacking a provider direct reply; the divergence is real but cosmetic, so severity is lowered.

### F17 — MongoQueue never abandons attempts-exhausted deliveries, diverging from MemoryQueue

- **Severity:** low | **Category:** bug | **Location:** `core/deliveries/mongo_queue.go:55`
- **Status:** fixed (7456162)

MongoQueue.Claim's filter excludes records with attempt >= max_attempts via $expr $lt, but nothing ever transitions such records to abandoned: the lease-expiry sweep on line 51 resets them to pending, and Retry only abandons while a lease is held. If a worker crashes after claiming a final attempt, the delivery is stranded as pending until expires_at, never delivered nor marked abandoned, and pipeline never calls MarkCompletedUndelivered for its job. MemoryQueue.Claim (queue.go:134-138) and the sibling jobs.MongoQueue.ReleaseRetryWait both explicitly set attempts_exhausted, so the two Queue implementations observably diverge.

**Evidence excerpt (working tree, 2026-08-04):**
```go
"$expr": bson.M{"$lt": []any{"$attempt", "$max_attempts"}}
```

**Suggested fix:** In Claim (or the pre-claim sweep), add an UpdateMany that transitions pending/retry_wait records with attempt >= max_attempts to abandoned with failure_reason attempts_exhausted, matching MemoryQueue semantics.

**Fable verifier:** MongoQueue.Claim's $lt attempt filter plus the pending-reset sweep strand a lease-lost final-attempt delivery as pending until TTL deletion while MemoryQueue.Claim (queue.go:134-138) explicitly abandons it with attempts_exhausted; impact is limited to status divergence since attempts were already exhausted, the TTL janitor cleans the record, and even the memory path skips MarkCompletedUndelivered for claim-time exhaustion.

### F27 — Terminate cancels walltime context first, so the watchdog SIGKILLs before the graceful SIGTERM window

- **Severity:** low | **Category:** bug | **Location:** `core/workers/local.go:263`
- **Status:** fixed (7456162)

Terminate calls process.cancel() before sending SIGTERM and waiting 500ms to escalate. But the watchdog goroutine started in provision() reacts to ctx.Done() by immediately sending SIGKILL to the whole process group. Cancelling the context therefore hard-kills the Codex App Server group right away, defeating the intended close-stdin -> SIGTERM -> 500ms grace -> SIGKILL sequence; the SIGTERM, the 500ms wait, and the escalation SIGKILL at line 274 are effectively dead logic that only wins a race in rare scheduling. The App Server process never gets a chance to exit cleanly.

**Evidence excerpt (working tree, 2026-08-04):**
```go
process.cancel()
...
_ = syscall.Kill(-process.command.Process.Pid, syscall.SIGTERM)
// watchdog in provision():
case <-ctx.Done():
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
```

**Suggested fix:** Move process.cancel() to after the graceful wait (or have the watchdog select on a dedicated timeout timer rather than ctx.Done()) so SIGTERM plus the 500ms grace period actually runs before SIGKILL.

**Fable verifier:** Terminate (local.go:263) cancels the walltime context whose watchdog (local.go:171-177) immediately SIGKILLs the process group, so the SIGTERM plus 500ms grace at lines 270-274 is effectively dead; however cleanup still completes via process.done and workers are disposable by design, limiting real impact.

### F29 — marketplace readRootFile silently truncates oversize files — diverged copy of tools.readRootFile

- **Severity:** low | **Category:** ai-slop | **Location:** `core/marketplace/marketplace.go:346`
- **Status:** fixed (7456162)

This readRootFile reads limit+1 bytes but never checks whether the limit was exceeded, so the vestigial +1 does nothing and oversize files are silently truncated instead of rejected. Its sibling copy in core/tools/tools.go:336 has the missing check ('if int64(len(data)) > limit { return nil, errors... }') — a classic silently-diverged copy-paste pair. Consequences: a catalog over 1MB surfaces as a confusing JSON syntax error rather than a size error, and a skill file that grows between the WalkDir size accounting and the hash read gets hashed over truncated content, producing a wrong snapshot hash that only fails much later at worker materialization.

**Evidence excerpt (working tree, 2026-08-04):**
```go
return io.ReadAll(io.LimitReader(file, limit+1))
```

**Suggested fix:** Add the same oversize rejection as tools.readRootFile (error when len(data) > limit), or better, share one helper between the two packages.

**Fable verifier:** marketplace.go:335-347 reads limit+1 bytes but never rejects oversize content while its sibling tools.go:336-355 does, a genuine silently-diverged copy; consequences are confined to a confusing JSON error for oversize catalogs and a narrow grow-between-walk-and-hash window that materializeSkills' own hash check (local.go:379) catches later.

### F33 — jobs.Operations (Cancel/Interrupt/Restart/Branch) has no production callers

- **Severity:** low | **Category:** dead-code | **Location:** `core/jobs/operations.go:11`
- **Status:** fixed (7456162)

The Operations wrapper is referenced only by operations_test.go — no server route or pipeline path constructs it (rg across the repo finds zero non-test uses), and Queue.Interrupt in both queue implementations is a pure alias of Cancel that only Operations calls. The dead Restart also carries latent bugs waiting for a future caller: it copies current.ExpiresAt into the new spec, so restarting a job whose expiry passed enqueues a job Claim can never pick up (Claim requires expires_at > now), and it silently drops RequesterID, losing the progress-recipient/notice addressing.

**Evidence excerpt (working tree, 2026-08-04):**
```go
type Operations struct {
	Queue    Queue
	Sessions sessions.Store
}
```

**Suggested fix:** Delete operations.go and the Interrupt interface method until a management surface actually needs them; if kept, reset ExpiresAt and carry RequesterID in Restart/Branch.

**Fable verifier:** rg confirms Operations is constructed only in operations_test.go while pipeline/approvals call Queue.Cancel directly, Interrupt is a Cancel alias with no other callers, and Restart verifiably copies a possibly-expired ExpiresAt that Claim's expires_at>now filter would never pick up and drops Spec.RequesterID — but none of this code can execute in production, so impact is maintenance-only.

### F37 — conversationalsearch package has no production callers

- **Severity:** low | **Category:** dead-code | **Location:** `core/conversationalsearch/search.go:50`
- **Status:** fixed (7456162)

A repo-wide search shows the only references to the conversationalsearch package outside its own directory are none; the sole consumer is its own search_test.go. The pipeline's cross-channel context path calls Observations.Recent directly (pipeline.go:1037) and does not use Searcher. This is a complete unused subsystem (~140 lines including audience-intersection and staleness policy logic) that must be kept correct and privacy-reviewed despite never executing.

**Evidence excerpt (working tree, 2026-08-04):**
```go
func (s *Searcher) Search(ctx context.Context, request Request) ([]Result, error) {
```

**Suggested fix:** Either wire Searcher into the production search path it was designed for, or delete the package until that feature lands.

**Fable verifier:** rg shows the conversationalsearch package is referenced only by its own search_test.go, the pipeline's cross-channel context path calls Observations.Recent directly, and no documentation designates it as staged deliberate design — an entire unused subsystem, but one that cannot execute, so severity is cleanup-level.

### F42 — Memory store fails open on missing workspace where Mongo fails closed

- **Severity:** low | **Category:** bug | **Location:** `core/orgconfig/store.go:118`
- **Status:** fixed (7456162)

Memory.Resolve and Memory.ListChannels default a missing workspace to WorkspaceEnabled=true (no kill switch), while the production Mongo.Resolve returns ErrNotFound and Mongo.ListChannels sets WorkspaceEnabled=false and forces KillSwitch=true for the same condition. Since the Memory store is the test double substituted for Mongo (per the repo's own rule that memory stores sit behind the same interfaces), tests exercising workspace-gating semantics validate fail-open behavior that production denies, which can mask fail-closed regressions.

**Evidence excerpt (working tree, 2026-08-04):**
```go
if workspace, exists := s.workspaces[org+"/"+team]; exists {
	value.WorkspaceEnabled = workspace.Enabled
	value.KillSwitch = value.KillSwitch || !workspace.Enabled
} else {
	value.WorkspaceEnabled = true
}
```

**Suggested fix:** Make Memory.Resolve return ErrNotFound when the workspace record is absent and make Memory.ListChannels mark the channel kill-switched, mirroring the Mongo semantics.

**Fable verifier:** Verified Memory.Resolve/ListChannels default a missing workspace to WorkspaceEnabled=true while Mongo.Resolve returns ErrNotFound and Mongo.ListChannels forces kill-switch, a real fail-open parity break, but orgconfig.NewMemory is used only in tests (core.go wires NewMongo), so severity drops to low.

### F43 — Memory ActivateDirective wipes the active directive on a failed activation

- **Severity:** low | **Category:** bug | **Location:** `core/channelconfig/store.go:120`
- **Status:** fixed (7456162)

The in-memory ActivateDirective mutates every revision's Active flag before checking whether the requested revision exists. When revisionID does not match any revision it returns an error, but by then all Active flags have already been cleared, so a failed activation destroys the currently active directive. The Mongo implementation validates the revision first and leaves the projection untouched on failure, so the two Repository implementations diverge and the error path corrupts store state.

**Evidence excerpt (working tree, 2026-08-04):**
```go
for index := range revisions {
	revisions[index].Active = revisions[index].ID == revisionID
	if revisions[index].Active {
		found = index
	}
}
if found < 0 {
	return DirectiveRevision{}, errors.New("directive revision not found")
}
```

**Suggested fix:** Locate the target revision first and return the not-found error before mutating any Active flags; only rewrite flags once the revision is confirmed to exist.

**Fable verifier:** Verified the memory ActivateDirective mutates every revision's Active flag on the shared backing array before the found<0 check so an unknown revisionID wipes the active directive, diverging from the validate-first Mongo store; channelconfig.NewStore is test-only, so severity drops to low.

### F44 — Audit CAS retry can double-record key-less receipts under contention

- **Severity:** low | **Category:** bug | **Location:** `core/audit/mongo.go:94`
- **Status:** fixed (7456162)

In MongoChain.Append, when writer A inserts receipt N+1 but loses the head CAS, the only success recovery is the exact state where A's receipt is the current head. If a concurrent writer B adopted A's receipt via recoverNext and then appended its own receipt (head now N+2, different hash), A's check fails, the loop continues, and A inserts a second receipt for the same AppendRequest at N+3. The idempotency-key unique index catches this for keyed appends, but server.auditMutation (core/server/server.go:772) appends management-mutation receipts with no IdempotencyKey, so those events can be durably recorded twice. The same happens when the post-CAS head re-read returns a transient error (headErr != nil falls through to continue).

**Evidence excerpt (working tree, 2026-08-04):**
```go
current, headErr := c.head(ctx, request.OrganizationID)
if headErr == nil && current.Sequence == receipt.Sequence && current.Hash == receipt.Hash {
	return receipt, nil
}
```

**Suggested fix:** After a lost CAS, check whether a receipt with this attempt's ID already exists in the chain (FindOne by public_id) and return it as success instead of relying solely on the head snapshot; or require an IdempotencyKey on every Append.

**Fable verifier:** Verified the lost-CAS recovery only detects adoption when the head is exactly this receipt, Receipt.IdempotencyKey is omitempty so key-less receipts escape the partial unique index, and server.auditMutation (server.go:772) appends without a key, so the adopt-then-advance interleaving or a transient head re-read error durably records the same event twice; the race is very narrow and the duplicate leaves the chain valid, so severity drops to low.

### F45 — routines and triggers packages are diverged near-duplicates

- **Severity:** low | **Category:** ai-slop | **Location:** `core/routines/routines.go:161`
- **Status:** fixed (7456162)

routines.Scheduler/Store/MongoStore/Service are near-verbatim copies of the triggers equivalents and have silently diverged: on authorizer denial routines ignores the AdvanceContext error (`_ =`, so a persistent advance failure re-runs the denied routine every poll tick forever) while triggers returns it (triggers.go:239-241); routines.MongoStore.DueContext calls SetLimit unconditionally while triggers guards `limit > 0` (triggers/mongo.go:77); routines' due-sort has no ID tiebreaker while triggers' does (triggers.go:180-187); and the Service Start/Stop type is duplicated byte-for-byte (routines.go:198-241 vs triggers.go:270-315). Copy-paste divergence in scheduler semantics is exactly where subtle behavior drift originates.

**Evidence excerpt (working tree, 2026-08-04):**
```go
if err := s.authorizer.AuthorizeRoutine(ctx, routine); err != nil {
	_ = s.store.AdvanceContext(ctx, routine.OrganizationID, routine.ID, now)
	continue
}
```

**Suggested fix:** Extract the shared Service poll-loop and the RunDue advance/enqueue skeleton into one helper (or merge the two packages behind a common scheduled-work core), and make the error-handling semantics identical.

**Fable verifier:** Re-verified every cited divergence: routines.go:161 discards the AdvanceContext error where triggers.go:239-241 returns it, routines/mongo.go:36 calls SetLimit unconditionally vs the limit>0 guard in triggers/mongo.go, routines' due sort lacks the ID tiebreaker, and the Service type is byte-identical; however no divergence misbehaves on a live path (RunDue always passes limit 100) and the two packages are deliberately separate components, so this is a low-severity maintainability finding.

### F46 — In-memory routines Store keys by ID only, allowing cross-organization overwrite

- **Severity:** low | **Category:** bug | **Location:** `core/routines/routines.go:77`
- **Status:** fixed (7456162)

routines.Store keys its map by r.ID alone, so Put with the same public ID from a different organization silently replaces the other tenant's routine (inheriting its Version/CreatedAt). The sibling triggers.Store correctly namespaces with organizationID+"/"+id. The repo contract requires memory stores to sit behind the same interfaces as Mongo consumers with equivalent semantics; the Mongo store scopes every filter by organization_id, so this memory implementation breaks tenant-scoping parity.

**Evidence excerpt (working tree, 2026-08-04):**
```go
if old, ok := s.routines[r.ID]; ok {
	r.Version = old.Version + 1
	r.CreatedAt = old.CreatedAt
}
```

**Suggested fix:** Key the map by r.OrganizationID + "/" + r.ID (matching triggers.Store) and update Due/Advance/List lookups accordingly.

**Fable verifier:** Verified routines.Store.Put keys the map by r.ID alone while triggers.Store uses organizationID+"/"+id and the Mongo store filters by organization_id, a real tenant-scoping parity break, but routines.NewStore appears only in routines_test.go (core.go:138 wires NewMongoStore), so severity drops to low.

### F47 — Retention sweep issues one DeleteOne round-trip per expired derivation link

- **Severity:** low | **Category:** inefficiency | **Location:** `core/retention/janitor.go:123`
- **Status:** fixed (7456162)

sweep loads every expired derivation link into memory with an unbounded cursor.All, then deletes each derived document with an individual DeleteOne inside the loop. After downtime or a bulk expiry there can be thousands of links, producing thousands of sequential Mongo round-trips per sweep and unbounded slice growth. The links are already grouped by a small allowlisted set of collections, so batch deletion is straightforward.

**Evidence excerpt (working tree, 2026-08-04):**
```go
result, err := j.db.Collection(link.DerivedCollection).DeleteOne(ctx, bson.M{"public_id": link.DerivedID})
```

**Suggested fix:** Group expired link IDs by DerivedCollection and issue one DeleteMany with {"public_id": {"$in": ids}} per collection, and bound the Find with a limit so each sweep processes a fixed batch.

**Fable verifier:** Verified janitor.sweep loads all expired derivation links with an unlimited Find/cursor.All and issues one DeleteOne per link inside the loop where a per-collection DeleteMany with $in would suffice, but the sweep runs on a background interval off any latency-sensitive path, so severity drops to low.

### F48 — WriterActive declared on Delivery but writer_active is a jobs-collection field

- **Severity:** low | **Category:** bug | **Location:** `models/models.go:326`
- **Status:** fixed (7456162)

All writer_active reads/writes happen on the jobs collection: core/jobs/mongo_queue.go sets "writer_active" via raw bson.M updates (lines 81, 87, 115, 156, 194, 213) and core/database/database.go:135 builds the session_generation_writer_unique partial index on CollectionJobs filtered by {writer_active: true}. Yet the struct field sits on models.Delivery, and models.Job has no WriterActive at all. Consequences: models.Job misrepresents the real job schema (a decode silently drops the flag, so any future full-document Job write would strip it and break the single-writer unique-index semantics), and every inserted Delivery document persists a spurious writer_active:false that nothing ever reads.

**Evidence excerpt (working tree, 2026-08-04):**
```go
type Delivery struct {
	...
	Version        int64         `bson:"version"`
	WriterActive   bool          `bson:"writer_active"`
}
```

**Suggested fix:** Move WriterActive to models.Job (where the queue and index actually use it) and delete it from models.Delivery.

**Fable verifier:** Verified via rg that every writer_active read/write is a raw bson update on the jobs collection and the session_generation_writer_unique partial index is on CollectionJobs, while models.Job lacks the field and models.Delivery persists a dead writer_active:false on every insert (deliveries/mongo_queue.go:35); since no current code round-trips the flag through the struct, this is a latent schema-misrepresentation defect rather than an active bug, so severity drops to low.

### F49 — evalProfiles is a silently diverged copy of core.defaultResponseProfiles

- **Severity:** low | **Category:** ai-slop | **Location:** `evals/live.go:267`
- **Status:** fixed (7456162)

evalProfiles duplicates the profile-construction loop in core/core.go:377-403 (defaultResponseProfiles) but has already drifted: the production version sets RequiredCapabilities: []string{"structured"} and AllowedDataClasses: []string{"internal"} on every profile, while the eval copy omits both. The live eval therefore advertises agent profiles to the OpenAI classifier that do not match the deployed profile definitions, and any future change to the profile set must be made in two places, only one of which will get it.

**Evidence excerpt (working tree, 2026-08-04):**
```go
{cfg.FastProfileBase + "-low", cfg.FastModel, "low", "light"},
{cfg.FastProfileBase + "-medium", cfg.FastModel, "medium", "standard"},
{cfg.DefaultProfile, cfg.DefaultModel, cfg.DefaultVariant, "strong"},
```

**Suggested fix:** Export the core helper (or move it to a shared package) and have evals/live.go call it instead of maintaining a copy.

**Fable verifier:** evalProfiles (evals/live.go:267) is a verified near-verbatim copy of core.defaultResponseProfiles (core/core.go:377) already missing RequiredCapabilities/AllowedDataClasses, though the drift is behaviorally inert because advertisedProfiles only reads ID/provider/model/strength/variant, so the advertised profiles are identical.

### F50 — Fixture-contract checking duplicated between Run and RunLive and already divergent

- **Severity:** low | **Category:** ai-slop | **Location:** `evals/eval.go:95`
- **Status:** open

The seven per-fixture contract checks (WantReleasableEvidence, WantRestrictedSafeBlock, WantDirectReply, WantFullAgent, WantSourceWriteRedirect, WantProductRetrieval, ForbidSourceRedirect) plus the speak/silence/placement accounting are duplicated nearly verbatim between eval.go lines 95-145 and live.go lines 99-192. They have already diverged: the live copy gained optionalNoReply handling and per-contract failure labels, while the deterministic copy reports only predicted/effective outcomes on contract failure, hiding which contract failed. Every new fixture contract must now be added twice.

**Evidence excerpt (working tree, 2026-08-04):**
```go
if fixture.WantSourceWriteRedirect && (!result.Predicted.SourceWriteRequested || !strings.Contains(result.Predicted.DirectReply, "Linear bug") || !strings.Contains(result.Predicted.DirectReply, "Linear feature")) {
```

**Suggested fix:** Extract a shared helper, e.g. checkFixtureContracts(fixture, decision, allowedPredicted) []string, that returns failure labels; the deterministic scorer passes single-element outcome slices.

**Fable verifier:** The seven per-fixture contract checks are duplicated near-verbatim between evals/eval.go:95-127 and evals/live.go:99-137 in the same package, with the live copy alone carrying optionalNoReply tolerance and per-contract failure labels while the deterministic copy hides which contract failed.

### F52 — boundedActivityText byte-slices UTF-8 and can emit an invalid rune

- **Severity:** low | **Category:** bug | **Location:** `core/pipeline/pipeline.go:2360`
- **Status:** fixed (7456162)

boundedActivityText truncates with value[:maximum-1] on byte indices. Slack message text routinely contains multi-byte runes (emoji, accented names), so the slice can cut a rune in half, producing invalid UTF-8 that is then published to the tenant activity feed (JSON marshaling mangles it into a replacement character). The 600 "characters" bound is also actually a byte bound.

**Evidence excerpt (working tree, 2026-08-04):**
```go
if len(value) <= maximum {
	return value
}
return strings.TrimSpace(value[:maximum-1]) + "…"
```

**Suggested fix:** Truncate on rune boundaries, e.g. convert to []rune (or walk with utf8.DecodeRuneInString) before slicing, so the excerpt is always valid UTF-8.

**Fable verifier:** boundedActivityText (pipeline.go:2355-2361) slices on byte indices, so a >600-byte Slack message with a multi-byte rune straddling byte 599 emits invalid UTF-8 into the unrestricted activity-feed excerpt (mangled to U+FFFD on JSON encoding), and the 600 bound is bytes not characters.

### F53 — safeToolProgressStep has no production caller and lifecycle helper carries vestigial params

- **Severity:** low | **Category:** dead-code | **Location:** `core/pipeline/pipeline.go:2011`
- **Status:** open

safeToolProgressStep is referenced only from pipeline_test.go — no production code calls it. Relatedly, safeToolProgressLifecycleStep declares its fourth parameter as _ (the call site at line 1868 still extracts call_id from event data just to pass it in and it is ignored), and safeSkillProgressStep's Complete/Error title branches are unreachable because its only caller passes SlackProgressInProgress. These are vestigial surfaces left from an earlier multi-step progress design.

**Evidence excerpt (working tree, 2026-08-04):**
```go
func safeToolProgressStep(toolID, operationID, resourceAction string) types.SlackProgressStep {
	return safeToolProgressLifecycleStep(toolID, operationID, resourceAction, "", types.SlackProgressComplete)
}
```

**Suggested fix:** Delete safeToolProgressStep (point the test at safeToolProgressLifecycleStep), drop the ignored callID parameter and its extraction at the call site, and remove the unreachable branches in safeSkillProgressStep.

**Fable verifier:** Verified safeToolProgressStep is referenced only from pipeline_test.go:669, safeToolProgressLifecycleStep's fourth parameter is declared _ while line 1860 extracts call_id solely to pass it, and safeSkillProgressStep's Complete/Error branches are unreachable because its only caller (line 1835) passes SlackProgressInProgress.

### F54 — Self-authored and integration-authored import blocks are near-identical copies

- **Severity:** low | **Category:** ai-slop | **Location:** `core/pipeline/pipeline.go:238`
- **Status:** open

HandleEnvelope contains two 12-line blocks (lines 220-232 and 238-250) that perform exactly the same sequence — Observations.Import, advanceContextSyncWatermark, two error logs, a debug log, and an identical AcceptResult{Duplicate, Ignored: true, ResolvedContext: true} return — differing only in log wording. Copy-paste blocks like this tend to diverge silently (the two error messages already differ only by one word). A shared helper taking the log label would keep the fail-closed behavior in one place.

**Evidence excerpt (working tree, 2026-08-04):**
```go
if envelope.IntegrationAuthored() {
	accepted, err := p.deps.Observations.Import(ctx, envelope)
	...
	return slack.AcceptResult{Duplicate: accepted.Duplicate, Ignored: true, ResolvedContext: true}, nil
```

**Suggested fix:** Extract a single importResolvedContext(ctx, envelope, label string) helper used by both the self-authored and integration-authored branches.

**Fable verifier:** Lines 220-231 and 238-249 of HandleEnvelope are structurally identical Import/watermark/log/AcceptResult blocks differing only in log labels, a verified copy-paste pair that a labeled helper would keep in one place.

### F58 — Dead 'question ||' term in assistInitiativeAuthorized

- **Severity:** low | **Category:** dead-code | **Location:** `core/classifier/service.go:121`
- **Status:** open

questionOrRequest is computed as question || looksLikeExplicitRequest(...), but the very next statement returns true whenever question is true. Every later use of questionOrRequest (lines 127 and 132) is therefore reachable only when question is false, making the OR-term dead and the variable equivalent to looksLikeExplicitRequest alone. This obscures the actual gate being applied.

**Evidence excerpt (working tree, 2026-08-04):**
```go
questionOrRequest := question || looksLikeExplicitRequest(target.Envelope.Text)
...
if question {
	return true
}
```

**Suggested fix:** Compute request := looksLikeExplicitRequest(...) after the early return and use it directly.

**Fable verifier:** Verified service.go:120-132: the early `if question { return true }` makes the later uses of questionOrRequest at lines 127 and 132 reachable only when question is false, so the `question ||` term is provably redundant (and looksLikeExplicitRequest is even computed needlessly on the question path).

### F59 — Repeated full scans of pack.Sources per classification

- **Severity:** low | **Category:** inefficiency | **Location:** `core/classifier/openai_classifier.go:132`
- **Status:** open

One Decide call recomputes the same derived signals from pack.Sources many times: destinationRecentParticipantIDs and previousDestinationMessageFromAgent run for the payload (lines 132-145), again inside likelyConversationallyAddressedToAgent for two policy corrections (lines 384, 400), and destinationConversationFocus (filter + stable sort) runs at lines 133, 376, and 435. service.go adds more via validateDirectReplyForTarget, which is itself invoked up to twice on the mention path (lines 231 and 246). Packs are bounded so this is not dangerous, but it is 6+ redundant O(n)/O(n log n) passes on the per-message hot path.

**Evidence excerpt (working tree, 2026-08-04):**
```go
recentParticipantIDs := destinationRecentParticipantIDs(target.Envelope.ChannelID, pack.Sources)
conversationFocus := destinationConversationFocus(target, pack.Sources, 8)
```

**Suggested fix:** Compute the derived signals (participants, focus, previous-from-agent, likely-addressed) once into a small context struct and pass it through the correction chain.

**Fable verifier:** Verified the redundant scans: Decide computes participants/focus/previous-from-agent/likely-addressed at lines 132-145, likelyConversationallyAddressedToAgent (472-477) rescans and is re-called at 384 and 400, destinationConversationFocus re-runs at 376 and 435, and validateDirectReplyForTarget (service.go:640) re-invokes it up to twice on the mention path (231, 246) — bounded packs keep it low impact.

### F60 — Plain-text fallback path skips link and table-row normalization applied to JSON output

- **Severity:** low | **Category:** bug | **Location:** `core/deliveries/model_output.go:29`
- **Status:** open

The non-JSON path calls only promoteMarkdownTables(normalizeModelSlackMarkdown(...)), while JSON output goes through normalizeModelSlackResult, which additionally runs normalizeModelSlackLinks and normalizeModelTableRows. Identical prose containing a malformed link target (e.g. `<local/path|label>`) is rescued when it arrives inside JSON but causes an unsafe-link render rejection — and thus delivery abandonment — when it arrives as plain text. Two normalization pipelines for the same boundary have silently diverged.

**Evidence excerpt (working tree, 2026-08-04):**
```go
return promoteMarkdownTables(normalizeModelSlackMarkdown(types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentMRKDWN, Text: raw}}})), nil
```

**Suggested fix:** Route the plain-text segment through normalizeModelSlackResult so both entry points share one normalization pipeline.

**Fable verifier:** Verified model_output.go:29 skips normalizeModelSlackLinks that the JSON path gets via normalizeModelSlackResult (line 72), and the renderer rejects non-http(s) `<target|label>` links (renderer.go:985-989) causing the job to fail as invalid_slack_result at pipeline.go:1536-1546 — so identical prose with a malformed link is rescued in JSON but dropped as plain text.

### F61 — approvalValue's boolean return is computed and discarded at every call site

- **Severity:** low | **Category:** dead-code | **Location:** `core/deliveries/renderer.go:932`
- **Status:** open

approvalValue returns a second bool (len(text) <= 80 && no whitespace) that looks like an inline-renderability signal, but both callers (renderer.go:823 and renderer.go:863) discard it with `value, _ :=`. The heuristic is dead: long multi-word argument strings are rendered verbatim into approval tables and fallbacks, and only the coarse 1800-byte actionJSON guard limits size. Vestigial abstraction that suggests truncation behavior that never happens.

**Evidence excerpt (working tree, 2026-08-04):**
```go
return text, len(text) <= 80 && !strings.ContainsAny(text, " \t\r\n")
```

**Suggested fix:** Either use the bool to summarize non-inline values (mirroring inlineBodySummary) or drop the second return value entirely.

**Fable verifier:** Verified approvalValue (renderer.go:932-942) returns an inline-suitability bool that both of its only callers (renderer.go:823 and 863) discard with `value, _ :=`, making the heuristic dead code.

### F62 — approvalFallback re-implements titleForApprovalStatus verbatim

- **Severity:** low | **Category:** ai-slop | **Location:** `core/deliveries/renderer.go:842`
- **Status:** open

The status-to-label mapping in approvalFallback (lines 842-849) is a byte-for-byte copy of titleForApprovalStatus (lines 733-743): "Approval required"/"Action approved"/"Action denied"/"Approval expired". If one copy is edited (e.g. a new status or wording change), the Block Kit header and the accessibility fallback text silently diverge.

**Evidence excerpt (working tree, 2026-08-04):**
```go
label := "Approval required"
	if status == "approved" {
		label = "Action approved"
```

**Suggested fix:** Replace the duplicated block with `label := titleForApprovalStatus(status)`.

**Fable verifier:** Verified approvalFallback lines 842-849 duplicate titleForApprovalStatus (lines 733-743) byte-for-byte for all four statuses, and titleForApprovalStatus is already used at line 704 so the fallback could call it directly.

### F63 — Notice tone-to-icon map duplicated between validation and rendering

- **Severity:** low | **Category:** ai-slop | **Location:** `core/deliveries/renderer.go:787`
- **Status:** open

The literal map {"info": "ℹ️", "success": "✅", "warning": "⚠️", "error": "⚠️"} appears in both validateNotice (line 776, used for the 150-char header length check) and renderNoticeBlocks (line 787, used for actual rendering). If one copy changes (e.g. a distinct error icon), the validated length no longer matches what is rendered, allowing an oversized header to slip past validation.

**Evidence excerpt (working tree, 2026-08-04):**
```go
icon := map[string]string{"info": "ℹ️", "success": "✅", "warning": "⚠️", "error": "⚠️"}[notice.Tone]
```

**Suggested fix:** Hoist the map to a package-level var (or a noticeIcon(tone) helper) used by both functions.

**Fable verifier:** Verified the identical tone-to-icon map literal appears in both validateNotice (line 776, feeding the 150-rune header check at 777) and renderNoticeBlocks (line 787, feeding the rendered header at 792), so an edit to one copy would desynchronize validation from rendering.

### F64 — Regex compiled inside loop per artifact URL and per synopsis call

- **Severity:** low | **Category:** inefficiency | **Location:** `core/deliveries/renderer.go:370`
- **Status:** open

stripPublishedArtifactLinks calls regexp.MustCompile inside its per-URL loop on every artifact publication, and compactPublishedArtifactSynopsis (line 334) compiles the constant `\n\s*\n` paragraph splitter on every invocation. These run once per delivered artifact result so cost is modest, but every other pattern in this file is a package-level var — the inconsistency invites the same mistake on hotter paths, and MustCompile in request flow can panic-on-typo at runtime rather than init.

**Evidence excerpt (working tree, 2026-08-04):**
```go
link := regexp.MustCompile(`<` + regexp.QuoteMeta(artifactURL) + `(?:\|[^>]*)?>`)
```

**Suggested fix:** Hoist the constant paragraph-splitter regex to a package-level var; for the URL link stripper, build the two removals with plain strings (strings.ReplaceAll plus a small scanner for the `<url|label>` form) instead of compiling a regex per URL.

**Fable verifier:** Verified regexp.MustCompile inside the per-URL loop at renderer.go:370 and per-call at line 334 on the artifact delivery path (called from line 309), while all eight other patterns in the file (lines 38-45) are package-level vars — real but modest inefficiency/inconsistency.

### F65 — splitMRKDWN compares a byte index against a rune limit

- **Severity:** low | **Category:** bug | **Location:** `core/deliveries/renderer.go:1021`
- **Status:** open

strings.LastIndex(prefix, "\n") returns a byte offset into prefix, but it is compared against limit/2, a rune count. For multibyte-heavy text (CJK, emoji) a newline that is early in rune terms can still have a byte offset above limit/2, so the chunk is cut far before the intended halfway point — e.g. mostly-CJK text with a newline at rune 600 (~byte 1800 > 1500) splits into ~600-rune chunks instead of ~3000, multiplying the number of Slack section blocks and messages.

**Evidence excerpt (working tree, 2026-08-04):**
```go
if newline := strings.LastIndex(prefix, "\n"); newline > limit/2 {
```

**Suggested fix:** Compute the newline position in runes (e.g. utf8.RuneCountInString(prefix[:newline])) before comparing against limit/2, or scan the rune slice directly.

**Fable verifier:** splitMRKDWN builds prefix as a limit-rune string then compares strings.LastIndex's byte offset against the rune-count limit/2 (maxSectionCharacters=3000), so multibyte text qualifies newlines as early as ~limit/6 runes and produces smaller chunks and more Slack blocks than intended.

### F66 — EventTime zero-fallback is unreachable; missing event_time yields Unix epoch

- **Severity:** low | **Category:** bug | **Location:** `core/slack/live.go:861`
- **Status:** open

Line 831 builds `EventTime: time.Unix(int64(callback.EventTime), 0).UTC()`. When Slack's integer event_time is absent (0), this produces 1970-01-01T00:00:00Z, which is not the zero time.Time, so `base.EventTime.IsZero()` is always false and the intended fallback to slackTimestamp(base.MessageTS) never executes. The same epoch value also bypasses the downstream zero-check in observer eventTime() (core/observer/store.go:49), flowing an epoch timestamp into retention expiry (`expires_at = SlackEventTime.Add(retention)` — instantly expired) and classifier time anchoring (`now := target.Envelope.EventTime`).

**Evidence excerpt (working tree, 2026-08-04):**
```go
if base.EventTime.IsZero() {
	base.EventTime = slackTimestamp(base.MessageTS)
}
```

**Suggested fix:** Only populate EventTime when callback.EventTime > 0 (leave zero Time otherwise), so both the local fallback and the downstream IsZero checks work as intended.

**Fable verifier:** time.Unix(0,0).UTC() is 1970 epoch rather than the zero time.Time, making the base.EventTime.IsZero() fallback at live.go:861 unreachable, and observer/mongo_store.go:200 would derive expires_at from that epoch, so a zero event_time callback yields an instantly-expired observation instead of the intended slackTimestamp fallback.

### F68 — progressMessage and deliveryParts duplicate a 30-line metadata pagination scan

- **Severity:** low | **Category:** ai-slop | **Location:** `core/slack/live.go:1017`
- **Status:** open

progressMessage (lines 1017-1047) and deliveryParts (lines 1194-1229) contain near-identical logic: a 20-page loop, a threadTS-vs-channel branch calling GetConversationRepliesContext or GetConversationHistoryContext with Limit 100 and IncludeAllMetadata, cursor advancement, and a metadata match over messages. Only the EventType/payload predicate and the result shape differ. Two copies invite silent divergence (e.g., changing the page cap or adding an Oldest bound in one but not the other).

**Evidence excerpt (working tree, 2026-08-04):**
```go
cursor := ""
for page := 0; page < 20; page++ {
	var messages []slackapi.Message
	var hasMore bool
	var next string
	if threadTS != "" {
```

**Suggested fix:** Extract one helper that paginates messages for a channel/thread and invokes a per-message callback (or predicate), and implement both reconciliation functions on top of it.

**Fable verifier:** Re-read both functions: progressMessage (1017-1047) and deliveryParts (1194-1229) duplicate the identical 20-page, Limit-100, IncludeAllMetadata, threadTS-vs-channel pagination scaffold, differing only in the metadata predicate and result shape.

### F69 — Live ingress and history import detect bot mentions differently

- **Severity:** low | **Category:** bug | **Location:** `core/slack/live.go:857`
- **Status:** open

Live normalization only matches the exact form `<@U…>` while the history/catch-up path uses mentionsSlackUser, which also accepts the labeled form `<@U…|name>` (context_sync.go:625-631). The same message can therefore be IsMention=true when recovered by CatchUp but false when observed live — and RecoverContextEnvelope uses IsMention to decide whether a recovered message re-enters the decision queue, so the two ingestion paths apply different participation semantics to identical text.

**Evidence excerpt (working tree, 2026-08-04):**
```go
base.IsMention = botUserID != "" && strings.Contains(base.Text, "<@"+botUserID+">")
```

**Suggested fix:** Use the shared mentionsSlackUser helper in NormalizeEventsAPI so both ingestion paths share one mention definition.

**Fable verifier:** live.go:857 matches only the exact <@U...> form while context_sync.go:625-631's mentionsSlackUser also matches <@U...|label>, and pipeline.go:404 (RecoverContextEnvelope) uses IsMention to admit recovered messages to the decision queue, so identical legacy-format mention text gets different participation semantics per ingestion path.

### F70 — Redundant atomic increment under an already-held mutex

- **Severity:** low | **Category:** ai-slop | **Location:** `core/slack/stub.go:158`
- **Status:** open

StartProgress and Send both call `atomic.AddUint64(&s.sequence, 1)` while holding s.mu, which already serializes every access to s.sequence. Mixing atomics with mutex-guarded access to the same field is an inconsistent idiom that suggests a lock-free path exists when it does not, and it is the only atomic use in the struct.

**Evidence excerpt (working tree, 2026-08-04):**
```go
seq := atomic.AddUint64(&s.sequence, 1)
```

**Suggested fix:** Replace with a plain `s.sequence++` under the mutex and drop the sync/atomic import.

**Fable verifier:** The only two accesses to s.sequence (stub.go:158 and 202) are both atomic.AddUint64 calls executed while holding s.mu, with no lock-free reader anywhere, so the atomic is redundant and the sole sync/atomic use in the file.

### F71 — pageViews entries for 'routines' and 'directives' are dead and schema-stale

- **Severity:** low | **Category:** dead-code | **Location:** `core/server/templates/index.html:777`
- **Status:** open

The generic table path (loadScoped/renderData) explicitly excludes the routines and directives pages (`page !== 'directives' && page !== 'routines'` at line 1314); both have dedicated bespoke implementations. So the pageViews.routines and pageViews.directives config blocks (guidance, empty text, search, filter, columns, summaries) are never executed. The directives entry has also silently diverged from the real schema: it references state, revision_id, activated_at, and actor_id, while DirectiveRevision actually serializes active, id, revision, and created_by — evidence it was copy-pasted and never reconciled.

**Evidence excerpt (working tree, 2026-08-04):**
```go
routines: {
        guidance:'Recurring work is classifier-gated before any output. Open details for instructions, confidence, and schedule.',
```

**Suggested fix:** Delete the routines and directives entries from pageViews (about 24 lines), or route those pages through renderData and fix the field names.

**Fable verifier:** Line 1314 excludes the routines and directives pages from the loadScoped/renderData path, their bespoke blocks at 1341+/1480+ never reference pageViews, and DirectiveRevision (channelconfig/store.go) serializes id/revision/created_by/active rather than the state/revision_id/activated_at/actor_id fields the dead config references.

### F72 — Triggers page references nonexistent event_type field

- **Severity:** low | **Category:** ai-slop | **Location:** `core/server/templates/index.html:791`
- **Status:** open

The live triggers page config lists an event_type column and a summary counting distinct row.event_type values, but triggers.Subscription has no event_type JSON field (fields are id, kind, instruction, cron, timezone, interval, next_run, classifier_gate, ...). renderData silently drops the unavailable column, and the 'Event types' summary card always reads 0. The guidance copy about 'a bounded event pattern' also mismatches the actual heartbeat-only schema.

**Evidence excerpt (working tree, 2026-08-04):**
```go
search:'Find a trigger…', filter:{key:'enabled',label:'state'}, columns:['id','enabled','event_type','channel_id','kind','created_at'],
```

**Suggested fix:** Replace event_type with a real field (e.g. kind or cron) in both the columns list and the summaries, and update the guidance copy.

**Fable verifier:** triggers.Subscription (triggers.go:24-45) has no event_type JSON field, renderData line 1248 silently drops unavailable configured columns, and the 'Event types' summary counts row.event_type values that never exist, so the card always reads 0.

### F73 — injectEnvelope reimplements decodeMutation verbatim

- **Severity:** low | **Category:** ai-slop | **Location:** `core/server/server.go:394`
- **Status:** open

The CSRF constant-time compare, Content-Type media-type check, 1MB MaxBytesReader, and DisallowUnknownFields decode in injectEnvelope (lines 394-408) are a line-for-line copy of decodeMutation (lines 1147-1163), differing only in the decode-error code string. Two copies of the security-sensitive request-admission logic can silently diverge (e.g., a future body-size or CSRF change applied to one path only).

**Evidence excerpt (working tree, 2026-08-04):**
```go
if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-TOS-TAG-CSRF")), []byte(s.csrf)) != 1 {
		writeError(w, http.StatusForbidden, "csrf_invalid")
```

**Suggested fix:** Call decodeMutation(w, r, s.csrf, &envelope) from injectEnvelope (optionally parameterizing the decode-error code) and delete the duplicated block.

**Fable verifier:** injectEnvelope (server.go:394-408) duplicates decodeMutation (1147-1163) verbatim — same CSRF compare, media-type check, 1MB MaxBytesReader, and DisallowUnknownFields decode — differing only in the decode error code, so the security-sensitive admission logic exists in two copies that can diverge.

### F74 — Inject/preview/keystore form submits swallow errors with no operator feedback

- **Severity:** low | **Category:** bug | **Location:** `core/server/templates/index.html:2033`
- **Status:** open

The overview 'Inject deterministic Slack fixture' and 'Route preview' submit handlers are async with no try/catch: when the server rejects the payload (e.g. 422 envelope_rejected or no_eligible_route — normal validation outcomes), fetchJSON throws, the rejection is unhandled, and the result <pre> keeps stale content, so the operator gets zero indication the action failed. The keystore form has a try/catch, but its CSRF fetch is placed before the try, so a CSRF fetch failure is likewise silently swallowed.

**Evidence excerpt (working tree, 2026-08-04):**
```go
inject.addEventListener('submit', async event => {
      event.preventDefault();
      const csrf = await fetchJSON('/admin/api/csrf');
```

**Suggested fix:** Wrap the handler bodies in try/catch (including the CSRF fetch) and write the error message into the corresponding result/notice element, as the directive and automation forms already do.

**Fable verifier:** The inject/preview handlers (index.html:2033-2043) have no try/catch while fetchJSON throws on non-ok responses like 422 envelope_rejected/no_eligible_route, and the keystore handler's CSRF fetch (1617) sits before its try block, so all three swallow failures with no operator feedback.

### F75 — listRecords bypasses requiredOrganization, turning a missing param into a 500

- **Severity:** low | **Category:** ai-slop | **Location:** `core/server/server.go:636`
- **Status:** open

Every other tenant-scoped list handler (jobs, deliveries, decisions, memory, approvals, routines, triggers) uses requiredOrganization and returns 400 organization_id_required. listRecords passes the raw query value; management.Mongo.List then errors on empty org, and writeList maps that to 500 query_failed. Same operation, two idioms, and the missing-parameter case is misclassified as a server error (also miscategorizing client mistakes in monitoring).

**Evidence excerpt (working tree, 2026-08-04):**
```go
values, err := s.deps.Records.List(r.Context(), kind, r.URL.Query().Get("organization_id"), 50)
```

**Suggested fix:** Use requiredOrganization(w, r) inside the listRecords closure like the other tenant-scoped handlers.

**Fable verifier:** listRecords (server.go:636) passes the raw query value and management.Mongo.List (store.go:28-30) errors on empty organization_id, which writeList maps to 500 query_failed, while every sibling tenant-scoped handler uses requiredOrganization and returns 400 organization_id_required.

### F76 — Unreachable empty-rows re-check in renderData

- **Severity:** low | **Category:** dead-code | **Location:** `core/server/templates/index.html:1242`
- **Status:** open

renderData already returns early at line 1234 when rows is empty (`if (!rows.length) { ... return; }`), so the `!rows.length` clause in the immediately following condition can never be true. It reads like a leftover from a refactor and obscures the real purpose of the second guard (non-object rows).

**Evidence excerpt (working tree, 2026-08-04):**
```go
if (!rows.length || rows.some(row => row === null || typeof row !== 'object' || Array.isArray(row))) {
```

**Suggested fix:** Drop the `!rows.length ||` clause, leaving only the non-object-row guard.

**Fable verifier:** renderData returns early at index.html:1234-1241 when rows is empty, so the !rows.length clause in the condition at line 1242 is unreachable dead code.

### F77 — Events forwarder races error against buffered events, dropping audit/usage events

- **Severity:** low | **Category:** bug | **Location:** `core/harness/worker_codex.go:340`
- **Status:** open

session.fail() enqueues the error and closes both channels, leaving already-emitted events buffered in session.events. The forwarder's select then has both cases ready and picks pseudo-randomly; when it picks the error case it returns immediately, discarding buffered events (tool.execution.completed, web.search.completed, usage.updated) that the pipeline turns into audit receipts and usage metrics. Failed/interrupted turns therefore nondeterministically lose audit records for work that actually ran.

**Evidence excerpt (working tree, 2026-08-04):**
```go
if eventErr != nil {
	errs <- eventErr
}
return
```

**Suggested fix:** On receiving an error, first drain the remaining buffered session.events into the output channel (non-blocking), then forward the error and return.

**Fable verifier:** fail() enqueues the error into buffered errs and closes both channels while emitted events remain in the 128-slot events buffer, so the forwarder select (worker_codex.go:321-345) can pseudo-randomly take the error case and return, dropping buffered web.search.completed/usage.updated/tool.execution.completed events the pipeline turns into audit receipts (pipeline.go:1911) and usage metrics.

### F78 — Hard-coded "tos_tag_tool" argument makes the tool parameter of wiki provenance helpers dead

- **Severity:** low | **Category:** ai-slop | **Location:** `core/harness/worker_codex.go:584`
- **Status:** open

Both call sites pass the string literal "tos_tag_tool" rather than request.Tool, so the `tool` parameter of producedWikiArtifactURL and resolvedWikiReference is always the constant and their `if tool != "tos_tag_tool"` guards are unreachable from production code (tests even exercise the dead branch via a "wrong dynamic tool" case that cannot occur). The behavior is correct — bridgeArguments built by buildWikiBridgeRequest carry the telemetryos.wiki tool_id — but the constant argument plus dead guard is misleading and invites a future caller to assume the guard does real work.

**Evidence excerpt (working tree, 2026-08-04):**
```go
if artifactURL := producedWikiArtifactURL("tos_tag_tool", bridgeArguments, output); artifactURL != "" {
```

**Suggested fix:** Drop the tool parameter from both helpers (they already gate on tool_id/operation_id parsed from bridgeArguments), or pass request.Tool and admit wikiDynamicTool explicitly.

**Fable verifier:** Both production call sites (worker_codex.go:584, 587) pass the literal "tos_tag_tool", making the tool parameter constant and the tool != "tos_tag_tool" guards (lines 896, 927) unreachable outside tests, which exercise an impossible tos_tag_trigger branch — behavior is correct but the parameter is dead and misleading.

### F79 — publishToolValidation is a diverged near-duplicate of publishToolResult that drops session_id

- **Severity:** low | **Category:** ai-slop | **Location:** `core/harness/worker_codex.go:647`
- **Status:** open

publishToolValidation rebuilds the same activity.Record construction as publishToolResult (same kind, summary format, title logic for interrupted_retrying) but silently diverges: it never reads s.threadID under the mutex, so its records omit session_id even when the thread is known, while every other codex activity record includes it. Copy-paste divergence makes activity records for validation failures inconsistent and harder to correlate.

**Evidence excerpt (working tree, 2026-08-04):**
```go
details := map[string]any{
	"job_id": s.jobID, "attempt_id": s.attemptID, "method": method,
	"direction": "outbound", "status": "interrupted_retrying", "dynamic_tool": wikiDynamicTool,
```

**Suggested fix:** Route validation publishing through publishToolResult (status "interrupted_retrying" plus a validation_code detail), or at minimum add the same mutex-guarded session_id enrichment.

**Fable verifier:** publishToolValidation (worker_codex.go:647-660) rebuilds the same codex.tool activity record as publishToolResult but never reads s.threadID under the mutex, so validation records omit session_id even though validation occurs after thread start, unlike publish and publishToolResult which both enrich with it.

### F80 — Server-initiated tool calls run with context.Background, outliving session termination

- **Severity:** low | **Category:** bug | **Location:** `core/harness/codex_app_server.go:192`
- **Status:** open

handleServerRequest invokes onRequest (WorkerCodex.serverRequest -> callBridge HTTP call) with context.Background(), so an in-flight reviewed-tool HTTP request is not cancelled when the turn is interrupted or the session terminated; it continues until the httpClient timeout (up to 5 minutes), holding a goroutine and issuing gateway work for a dead attempt. Lease fencing and RevokeAttempt make this safe, but cancellation should still propagate so aborted jobs stop consuming gateway/tool resources promptly.

**Evidence excerpt (working tree, 2026-08-04):**
```go
result, err = c.onRequest(context.Background(), message.Method, message.Params)
```

**Suggested fix:** Derive the request context from a client-lifetime context cancelled in close()/fail(), so in-flight bridge calls abort when the transport dies.

**Fable verifier:** handleServerRequest (codex_app_server.go:192) passes context.Background() into onRequest → callBridge's http.NewRequestWithContext, and terminate()/close() cancels nothing, so an in-flight bridge call for a dead attempt runs until the httpClient timeout capped at 5 minutes (worker_codex.go:135); safety is preserved by fencing but the resource lingering is real.

### F81 — Redundant NUL checks alongside hasDisallowedControl

- **Severity:** low | **Category:** ai-slop | **Location:** `core/harness/wiki_tool.go:248`
- **Status:** open

validateWikiValues checks strings.ContainsRune(value, 0) in the same condition as hasDisallowedControl(value), but hasDisallowedControl already rejects every rune below 0x20 except '\t', which includes NUL. The same redundant pair is repeated in the tags loop at line 270. The only place the NUL check is load-bearing is the Body check (line 252), which deliberately does not use hasDisallowedControl.

**Evidence excerpt (working tree, 2026-08-04):**
```go
if len(value.value) > value.max || strings.ContainsRune(value.value, 0) || hasDisallowedControl(value.value) {
```

**Suggested fix:** Drop the ContainsRune(…, 0) term where hasDisallowedControl is also applied (lines 248 and 270), keeping it only for the Body check.

**Fable verifier:** hasDisallowedControl (wiki_tool.go:277-284) rejects every rune below 0x20 except tab, so strings.ContainsRune(v, 0) at lines 248 and 270 is strictly subsumed, while only the Body check at line 252 legitimately needs it.

### F83 — marketplace.Load and the RequiresTools/availableTools resolution machinery are production-dead

- **Severity:** low | **Category:** dead-code | **Location:** `core/marketplace/marketplace.go:51`
- **Status:** open

Load (the Catalog/SkillEntry JSON contract, ~40 lines) has no production caller — its only reference is core/workers/workers_test.go. It is also the only code path that populates SkillSnapshot.RequiresTools for behavioral skills (LoadPlugin/LoadPluginMarketplace never set it), and Resolve's availableTools gate is only ever invoked with an empty map via NewRegistry ('Resolve(snapshots, map[string]bool{})' at line 322). Tool skills that do carry RequiresTools (from tools.Registry.Select) bypass NewRegistry entirely in core.go:192, so the tool-availability check never fires with real data at runtime.

**Evidence excerpt (working tree, 2026-08-04):**
```go
func Load(root, catalogPath string) (Catalog, []SkillSnapshot, error) {  // only caller: workers_test.go
...
resolved, err := Resolve(snapshots, map[string]bool{})
```

**Suggested fix:** Delete Load and the Catalog/SkillEntry contract (move the test onto LoadPlugin fixtures), and either wire real tool availability into Resolve or drop the availableTools parameter.

**Fable verifier:** rg confirms marketplace.Load's sole caller is workers_test.go:57, LoadPlugin/LoadPluginMarketplace never populate RequiresTools (marketplace.go:237), Resolve is only invoked with an empty map via NewRegistry (line 322), and RequiresTools-bearing tool skills from tools.Registry.Select bypass NewRegistry entirely (core.go:188-192, 223).

### F84 — snapshotSharedReferences duplicates the snapshotSkill walker and hash loop nearly verbatim

- **Severity:** low | **Category:** ai-slop | **Location:** `core/marketplace/marketplace.go:261`
- **Status:** open

snapshotSharedReferences (lines 261-317) repeats snapshotSkill's WalkDir body almost line for line — the symlink check, regular-file/executable-bit check, identical extension allowlist, the same maxSkillFiles/maxSkillBytes limits with the same post-append check, then an identical sort + name/NUL/data/NUL hash loop over readRootFile. Roughly 80 lines of near-duplicate validation logic in the same file; any future tightening of the file-safety rules (e.g. a new forbidden extension) must be applied in two places and can silently diverge, exactly as readRootFile already has across packages.

**Evidence excerpt (working tree, 2026-08-04):**
```go
if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 != 0 {
	return fmt.Errorf("%w: executable shared reference %s", ErrUnsafeMarketplace, path)
}
ext := strings.ToLower(filepath.Ext(path))
if ext != ".md" && ext != ".txt" && ext != ".json" && ext != ".yaml" && ext != ".yml" {
```

**Suggested fix:** Extract one walkValidatedFiles(root) ([]string, error) helper plus one hashFiles(root, files) helper and use them from both snapshotSkill and snapshotSharedReferences.

**Fable verifier:** snapshotSharedReferences (lines 261-317) duplicates snapshotSkill's symlink/regular-executable/extension-allowlist/size-limit walker and the byte-identical name-NUL-data-NUL hash loop, differing only in directory handling, and readRootFile is indeed already duplicated in core/tools/tools.go:336.

### F85 — GatewayRequest is a zero-value wrapper around Request with no leverage

- **Severity:** low | **Category:** ai-slop | **Location:** `core/tools/gateway.go:26`
- **Status:** open

GatewayRequest embeds Request and adds no fields, methods, or invariants. Its single caller (bridge.go:261) constructs GatewayRequest{Request: Request{...}} and Gateway.Execute immediately unwraps it with request.Request when calling the executor. The type only adds construction noise and a misleading suggestion that gateway-level requests differ from executor-level requests.

**Evidence excerpt (working tree, 2026-08-04):**
```go
type GatewayRequest struct {
	Request
}
```

**Suggested fix:** Delete GatewayRequest and have Gateway.Execute accept Request directly.

**Fable verifier:** GatewayRequest (gateway.go:26-28) embeds Request with no added fields, methods, or invariants; its only callers (bridge.go:261, gateway_test.go:37) wrap Request{...} and Gateway.Execute immediately unwraps request.Request at line 55.

### F86 — Index option drift is silently swallowed by string-matching IndexOptionsConflict

- **Severity:** low | **Category:** bug | **Location:** `core/database/database.go:182`
- **Status:** open

EnsureIndexes ignores CreateOne errors whose message contains "IndexOptionsConflict" (server code 85) without logging. If an index definition's options change in code (e.g., a partial filter or unique flag on session_generation_writer_unique or a TTL value), existing deployments keep the stale index forever and nothing reports it, while a key-pattern change (code 86, IndexKeySpecsConflict) fails Connect hard — an inconsistent contract. String matching on err.Error() is also fragile versus checking the server error code.

**Evidence excerpt (working tree, 2026-08-04):**
```go
func isIndexOptionsConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), "IndexOptionsConflict")
}
```

**Suggested fix:** Detect code 85 via mongo.ServerError.HasErrorCode, and on conflict either drop-and-recreate the named index or at least log a warning that the on-disk index differs from the code's contract.

**Fable verifier:** EnsureIndexes (database.go:94) silently ignores errors matching the string "IndexOptionsConflict" (server code 85) with no logging, so option drift (TTL, partial filter, unique) on the ~65 declared indexes persists invisibly while key-pattern changes (code 86) fail Connect hard, and the err.Error() string match is fragile versus mongo.ServerError code checks.

### F87 — fromModel swallows decode errors for Result/ResolvedModel/RouteTrace

- **Severity:** low | **Category:** bug | **Location:** `core/jobs/mongo_queue.go:331`
- **Status:** open

fromModel round-trips the stored `any` fields through bson.Marshal/Unmarshal and discards both errors (`_ = bson.Unmarshal(encoded, &result)`). If a stored job result or resolved-model document fails to decode (schema drift, corrupt write), the job silently proceeds with a zero SlackResult/ResolvedModel instead of surfacing an error, which downstream looks like an empty model answer or missing routing snapshot with no diagnostic.

**Evidence excerpt (working tree, 2026-08-04):**
```go
if encoded, err := bson.Marshal(doc.Result); err == nil {
	_ = bson.Unmarshal(encoded, &result)
}
```

**Suggested fix:** Propagate or at least log a bounded diagnostic when the re-unmarshal fails, so a corrupt persisted result is distinguishable from an intentionally empty one.

**Fable verifier:** fromModel (mongo_queue.go:329-343) discards both bson.Marshal and bson.Unmarshal errors for the any-typed Result/ResolvedModel/RouteTrace fields, so a type-drifted or corrupt persisted document silently yields a zero SlackResult indistinguishable from an intentionally empty one.

### F88 — Cancel steering-epoch semantics differ between MongoQueue and MemoryQueue

- **Severity:** low | **Category:** ai-slop | **Location:** `core/jobs/mongo_queue.go:212`
- **Status:** open

MongoQueue.Cancel's aggregation pipeline unconditionally increments steering_epoch for every matched state, while MemoryQueue.Cancel increments SteeringEpoch only in the Leased/Preparing/Running (Cancelling) branch and leaves it unchanged for immediate Queued/RetryWait/WaitingApproval cancellations. The two implementations of the same interface method have quietly diverged, so any consumer using the epoch as a fencing token observes different values depending on the backing store, and memory-backed tests cannot catch Mongo epoch behavior.

**Evidence excerpt (working tree, 2026-08-04):**
```go
"failure_reason": reason, "steering_epoch": bson.M{"$add": bson.A{"$steering_epoch", 1}},
```

**Suggested fix:** Make the increment conditional on the Cancelling branch in the Mongo pipeline (mirroring MemoryQueue), or increment unconditionally in MemoryQueue — whichever epoch contract the fencing consumers actually rely on.

**Fable verifier:** MongoQueue.Cancel's pipeline (line 212) unconditionally increments steering_epoch for all six matched states while MemoryQueue.Cancel (memory_queue.go:118-127) increments only in the Leased/Preparing/Running branch, a verified accidental divergence between two implementations of the same Queue interface, though severity stays low because no consumer (bridge.go:543 requires StateRunning) reads the epoch of a terminal Cancelled job.

### F89 — Production LateCandidates gate is three hardcoded English substrings defined in the memory store file

- **Severity:** low | **Category:** ai-slop | **Location:** `core/observer/memory_store.go:332`
- **Status:** open

isStatusQuestion — matching only "system down", "is it down", and "outage?" — lives in memory_store.go alongside the memory test double, but is load-bearing for the production MongoStore.LateCandidates path (mongo_store.go:321) that pipeline.go:931 uses to find late incident questions. The shared ErrMessageNotFound sentinel is likewise defined here as a custom type while the package's other sentinel lives in errors.go. A three-phrase, English-only, punctuation-sensitive heuristic deciding production catch-up behavior is fragile and easy to miss during review because of where it is defined.

**Evidence excerpt (working tree, 2026-08-04):**
```go
func isStatusQuestion(text string) bool {
	value := strings.ToLower(text)
	return strings.Contains(value, "system down") || strings.Contains(value, "is it down") || strings.Contains(value, "outage?")
```

**Suggested fix:** Move isStatusQuestion and ErrMessageNotFound into store.go/errors.go where shared package contracts live, and either broaden the phrase list into a documented, configurable set or document why exactly these three phrases gate late candidates.

**Fable verifier:** Verified isStatusQuestion (memory_store.go:332) gates the production MongoStore.LateCandidates path (mongo_store.go:321) used by pipeline.reconsiderLateQuestions (pipeline.go:931) with exactly three hardcoded English substrings, and ErrMessageNotFound (memory_store.go:337) is a shared package sentinel defined beside the test double while errors.go holds ErrInvalidEnvelope.

### F90 — Restricted-signal and fact upsert branches are divergent copy-paste with dead $setOnInsert logic

- **Severity:** low | **Category:** ai-slop | **Location:** `core/intelligence/projector.go:127`
- **Status:** open

The restricted branch carefully splits $set/$setOnInsert to preserve public_id and created_at across updates, while the adjacent fact branch blindly `$set`s the whole doc including a freshly minted PublicID every time. Because DeleteMany on both collections always precedes these upserts (lines 92-96), the existing document can never survive to be found, so the restricted branch's preservation logic is unreachable and both branches always insert. Two near-duplicate blocks implementing the same operation with silently different idioms invite future drift.

**Evidence excerpt (working tree, 2026-08-04):**
```go
"$setOnInsert": bson.M{"public_id": doc.PublicID, "organization_id": doc.OrganizationID, "kind": doc.Kind, ...
```

**Suggested fix:** Use one shared upsert helper with a single idiom for both branches (or plain inserts, since the preceding DeleteMany guarantees absence).

**Fable verifier:** Verified DeleteMany at projector.go:92-96 uses a filter (org/channel/message_ts) that is a superset of both upsert filters, so the restricted branch's $set/$setOnInsert preservation at line 127 can never find an existing doc in the same Project call while the fact branch at line 132 $sets the entire doc with a fresh PublicID — two divergent idioms for the same always-insert operation.

### F91 — safeSummary truncates on a byte boundary and can produce invalid UTF-8

- **Severity:** low | **Category:** bug | **Location:** `core/intelligence/projector.go:169`
- **Status:** open

safeSummary slices the message text with value[:500], which counts bytes, not runes, and can cut a multi-byte UTF-8 sequence in half. The corrupted string is persisted as the situation fact Summary and later flows into context packs and JSON encoding (where invalid bytes become U+FFFD). The sibling memory package correctly measures rune length (store.go validateText uses len([]rune(value))), so this is also an internal inconsistency.

**Evidence excerpt (working tree, 2026-08-04):**
```go
if len(value) > 500 {
		return value[:500]
```

**Suggested fix:** Truncate on a rune boundary, e.g. convert to []rune (or walk back with utf8.RuneStart) before slicing at 500.

**Fable verifier:** Verified safeSummary (projector.go:166-172) slices bytes with value[:500], which can bisect a multi-byte UTF-8 rune in an incident message and persist invalid UTF-8 into SituationFact.Summary consumed by pipeline context candidates (pipeline.go:1093), while memory/store.go:72 correctly measures rune length.

### F92 — Summarizer endpoint built from untrimmed BaseURL despite trimmed validation

- **Severity:** low | **Category:** bug | **Location:** `core/memory/summarizer.go:88`
- **Status:** open

NewOpenAISummarizer validates url.Parse(strings.TrimSpace(options.BaseURL)) but constructs the endpoint from the raw options.BaseURL. A configured base URL with leading/trailing whitespace passes constructor validation, then every Summarize call fails at http.NewRequestWithContext (or trailing-space input defeats the TrimRight("/")), surfacing only as runtime "memory summary request failed" warnings instead of a startup configuration error.

**Evidence excerpt (working tree, 2026-08-04):**
```go
return &OpenAISummarizer{endpoint: strings.TrimRight(options.BaseURL, "/") + "/responses", apiKey: strings.TrimSpace(options.APIKey), ...
```

**Suggested fix:** Trim once up front (base := strings.TrimSpace(options.BaseURL)), validate and build the endpoint from the same trimmed value.

**Fable verifier:** Verified NewOpenAISummarizer validates url.Parse(strings.TrimSpace(options.BaseURL)) at line 77 but builds the endpoint from the untrimmed options.BaseURL at line 88, so whitespace-padded config passes construction and fails only at runtime in Summarize with the 'memory summary request failed' warning path in curator.go:127.

### F94 — RuneTokenizer is unused

- **Severity:** low | **Category:** dead-code | **Location:** `core/contextpacks/builder.go:34`
- **Status:** open

RuneTokenizer has zero references anywhere in the repository outside its own definition: production wiring (core/core.go:88), evals, and every test construct WordTokenizer only. It is a vestigial alternative implementation with no callers and no leverage; its "rune/v1" revision string is likewise never exercised.

**Evidence excerpt (working tree, 2026-08-04):**
```go
type RuneTokenizer struct{}

func (RuneTokenizer) Revision() string { return "rune/v1" }
```

**Suggested fix:** Delete RuneTokenizer (the Tokenizer interface already documents the extension point) or add it back when a real caller exists.

**Fable verifier:** rg across the repository shows RuneTokenizer has zero references outside its own definition at builder.go:34-39; production wiring (core.go:88), evals (eval.go:399), and every test construct WordTokenizer only, so the type and its rune/v1 revision are dead code.

### F96 — Hand-rolled env override functions duplicate the orale loader for unambiguous names

- **Severity:** low | **Category:** ai-slop | **Location:** `core/config/config.go:410`
- **Status:** open

The comment above applyClassifierEnvironment justifies explicit mapping only for tokenization-ambiguous names like TAG__CLASSIFIER__OPENAI_API_KEY. But applyJobsEnvironment, and most of applyCodexEnvironment/applyClassifierEnvironment, re-implement mappings orale already performs identically: TAG__JOBS__WORKER_CONCURRENCY maps to the `workerConcurrency` tag, TAG__CODEX__WORKER_ROOT/WEB_SEARCH_MODE/COMMAND to their tags, and orale parses durations (intoDuration in orale v1.11.0 calls time.ParseDuration) and ints from env strings. Live-Slack fields (TAG__SLACK__BOT_USER_ID, tokens, team/app IDs) already rely on the generic loader alone, proving it works. The duplication (~90 lines) also quietly gives env precedence over flags/files for only these fields, inconsistent with the rest of the config surface.

**Evidence excerpt (working tree, 2026-08-04):**
```go
func applyJobsEnvironment(cfg *JobsConfig) error {
	if raw, ok := os.LookupEnv("TAG__JOBS__WORKER_CONCURRENCY"); ok {
```

**Suggested fix:** Keep explicit overrides only for genuinely ambiguous or list-splitting cases (OPENAI_API_KEY, comma-separated INJECTED_SKILLS/OUTPUT_CHANNEL_IDS, CODEX_HOME fallback) and delete the mappings the loader already handles, restoring a single precedence model.

**Fable verifier:** Read orale v1.11.0's loadEnvironment/toCamelCase and into.go: TAG__JOBS__WORKER_CONCURRENCY, TAG__CODEX__COMMAND/WORKER_ROOT/WEB_SEARCH_MODE, and most TAG__CLASSIFIER__* names tokenize exactly to the struct tags with typed parsing that errors on bad values, only OPENAI_API_KEY (openaiApiKey vs tag openAiApiKey) is genuinely ambiguous per the code comment's own rationale, and the post-GetAll overrides invert orale's flags-over-env precedence for only these fields.

---

## Appendix A — 17 unverified low findings (passthrough; verify the claim before fixing)

These exceeded the adversarial-verification budget cap and carry only single-reviewer confidence. An agent picking one up must first re-verify the claim against source.

### F97 — Memory keystore Put silently discards updated purpose on existing secrets

- **Severity:** low | **Category:** bug | **Location:** `core/keystore/keystore.go:73`
- **Status:** open

When updating an existing secret by name, the in-memory Store replaces the freshly built reference with the stored one wholesale, so the caller's new `purpose` value is dropped and the old purpose is retained. The Mongo implementation keeps the new purpose and copies only ID and CreatedAt from the existing document. The two Repository implementations therefore return different metadata for the same rotation call, and tests using the memory store validate the wrong behavior.

**Evidence excerpt (working tree, 2026-08-04):**
```go
if id := s.byName[key]; id != "" {
	reference = s.values[id].Reference
	reference.UpdatedAt = now
}
```

**Suggested fix:** Copy only ID and CreatedAt from the stored reference (as Mongo does) so Name/Purpose reflect the caller's update: `reference.ID = old.ID; reference.CreatedAt = old.CreatedAt`.

### F98 — Keystore Put read-then-replace race can return a dangling reference ID

- **Severity:** low | **Category:** bug | **Location:** `core/keystore/mongo.go:69`
- **Status:** open

Put does a FindOne to decide whether a secret exists and then a separate ReplaceOne upsert. Two concurrent Puts for the same (organization, name) can both miss the FindOne, mint different public_ids, and the second ReplaceOne overwrites the first document — the first caller's returned Reference.ID no longer resolves. A truly simultaneous pair can also surface a raw duplicate-key error from the unique (organization_id, name) index instead of converging.

**Evidence excerpt (working tree, 2026-08-04):**
```go
err := s.db.Collection(models.CollectionSecrets).FindOne(ctx, bson.M{"organization_id": organizationID, "name": name}).Decode(&existing)
...
_, err = s.db.Collection(models.CollectionSecrets).ReplaceOne(ctx, bson.M{"organization_id": organizationID, "name": name}, document, options.Replace().SetUpsert(true))
```

**Suggested fix:** Use a single atomic FindOneAndUpdate with $setOnInsert for public_id/created_at and $set for nonce/ciphertext/purpose/updated_at, returning the document's authoritative public_id; or retry on duplicate-key.

### F99 — Prefix rule matching has no segment boundary, so allow rules can silently widen

- **Severity:** low | **Category:** bug | **Location:** `core/policy/policy.go:84`
- **Status:** open

OperationPrefix and DestinationPrefix use raw strings.HasPrefix with no separator awareness. A rule scoped to operation prefix "telemetryos.code" would also match a future "telemetryos.codereview..." operation, and a destination prefix "C123" matches "C1234567". In a deny-wins engine this is only exploitable via over-broad allow/approval rules, but it makes rule authoring a footgun where adding a new operation or channel ID can silently inherit an unrelated allow.

**Evidence excerpt (working tree, 2026-08-04):**
```go
(rule.OperationPrefix == "" || strings.HasPrefix(input.Operation, rule.OperationPrefix)) &&
	(rule.Risk == "" || rule.Risk == input.Risk) &&
	(rule.DestinationPrefix == "" || strings.HasPrefix(input.Destination, rule.DestinationPrefix))
```

**Suggested fix:** Match on exact value or prefix followed by a separator (e.g. equal, or HasPrefix(op, prefix+"/")/prefix+"."), so prefixes only match whole path segments.

### F100 — Editor.Save misclassifies infrastructure failures as ErrDirectiveInvalid

- **Severity:** low | **Category:** bug | **Location:** `core/channelconfig/editor.go:73`
- **Status:** open

Every error from repository.PublishDirective — including Mongo outages, timeouts, and duplicate-key recovery failures — is wrapped in ErrDirectiveInvalid. Callers that branch on ErrDirectiveInvalid (e.g., to show a validation message in the Slack modal) will tell the operator their directive is invalid when the store was simply unavailable, and the %v flattening also strips the original error chain from errors.Is/As inspection.

**Evidence excerpt (working tree, 2026-08-04):**
```go
directive, err := e.repository.PublishDirective(ctx, request.OrganizationID, request.ChannelID, request.Prompt, request.ActorID, request.SourceID)
if err != nil {
	return EditResult{}, fmt.Errorf("%w: %v", ErrDirectiveInvalid, err)
}
```

**Suggested fix:** Only wrap validation errors (returned before any store I/O) in ErrDirectiveInvalid; return store/transport errors wrapped with %w so callers can distinguish retryable failures.

### F101 — Memory admission controller increments a `next` counter that is never read

- **Severity:** low | **Category:** dead-code | **Location:** `core/admission/admission.go:76`
- **Status:** open

The Memory struct carries a `next uint64` field that Admit increments on every successful admission, but nothing ever reads it — reservation IDs come from types.NewID("admit"). It looks like a leftover from an earlier sequential-ID scheme and now only adds noise inside the locked section.

**Evidence excerpt (working tree, 2026-08-04):**
```go
m.next++
id := types.NewID("admit")
```

**Suggested fix:** Delete the `next` field and the `m.next++` statement.

### F102 — No-op delete of "name" from setOnInsert in UpsertContextChannel

- **Severity:** low | **Category:** dead-code | **Location:** `core/orgconfig/store.go:392`
- **Status:** open

The setOnInsert map built at lines 372-390 never contains a "name" key, so `delete(setOnInsert, "name")` in the Name != "" branch is a no-op. It mimics the genuine conflict-avoidance deletes below it (restricted, bot_is_member, participation_mode — which do exist in the map), making the block read like a copy-paste that silently diverged and obscuring which $set/$setOnInsert conflicts are actually possible.

**Evidence excerpt (working tree, 2026-08-04):**
```go
if value.Name != "" {
	delete(setOnInsert, "name")
} else {
	setOnInsert["name"] = ""
}
```

**Suggested fix:** Drop the delete branch and keep only `if value.Name == "" { setOnInsert["name"] = "" }`, or add "name" to the initial map so the delete pattern is uniform with the other fields.

### F103 — Interval Advance steps one interval at a time from an arbitrarily old timestamp

- **Severity:** low | **Category:** inefficiency | **Location:** `core/schedule/schedule.go:71`
- **Status:** open

For legacy fixed-interval specs, Advance loops next = next.Add(interval) until it passes now. If a stored next_run is very old (e.g. a document missing next_run decodes as the zero time, which DueContext's next_run <= now filter happily matches), the loop performs on the order of a billion Add iterations (zero time to today at the 1-minute minimum interval), burning seconds of CPU inside the scheduler tick. Normalize prevents zero times on the Put path only; AdvanceContext reads whatever is in Mongo.

**Evidence excerpt (working tree, 2026-08-04):**
```go
next := current.UTC()
for !next.After(now) {
	next = next.Add(s.Interval)
}
```

**Suggested fix:** Compute the number of missed intervals arithmetically (elapsed/interval + 1) and add once, or fall back to now.Add(interval) when current is zero or older than one window.

### F104 — bounded truncates on a byte boundary and can emit invalid UTF-8

- **Severity:** low | **Category:** bug | **Location:** `core/activity/activity.go:272`
- **Status:** fixed (7456162)

bounded slices the string by bytes (value[:maximum-1]) before appending the ellipsis. Records explicitly may carry bounded public Slack excerpts (Message field), which frequently contain multi-byte UTF-8 (emoji, non-ASCII names); truncation mid-rune produces an invalid UTF-8 string that JSON encoding for the SSE feed replaces with U+FFFD garbage before the ellipsis.

**Evidence excerpt (working tree, 2026-08-04):**
```go
if len(value) <= maximum {
	return value
}
return strings.TrimSpace(value[:maximum-1]) + "…"
```

**Suggested fix:** Back up to a rune boundary before slicing (e.g. decrement the cut index while the byte is a UTF-8 continuation byte, or truncate with []rune conversion for these small maxima).

### F105 — usage.Memory.List and usage.Mongo.List disagree on limit semantics

- **Severity:** low | **Category:** bug | **Location:** `core/usage/usage.go:69`
- **Status:** open

Mongo.List normalizes limit <= 0 (and > 1000) to 100, but Memory.List's loop condition len(result) < limit means limit <= 0 returns an empty slice, and any huge limit is honored unbounded. Callers exercising the memory implementation behind the shared Recorder interface get different behavior than production Mongo, violating the repo's memory-store parity rule and making tests silently pass with an empty result where Mongo would return rows.

**Evidence excerpt (working tree, 2026-08-04):**
```go
for index := len(m.events) - 1; index >= 0 && len(result) < limit; index-- {
```

**Suggested fix:** Apply the same clamp as Mongo.List (limit <= 0 || limit > 1000 -> 100) at the top of Memory.List.

### F106 — Org-unscoped Store.Approve bypass has no production caller

- **Severity:** low | **Category:** dead-code | **Location:** `core/approvals/approvals.go:83`
- **Status:** open

Store.Approve(id, approverID) forwards to approve("", ...) which explicitly skips the organization tenancy check (organizationID != "" guard). No production code calls it (only approvals_test.go); all real paths use ApproveContext with an org. Leaving an exported tenant-check bypass on the approval store is risk without leverage, and MongoStore deliberately offers no equivalent.

**Evidence excerpt (working tree, 2026-08-04):**
```go
func (s *Store) Approve(id, approverID string) (Approval, error) {
	return s.approve("", id, approverID)
}
```

**Suggested fix:** Delete Store.Approve and have the test use ApproveContext with the organization ID, making approve's org parameter mandatory.

### F107 — routines.Scheduler.Trigger, Trigger type, and ErrLoopSuppressed are unused in production

- **Severity:** low | **Category:** dead-code | **Location:** `core/routines/routines.go:245`
- **Status:** open

The event-triggered path (Scheduler.Trigger, the Trigger struct, and ErrLoopSuppressed) has no caller anywhere outside routines_test.go; production wiring in core.go only uses the polled RunDue path, and the event-subscription feature lives in core/triggers instead. This is a vestigial abstraction that duplicates the enqueue Spec literal a third time.

**Evidence excerpt (working tree, 2026-08-04):**
```go
func (s *Scheduler) Trigger(ctx context.Context, routine Routine, trigger Trigger) (jobs.Job, error) {
	if trigger.OriginTag != "" {
		return jobs.Job{}, ErrLoopSuppressed
	}
```

**Suggested fix:** Remove Scheduler.Trigger, the Trigger struct, and ErrLoopSuppressed (and their test), or wire the method to a real event source if one is imminent.

### F108 — Hub.Publish shifts the whole 500-record window on every publish once full

- **Severity:** low | **Category:** optimization | **Location:** `core/activity/activity.go:66`
- **Status:** open

When the buffer is at capacity, Publish does copy(h.records, h.records[1:]), moving 499 Record structs (each with string headers and a Details map pointer) on every published record, all while holding the mutex that Snapshot, Subscribe, and other publishers contend on. The feed receives a record for essentially every classifier decision, delivery, and lifecycle log, so this O(capacity) shift runs continuously in steady state for no benefit.

**Evidence excerpt (working tree, 2026-08-04):**
```go
if len(h.records) == h.capacity {
	copy(h.records, h.records[1:])
	h.records[len(h.records)-1] = record
}
```

**Suggested fix:** Use a true ring buffer (head index + modulo write) so Publish is O(1); Snapshot already iterates and can read the ring in order.

### F109 — models.Summary is unused and diverged from the actual summaries document schema

- **Severity:** low | **Category:** dead-code | **Location:** `models/models.go:218`
- **Status:** open

models.Summary has zero references anywhere in the repo (only the CollectionSummaries constant is used). The summaries collection is actually written and read by core/memory with its own record type carrying scope_key, pinned, status, and restricted (core/memory/store.go:39, core/memory/mongo.go), and the indexes in core/database/database.go:148-149 reference those fields — none of which exist on models.Summary. In a package documented as "MongoDB persistence documents only", this stale struct misrepresents the live schema and invites someone to code against the wrong shape.

**Evidence excerpt (working tree, 2026-08-04):**
```go
type Summary struct {
	ID             bson.ObjectID `bson:"_id,omitempty"`
	PublicID       string        `bson:"public_id"`
	OrganizationID string        `bson:"organization_id"`
	ChannelID      string        `bson:"channel_id,omitempty"`
	Text           string        `bson:"text"`
```

**Suggested fix:** Delete models.Summary (the memory package owns its own document type), or replace it with the real memory-record schema if a canonical model is wanted.

### F110 — Nested Slack segment types lack bson tags while SlackResult's own fields specify them

- **Severity:** low | **Category:** ai-slop | **Location:** `types/delivery.go:48`
- **Status:** open

SlackResult deliberately carries bson tags (segments, allowed_mentions, agent_footer), but SlackSegment and every nested type (SlackTable, SlackCard, SlackApproval, SlackArtifact, ...) have only json tags. Since models.Delivery.Result is stored as `any` and round-tripped with bson.Marshal (core/deliveries/mongo_queue.go:141), persisted documents mix snake_case top-level keys with lowercased-concatenated segment keys such as rowheadercolumnindex, alttext, actionhash, and mediatype, diverging from the documented JSON names; and with no omitempty, every segment stores eight explicit null pointer fields. It works today only because encode and decode share the struct codec — any Mongo-side query, migration, or JSON-based reader of the stored result will mismatch.

**Evidence excerpt (working tree, 2026-08-04):**
```go
type SlackSegment struct {
	Kind     SlackSegmentKind `json:"kind"`
	Text     string           `json:"text,omitempty"`
	Table    *SlackTable      `json:"table,omitempty"`
```

**Suggested fix:** Add bson tags mirroring the json names (with omitempty) to SlackSegment and all nested segment types.

### F111 — MeanLatencyMS averages over fixtures that never call the provider

- **Severity:** low | **Category:** bug | **Location:** `evals/live.go:234`
- **Status:** open

totalLatency accumulates gate.Decide time for all 47 fixtures, but 8 fixtures are SkipLiveProviderCall and complete deterministically in microseconds. Dividing by len(fixtures) therefore understates the real classifier latency by roughly 17%, even though the correct denominator (score.ProviderCalls) is already tracked in the same function. The reported mean_latency_ms is a headline metric for the live eval.

**Evidence excerpt (working tree, 2026-08-04):**
```go
score.MeanLatencyMS = float64(totalLatency.Microseconds()) / 1000 / float64(len(fixtures))
```

**Suggested fix:** Accumulate latency only for fixtures where the provider was called and divide by score.ProviderCalls (guarding against zero).

### F112 — ModelRouting metric double-counts fixtures when provider picks start_background_job

- **Severity:** low | **Category:** bug | **Location:** `evals/live.go:161`
- **Status:** open

A fixture with WantLiveRoutes increments routingTotal at line 153; if the provider's decision for that same fixture is OutcomeStartBackgroundJob, the follow-on check at line 161 increments routingTotal again. Fixtures like deep_direct_mention_thread_reply and security_investigation_strong_background_work then carry double weight in score.ModelRouting, and the second check (strength must be standard/strong) is largely subsumed by the exact-route check that already ran.

**Evidence excerpt (working tree, 2026-08-04):**
```go
if providerCalled && providerDecision.Outcome == types.OutcomeStartBackgroundJob {
			routingTotal++
```

**Suggested fix:** Skip the background-job strength check when the fixture already declared WantLiveRoutes, or fold the minimum-strength rule into containsRoute so each fixture contributes one routing sample.

### F113 — ValidateID has no callers and 7 of 13 typed ID aliases are unused

- **Severity:** low | **Category:** dead-code | **Location:** `types/ids.go:33`
- **Status:** open

ValidateID is defined but never called anywhere in the repository, so ID-shape validation it implies never happens. Additionally the typed aliases OrganizationID, WorkspaceID, ChannelID, MessageID, AttemptID, ReceiptID, and PrincipalID have zero uses (including tests); only ObservationID, SessionID, JobID, DeliveryID, RevisionID, and WorkerID are actually used at boundaries. The unused surface suggests type safety that the codebase does not actually provide.

**Evidence excerpt (working tree, 2026-08-04):**
```go
func ValidateID(value, prefix string) error {
	if !strings.HasPrefix(value, prefix+"_") || len(value) <= len(prefix)+1 {
		return fmt.Errorf("invalid %s ID", prefix)
	}
```

**Suggested fix:** Delete ValidateID and the seven unused aliases, or adopt them at the store/API boundaries where raw strings are currently passed.

---

## Appendix B — 9 findings refuted during verification (do not re-report)

- **F28** `core/workers/local.go:251` — Terminate deletes the active entry before validating Root/AttemptID, orphaning the real process on mismatch. Refuted: The delete-before-validate order exists, but all Terminate callers (worker_codex.go:182/192/200/410) pass the exact Workspace struct returned by ProvisionConnected, so a valid-ID-with-mismatched-Root/AttemptID call cannot occur on any real path and the orphaning scenario is purely hypothetical.
- **F51** `core/pipeline/pipeline.go:574` — Scope-denied context pack skips CompleteDecision, leaving the observation leased. Refuted: buildContextPack can only return errScopeDenied if the envelope's own channel is unauthorized (it is always appended at pipeline.go:1018 and never skipped by the line-1015 filters), but line 610 verified authorization moments earlier, so the path needs a mid-decision policy race and the retry after lease expiry then hits the line 615 branch that calls CompleteDecision(denied, suppressed) — the claimed repeat-every-lease-until-expiry loop cannot occur.
- **F55** `core/classifier/store.go:155` — decisionFromModel silently swallows BSON round-trip errors. Refuted: MongoDecisionStore.Record persists the exact types.ClassificationDecision struct that decisionFromModel round-trips back into, so a BSON marshal/unmarshal failure has no real path absent out-of-band document corruption, and the explicit '_ =' discards are deliberate best-effort decoding rather than an accidental swallow.
- **F56** `core/classifier/service.go:916` — alignmentConflict skips its 48h staleness window when EventTime is zero. Refuted: target.Envelope.EventTime is never zero on any real path — observer.eventTime (store.go:48) falls back to ReceivedAt then now for every persisted observation, heartbeat targets set EventTime: now (pipeline.go:1162), and eval fixtures set it — so the skipped 48h guard in alignmentConflict is unreachable.
- **F57** `core/classifier/openai_classifier.go:708` — withDefaultReaction has a dead branch identical to the default. Refuted: The branch is not a no-op: it heads an else-if chain, and since SourceWriteRequested decisions always carry the non-empty redirect DirectReply, deleting it (the suggested fix) would switch their reaction preference to white_check_mark — the branch deliberately keeps the refusal/redirect on speech_balloon, matching the explicit Reaction set at openai_classifier.go:773.
- **F67** `core/slack/live.go:800` — Timed-out Stop lets a second Start run two concurrent Socket Mode loops. Refuted: Ingress.Start is invoked exactly once per process (Core.Start -> pipeline.StartIngress; cmd/api/main.go starts once and exits after Stop) and no production or test path calls Start after a timed-out Stop, so the double run-loop cannot manifest on any real path.
- **F82** `core/tools/bridge.go:483` — Nil approvalCoordinator handled inconsistently between the tool and trigger approval paths. Refuted: The only production wiring (core.go:212) always passes approvalCoordinator, and the tool path's nil tolerance is deliberate test affordance exercised by bridge_test.go:64's coordinator-less approval round-trip, so the stranded-approval behavior cannot manifest on any real path and the fail-closed trigger check matches documented design.
- **F93** `core/contextpacks/builder.go:89` — Pack expiry is tightened by candidates that never make it into the pack. Refuted: The tightening at builder.go:89-91 is real, but the claimed waste cannot manifest: packs are built fresh per decision revision and never reused or rebuilt based on ExpiresAt — the field only drives Mongo TTL retention (database.go:130) and job/delivery expiry (pipeline.go:742,811), and expiring derived data no later than considered sources is this repo's deliberate conservative retention posture.
- **F95** `core/core.go:707` — Stop orders the startup backfill shutdown after pipeline.Stop, unlike the refresh loop. Refuted: RecoverContextEnvelope and ImportContextEnvelope only touch Mongo-backed stores (Scopes, Sessions, Observations, audit) that remain connected until database.Disconnect runs after stopContextBackfill at core.go:710-735, and none use the ingress/workers/harness that pipeline.Stop tears down, so an observation accepted in the ordering window is simply durable pending state recovered by the polling workers at next startup — exactly the documented Mongo-authoritative, workers-disposable design, with no manifestable harm.
