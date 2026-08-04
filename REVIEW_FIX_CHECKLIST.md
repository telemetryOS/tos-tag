# tos-tag review remediation checklist

Source: [`REVIEW_FINDINGS.md`](REVIEW_FINDINGS.md), reviewed against `main` at
`307caee`. This ledger tracks remediation; the source review retains the full
evidence and suggested fixes.

## Completion contract

A finding is complete only when:

- the current source claim has been re-verified;
- the smallest contract-preserving fix is implemented;
- a focused regression test covers the failure mode;
- the relevant package tests pass;
- `REVIEW_FINDINGS.md` records `fixed (<commit>)` after commit; and
- the final batch passes `make verify`.

If verification refutes a finding, move it to the refuted section with concise
source evidence instead of changing code to satisfy the report.

Current implementation checkpoint (full tests, race detector, vet, behavioral
eval, gosec, govulncheck, and focused real-Mongo tests passing in implementation
commit `7456162`): `NEW-1`, `F2`, `F3`, `F19`, `F5`, `F6`, `F7`,
`F41`, `F8`, `F9`, `F24`, `F26`, `F18`, `F1`, `F16`, `F25`, `F38`, `F40`,
`F30`, `F32`, `F20`, `F31`, `F15`, `F23`, `F21`, `F22`, `F4`, `F35`, `F36`,
`F34`, `F39`, `F13`, `F10`, and `F11` (covered by the `F32` reconciliation
query fix), plus `F12`, `F14`, `F17`, `F27`, `F29`, `F33`, `F37`, `F42`,
`F43`, `F44`, `F45`, `F46`, `F47`, `F48`, `F49`, `F52`, and `F104`.

## Batch 1 — recovery and production data loss

- [x] `NEW-1` Recover actionable top-level direct messages missed while offline.
- [x] `F2` Make bounded catch-up progress without wedging later channels or skipping gaps.
- [x] `F3` Persist all supported Mongo job transition mutations, including resolved routing.
- [x] `F19` Recover offline replies in existing Tag threads whose roots predate the watermark.
- [x] `F6` Reconcile a succeeded job whose final delivery enqueue failed.
- [x] `F7` Reserve observation output atomically before enqueueing user-visible work.
- [x] `F41` Make admission reservation completion and active-count release recoverable.
- [x] `F5` Persist and verify `/tag-mode` audit receipts.

## Batch 2 — worker lifecycle and output safety

- [x] `F8` Treat transient job-read failures as retryable rather than revocation.
- [x] `F9` Clean up sessions on every `runHarness` early return.
- [x] `F24` Remove bounded-channel sends from the worker-session mutex critical section.
- [x] `F26` Terminate provisioned workers on prompt validation failures.
- [x] `F18` Validate unlabeled Slack angle-bracket URLs against the safe-scheme policy.
- [x] `F1` Preserve legitimate fenced code in plain-text model output.
- [x] `F16` Preserve literal identifier punctuation in rich-text table cells.
- [x] `F25` Report cumulative per-turn token usage.
- [x] `F38` Make audit content commitments durable and verifiable across restarts.
- [x] `F40` Require or resolve the Slack bot user ID in Socket Mode.

## Batch 3 — production query behavior and operator correctness

- [x] `F30` Add indexes matching job claim, recovery, heartbeat, and public-ID queries.
- [x] `F32` Stop loading every job on each reconciliation tick.
- [x] `F20` Bound bot-membership discovery without failing on large public-channel inventories.
- [x] `F31` Add indexes matching observation claim and public-ID mutation queries.
- [x] `F15` Bound and filter decision-list queries.
- [x] `F23` Remove unauthenticated status-path full-collection scans.
- [x] `F21` Make learned-note listing use the persisted schema.
- [x] `F22` Return recent work newest-first with bounded tenant lists.
- [x] `F4` Allow forgotten memory scopes to be relearned under the tombstone contract.
- [x] `F35` Prevent generated memory from overwriting concurrent operator corrections.
- [x] `F36` Remove unnecessary projection round trips for ordinary new messages.
- [x] `F34` Align memory and Mongo observation projection/claim semantics.
- [x] `F39` Consolidate background-work scope authorization without widening authority.
- [x] `F13` Generalize external public-source detection beyond a single vendor.
- [x] `F10` Match incident terms on token boundaries.

## Batch 4 — confirmed lower-severity maintenance

