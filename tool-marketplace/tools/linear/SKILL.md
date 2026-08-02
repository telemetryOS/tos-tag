---
name: telemetryos-linear-tool
description: Invoke the reviewed TelemetryOS Linear helper through tos_tag_tool after following the injected Linear workflow skill.
---

Use `tool_id=telemetryos.linear`. Use `operation_id=read` for `get`, `comments`, `whoami`, `mine`, `list`, `search`, `history`, `members`, and `download`. Use `operation_id=write` for `set-state`, `comment`, `update`, `start`, `create`, and `upload`; writes require Slack approval. Pass the helper verb and its flags as the `arguments` array. Never request, print, or pass a credential.

Disposable workers have no shared file path. For model-authored text, use
inline `--body`, `--title`, `--description`, or `--comment` arguments rather
than the corresponding `*-file` flags. Inline titles are limited to 512
characters and one line, descriptions to 100,000 characters, and comments to
20,000 characters. File flags remain for trusted local callers only.

```json
{"tool_id":"telemetryos.linear","operation_id":"read","arguments":["get","--issue","ENG-1234"]}
{"tool_id":"telemetryos.linear","operation_id":"write","arguments":["comment","--issue","ENG-1234","--body","Concise PM-readable update."]}
```
