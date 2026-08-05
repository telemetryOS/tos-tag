---
name: telemetryos-linear-tool
description: Invoke the reviewed TelemetryOS Linear helper through tos_tag_tool after following the injected Linear workflow skill.
---

Use `tool_id=telemetryos.linear`. Use `operation_id=read` for `get`, `comments`, `whoami`, `mine`, `list`, `search`, `history`, `members`, and `download`.

Use `operation_id=intake` only while following the injected `bug` or `feature` workflow together with `linear-issue-manager`. It executes without per-action approval and is limited to:

- creating a Triage, unassigned Bug or Feature at Medium or High priority;
- adding an evidence comment to an existing issue; and
- updating only the description, Bug/Feature label, and mutually exclusive suitability labels/comment.

Use `operation_id=write` for every other `set-state`, `comment`, `update`, `start`, `create`, or `upload`; generic writes require Slack approval. Pass the helper verb and its flags as the `arguments` array. Never request, print, or pass a credential.

Disposable workers have no shared file path. For model-authored text, use
inline `--body`, `--title`, `--description`, or `--comment` arguments rather
than the corresponding `*-file` flags. Inline titles are limited to 512
characters and one line, descriptions to 100,000 characters, and comments to
20,000 characters. File flags remain for trusted local callers only.
After a title or description write, the helper verifies durable content with a
fresh Linear issue query rather than trusting the mutation payload. Description
verification preserves Markdown exactly while tolerating only CRLF/CR line-end
normalization and trailing newline removal; output remains content-free.

```json
{"tool_id":"telemetryos.linear","operation_id":"read","arguments":["get","--issue","ENG-1234"]}
{"tool_id":"telemetryos.linear","operation_id":"intake","arguments":["create","--title","Specific product outcome","--description","Complete issue body","--priority","3","--label","Feature"]}
{"tool_id":"telemetryos.linear","operation_id":"write","arguments":["comment","--issue","ENG-1234","--body","Concise PM-readable update."]}
```
