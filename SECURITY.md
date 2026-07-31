# Security

tos-tag processes workplace communications and is designed to execute tools.
Treat Slack content, model output, repository files, skills, tool results, and
marketplace artifacts as untrusted input.

## Current initiative

Slack defaults to a deterministic stub; fake inference remains available for
tests and evals. Live Slack, credentialed provider calls, and connector effects
are opt-in integration modes. No Slack token, provider credential, connector
secret, or live customer content is required or permitted in normal tests,
fixtures, or behavioral evals.

## Non-negotiable boundaries

- A message, model, skill, or tool result may request an action; none authorizes
  it.
- Tenant and scope predicates are required before data retrieval.
- Ambient observations cannot authorize writes.
- Workers receive no long-lived credentials or MongoDB connection string.
- Tool secrets may enter only the exact reviewed subprocess that declares them.
- Output destinations derive from admitted server state, never model output.
- Every sensitive transition is fenced by live lease and kill-switch state.
- Audit receipts contain redacted metadata, not copies of secret/message data.

Report security issues privately to the repository owners. Do not include live
credentials, private Slack excerpts, or customer data in an issue.