- [x] `F11` Stop full job scans on every reconciliation poll.
- [x] `F12` Keep policy-correction reactions inside the configured allowlist.
- [x] `F14` Consolidate social-reply selection behavior.
- [x] `F17` Align exhausted-delivery handling in Mongo and memory queues.
- [x] `F27` Preserve the graceful worker-termination window.
- [x] `F29` Align bounded marketplace file reads with the tools implementation.
- [x] `F33` Remove or integrate production-dead job operations.
- [x] `F37` Remove or integrate the production-dead conversational-search package.
- [x] `F42` Make the memory organization store fail closed like Mongo.
- [x] `F43` Preserve an active directive when replacement activation fails.
- [x] `F44` Prevent duplicate keyless audit receipts during CAS retries.
- [x] `F45` Consolidate duplicated routine and trigger behavior.
- [x] `F46` Scope in-memory routine IDs by organization.
- [x] `F47` Batch expired derivation deletion.
- [x] `F48` Remove the misplaced delivery `WriterActive` field.
- [x] `F49` Share response-profile defaults with live evals.
- [ ] `F50` Share fixture-contract validation between deterministic and live evals.
- [x] `F52` Make activity text truncation UTF-8 safe.
- [ ] `F53` Remove dead progress helpers and vestigial parameters.
- [ ] `F54` Consolidate self-authored and integration-authored context import.
- [ ] `F58` Remove the dead initiative-expression term.
- [ ] `F59` Avoid repeated full source scans during one classification.
- [ ] `F60` Normalize plain-text fallback output consistently with JSON output.
- [ ] `F61` Remove the unused approval-value boolean result.
- [ ] `F62` Consolidate approval-status titles.
- [ ] `F63` Share notice tone/icon validation and rendering data.
- [ ] `F64` Precompile artifact URL regular expressions.
- [ ] `F65` Make mrkdwn splitting consistently rune-based.
- [ ] `F66` Preserve a real zero-time fallback for Slack events.
- [ ] `F68` Consolidate Slack delivery metadata pagination.
- [ ] `F69` Share bot-mention detection across live and history ingestion.
- [ ] `F70` Remove the redundant atomic increment under the mutex.
- [ ] `F71` Remove stale routines/directives page-view entries.
- [ ] `F72` Remove the nonexistent trigger `event_type` UI field.
- [ ] `F73` Share mutation-envelope decoding.
- [ ] `F74` Surface inject, preview, and keystore form errors to operators.
- [ ] `F75` Use required-organization validation in record listing.
- [ ] `F76` Remove the unreachable empty-row check.
- [ ] `F77` Drain worker events deterministically before terminal errors.
- [ ] `F78` Remove the dead Wiki provenance tool parameter.
- [ ] `F79` Consolidate tool validation/result publication without losing session IDs.
- [ ] `F80` Bind server-initiated tool calls to session cancellation.
- [ ] `F81` Remove redundant NUL checks.
- [ ] `F83` Remove or integrate dead marketplace resolution machinery.
- [ ] `F84` Share marketplace snapshot walking and hashing.
- [ ] `F85` Remove the zero-value gateway request wrapper.
- [ ] `F86` Compare Mongo index definitions structurally instead of swallowing option drift.
- [ ] `F87` Surface persisted job decode failures.
- [ ] `F88` Align cancellation steering epochs across queue implementations.
- [ ] `F89` Move late-candidate gating out of the memory test-double file.
- [ ] `F90` Consolidate situation fact/restricted-signal upserts.
- [ ] `F91` Make intelligence summaries UTF-8 safe.
- [ ] `F92` Use the validated, trimmed summarizer base URL.
- [ ] `F94` Remove the unused rune tokenizer.
- [ ] `F96` Consolidate unambiguous environment overrides with the config loader.

## Batch 5 — verify before changing code

- [ ] `F97` Verify existing-secret purpose update behavior.
- [ ] `F98` Verify the keystore replacement race and reference lifetime.
- [ ] `F99` Verify prefix-rule segment-boundary behavior.
- [ ] `F100` Verify directive editor infrastructure-error classification.
- [ ] `F101` Verify the unused memory-admission counter.
- [ ] `F102` Verify the context-channel `$setOnInsert` no-op.
- [ ] `F103` Verify pathological interval advancement cost.
- [x] `F104` Verify activity truncation UTF-8 behavior.
- [ ] `F105` Verify usage-list limit conformance.
- [ ] `F106` Verify the production reachability of unscoped approval.
- [ ] `F107` Verify production reachability of routine trigger APIs.
- [ ] `F108` Verify activity-hub window-shift cost.
- [ ] `F109` Verify the unused summary model and schema drift.
- [ ] `F110` Verify BSON behavior of nested Slack segment types.
- [ ] `F111` Verify live-eval latency denominator semantics.
- [ ] `F112` Verify live-eval model-routing double counting.
- [ ] `F113` Verify typed-ID and validation reachability.

## Refuted — no code change

- [x] `F28` Refuted: production callers retain the exact provisioned workspace identity.
- [x] `F51` Refuted: the claimed persistent decision-lease loop is not reachable.
- [x] `F55` Refuted: decision decode errors require out-of-band corruption.
- [x] `F56` Refuted: persisted observation event time is never zero.
- [x] `F57` Refuted: the reaction branch deliberately blocks a later default.
- [x] `F67` Refuted: ingress is started once per process.
- [x] `F82` Refuted: production always wires the approval coordinator.
- [x] `F93` Refuted: conservative pack expiry is intentional retention behavior.
- [x] `F95` Refuted: shutdown ordering preserves Mongo-authoritative pending work.
