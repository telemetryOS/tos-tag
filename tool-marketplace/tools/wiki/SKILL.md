---
name: telemetryos-wiki-tool
description: Invoke the reviewed TelemetryOS Agent Wiki CLI through tos_tag_tool after following the injected wiki authoring skill.
---

Use `tool_id=telemetryos.wiki`. This capability is page CRUD only:

- `operation_id=read` permits page indexes/lists/trees, `get`, `search`, revision lists, and page URLs;
- `operation_id=write` permits only page `put`, `append`, `restore`, and `revert`; `put` and `append` require an inline `--body` and cannot read files or stdin; and
- `operation_id=delete` permits only the recoverable 30-day page soft-delete `rm` and always requires exact-action approval.

Namespace operations are unavailable, including namespace reads. Asset upload, publish-from-file, cascading move/rename, activity inspection, generic undo, and every admin operation are unavailable. Do not request them. Pass the permitted Wiki verb and flags as the `arguments` array. Page reads and ordinary page writes execute without per-action Slack approval. The gateway always enforces the job capability, tool/version/operation allowlist, exact argv, environment allowlist, timeout, output bound, kill switch, and tamper-evident audit receipts. Never request, print, or pass a credential.

Disposable workers do not have the source checkout or a shared local file. When creating or updating content obtained through `telemetryos.code` or composed in the current turn, pass the complete page body with `put ... --body <BODY> --md --json`; never invent `/workspace/...` or another filename. The gateway commits the exact inline body in its audit receipt.

The contract in this skill is complete. Do not call `--help`, `put --help`, or any other discovery probe through `operation_id=write`; proceed directly to the requested typed operation. For a source-derived Markdown page, use this shape:

```json
{"tool_id":"telemetryos.wiki","operation_id":"write","arguments":["put","artifacts/tos-tag-architecture","--title","tos-tag architecture","--tags","tos-tag,architecture,slack","--note","Published from the current source.","--body","# tos-tag architecture\n\nComplete Markdown body","--md","--json"]}
```

Every successful `get` through `operation_id=read` returns the full page JSON,
including `body_html` and the server-derived opaque human `url`, even if the
arguments omit `--json`. If you reference that page in Slack, use the exact
returned `url` as a descriptive clickable link. Never present its
`namespace/slug` lookup identifier as a citation and never reconstruct a page
URL.

## Long-form Slack delivery

When the requested answer becomes genuinely expository or document-shaped,
use this page capability proactively rather than filling Slack with a wall of
text. About 20,000 visible characters is a soft planning signal, not a strict
cutoff: publish earlier when the work has many sections, extensive evidence,
durable reference value, or navigation that belongs in a document. Keep short
and medium answers in Slack.

Publish Markdown under `artifacts/<descriptive-slug>`. Before replacing an
existing stable slug, read it and update only when it is clearly the same
artifact; otherwise choose a more specific slug. After a successful `put`, use
only the exact HTTPS `url` in its JSON response. The final Slack result should
contain a concise synopsis plus an `artifact` segment with that URL and
`media_type` `text/html`. Never guess a Wiki URL or claim publication after an
error. If the write is unavailable or fails, compress the useful result into
Slack and say that no Wiki artifact was created.

For a read-only search, use exactly:

```json
{"tool_id":"telemetryos.wiki","operation_id":"read","arguments":["search","tos-tag","--limit","20"]}
```

For an explicitly requested recoverable page delete, use exactly this shape;
the gateway will require exact-action approval before execution:

```json
{"tool_id":"telemetryos.wiki","operation_id":"delete","arguments":["rm","artifacts/obsolete-page"]}
```

The search term is positional. Do not invent `--query`, `--json`, `status`, or
authentication-probe flags; the reviewed CLI does not support them. A successful
read proves that the server-side binding was usable. If only a result count is
needed, count the returned rows after the single header line and do not expose
the row contents.
