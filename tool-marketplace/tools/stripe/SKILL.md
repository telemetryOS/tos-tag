---
name: stripe-billing-tool
description: Read and manage approved Stripe billing data through a reviewed wrapper around the official Stripe CLI with separate read, write, and delete operations.
---

# Stripe Billing Tool

Call `tos_tag_tool` with `tool_id=stripe.billing`, the active `stripe` skill in
`skill_names`, and one reviewed operation:

- `read`: `get <documented-/v1-or-/v2-path> [reviewed options]`;
- `write`: `post <documented-/v1-or-/v2-path> --idempotency <key>
  [reviewed options]`; or
- `delete`: `delete <documented-/v1-or-/v2-path> --idempotency <key>
  [reviewed options]`.

Reviewed options are repeated `--data <form-field=value>`, repeated `--expand
<field>`, bounded pagination, connected-account/context selection, an API
version, and mutation idempotency. The wrapper invokes the official `stripe`
CLI with an isolated home and control-plane-owned `--live`, requires a live
secret or restricted `STRIPE_API_KEY` from the private keystore, validates
arguments and API paths, and returns JSON. It excludes arbitrary CLI
flags, URLs, login/config/key management, plugins, fixtures, webhook listeners,
event triggers, and Dashboard actions.

Use `read` for account identity and bounded resource retrieval. Use `write` or
`delete` only for an explicit external mutation, then freshly read back the
result when possible. Never request or reveal `STRIPE_API_KEY`, full payment
credentials, or unrelated customer and financial data.
