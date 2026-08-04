---
name: telemetryos-wiki-tool
description: Invoke the reviewed TelemetryOS Agent Wiki through the typed tos_tag_wiki interface after following the injected wiki authoring skill.
---

Use `tos_tag_wiki`. Supply semantic fields; Go validates them and constructs
the reviewed CLI arguments. The generic `tos_tag_tool` rejects Wiki calls. This
capability is page CRUD only:

- `map`, `ls`, `tree`, `get`, `search`, `revs`, and `url` are reads;
- `put`, `append`, `restore`, and `revert` are writes; `put` and `append` require an inline `body` and cannot read files or stdin; and
- `rm` is the recoverable 30-day page soft-delete and always requires exact-action approval.

Namespace operations are unavailable, including namespace reads. Asset upload, publish-from-file, cascading move/rename, activity inspection, generic undo, and every admin operation are unavailable. Do not request them. Page reads and ordinary page writes execute without per-action Slack approval. The gateway always enforces the job capability, tool/version/operation allowlist, Go-generated exact argv, environment allowlist, timeout, output bound, kill switch, and tamper-evident audit receipts. Never request, print, or pass a credential.

Disposable workers do not have the source checkout or a shared local file. When creating or updating content obtained through `telemetryos.code` or composed in the current turn, pass the complete page through `page_reference`, `title`, `body`, `tags`, `note`, and `format`; never invent `/workspace/...` or another filename. The gateway commits the exact typed action in its audit receipt.

The contract in this skill is complete. There is no arbitrary argv or help-probe surface. For a source-derived Markdown page, use this shape:

```json
{"skill_names":["wiki"],"operation":"put","page_reference":"artifacts/tos-tag-architecture","title":"tos-tag architecture","body":"# tos-tag architecture\n\nComplete Markdown body","tags":["tos-tag","architecture","slack"],"note":"Published from the current source.","format":"markdown"}
```

Every successful `get` returns the full page JSON,
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
{"skill_names":["wiki"],"operation":"search","query":"tos-tag","limit":20}
```

For an explicitly requested recoverable page delete, use exactly this shape;
the gateway will require exact-action approval before execution:

```json
{"skill_names":["wiki"],"operation":"rm","page_reference":"artifacts/obsolete-page"}
```

Do not invent fields outside the published typed schema. A successful read
proves that the server-side binding was usable. If only a result count is
needed, count the returned rows after the single header line and do not expose
the row contents.
