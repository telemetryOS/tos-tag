# Claude Fable adversarial architecture review

Date: 2026-07-30
Model: Claude Fable (`fable`)
Effort: high
Mode: headless, read-only (`Read`, `Grep`)
Reviewed document: [architecture.md](architecture.md)

## Review method

Claude Fable was asked to assume hostile Slack content and users, compromised
marketplace content, a malicious tool helper, worker escape attempts,
crash/retry/reordering, revoked membership, concurrent steering, secret
exfiltration, and operator error. The requested output was limited to concrete
security, authorization, reliability, privacy, state-machine, operability, and
internal-consistency defects.

The first `xhigh` run was stopped after it stalled. A connectivity probe
confirmed the `fable` model alias, and the successful review was rerun at high
effort. Claude had no editing tools. The primary agent independently checked the
findings before changing the architecture.

## Fable verdict

Fable returned **Go for Phase 0**, provided the durable Phase-0 contracts were
fixed before implementation. It identified two P0, twelve P1, ten P2, and one
P3 findings. All were accepted as real architecture gaps; a few recommendations
were adapted to the project's existing boundaries.

## Findings and dispositions

| ID | Finding | Disposition and architecture change |
| --- | --- | --- |
| P0-1 | Tool gateway trusted worker-supplied identity, requester, policy, destination, and credential fields | Accepted. The worker payload now carries only capability, tool/operation, typed arguments, and idempotency key. All authority is derived server-side from capability claims and live state. |
| P0-2 | Cross-channel search authorized the requester but not the channel receiving the answer | Accepted. Search now intersects the complete destination audience and source quote-out policy before querying. Requester-private delivery is deferred. |
| P1-1 | A policy revision could create a second job for one observation | Accepted. Canonical job keys exclude policy revisions and an atomic observation-level `output_produced` guard prevents duplicate output. |
| P1-2 | Task capabilities were not fenced by the live job lease | Accepted. Model and tool capabilities carry the lease/fencing token and steering epoch and are checked against live Mongo state on every call. |
| P1-3 | Interrupt did not fence an in-flight external action | Accepted. Interrupt increments the steering epoch, revokes old capabilities, drains actions, and records uncertain effects before admitting a new prompt. |
| P1-4 | A shell helper could bypass gateway destination and redirect enforcement | Accepted. Tool subprocesses have no direct route and must use a manifest-enforcing egress proxy; redirects default to disabled and every target is reauthorized. |
| P1-5 | Approval was not bound to the exact arguments shown to the approver | Accepted. Approval and external-action keys include tool version, operation, canonical typed arguments, and destination. Write requesters cannot self-approve absent audited break glass. |
| P1-6 | Marketplace scope bindings could accidentally follow a mutable ref | Accepted. Bindings reference exact immutable content hashes; changed refs degrade the marketplace and require explicit promotion. |
| P1-7 | Marketplace contract-test execution boundary was ambiguous | Accepted. The control plane validates test presence/shape only; execution is restricted to CI or a disposable no-secret sandbox. |
| P1-8 | Cross-channel requester visibility required Slack scopes and cache behavior not specified | Accepted. Phase 1 now declares membership scopes, short TTL/revalidation, and current-channel-only failure behavior. |
| P1-9 | The kill switch was named but had no state or propagation contract | Accepted. Durable scoped `pause_speech` and `abort_all` modes are checked at decision, claim, gateway, and delivery boundaries with a five-second maximum cache. |
| P1-10 | Multi-instance audit append ordering was left open while Phase 0 depended on it | Accepted. Audit append uses CAS on the organization chain head; security-relevant transitions fail closed when append fails. |
| P1-11 | Phase 0 allowed a real ambient classifier before provider/data policy existed | Accepted. Phase 0 is deterministic/fake-only; no Slack content reaches a real model provider. |
| P1-12 | Agent-authored notes created a persistent prompt-injection channel | Accepted. Agent note revisions remain `pending_review`, excluded from context/search until human activation, and are rendered as delimited untrusted reference data. |
| P2-1 | `received_seq` allocation and permanent-gap handling were unspecified | Accepted. Sequence allocation uses an atomic per-channel counter and a bounded missing-predecessor timeout. |
| P2-2 | Slack membership resolution was incorrectly placed inside the acknowledgement path | Accepted. Ingress persists `scope_state: unresolved`, acknowledges, and resolves asynchronously before decision eligibility. |
| P2-3 | The job state machine lacked cancellation/failure/reconciliation edges and finite retries | Accepted. Missing edges, `needs_reconciliation`, and `max_attempts` were added. |
| P2-4 | Resuming approval after releasing a worker was undefined | Accepted. Approval now resumes from a durable exact-action reference/result rather than a permission event in a destroyed OpenCode session. |
| P2-5 | Public SHA-256 payload hashes could reveal low-entropy deleted content | Accepted. Receipts use organization/retention-epoch keyed HMAC commitments with documented key destruction semantics. |
| P2-6 | Source deletion did not propagate to transcript, memory, notes, and other derived copies | Accepted. A source-derivation index drives idempotent purge/redaction fan-out; already-delivered Slack content is explicitly outside that boundary. |
| P2-7 | Retained channel history could remain searchable after bot removal | Accepted. `ingestion_revoked_at` excludes the channel from active search unless an explicit audited policy permits historical search. |
| P2-8 | Loopback OpenCode inside a container was unreachable from the control plane | Accepted with adaptation. OpenCode binds a per-worker private interface with firewall-limited control-plane access and no published port. |
| P2-9 | A user-scoped secret could become a confused deputy for ambient or routine work | Accepted. It resolves only when that user is the explicit requester of record. |
| P2-10 | Channel-directive activation was not classified as behavior/access expanding | Accepted. Activation requires diff/impact preview, confirmation, and a dedicated receipt. |
| P3-1 | Permanent Slack delivery failure could silently swallow a completed mention | Accepted. `delivery_abandoned` alerts operators and marks the job `completed_undelivered`. |

## Controls Fable found adequate

Fable explicitly rejected concerns where the architecture already had a strong
control, including:

- durable insert before Slack acknowledgement;
- self-output and causal-loop suppression;
- no acknowledgement when MongoDB is unavailable;
- output destination fixed by the admitted job rather than the model;
- OpenCode local state treated as disposable correlation state;
- hooks and executable OpenCode plugins disabled by default;
- host Docker socket, inherited control-plane ENV, and metadata endpoints denied;
- SSRF/CIDR/TLS/webhook controls at the gateway;
- management authentication and CSRF for the initial role model;
- bounded metric cardinality;
- no false exactly-once claim for external systems;
- shadow-before-live and read-only-first delivery phases; and
- executable tool files absent from the OpenCode worker filesystem.

## Result

The accepted findings were incorporated into architecture version 0.2 and are
preserved by the later version 0.3 cross-channel intelligence design. The
Phase 0 verdict remains **Go**, but only with the
new idempotency, sequencing, kill-switch, audit, finite-retry, note-review, and
keyed-receipt contracts treated as implementation gates. Phase 1 is gated on
server-derived gateway authority, live lease/steering fencing, destination-
audience search authorization, explicit Slack membership scopes, mandatory tool
egress enforcement, and the private OpenCode control network.
