---
name: telemetryos-linear-tool
description: Invoke the reviewed TelemetryOS Linear helper through tos_tag_tool after following the injected Linear workflow skill.
---

Use `tool_id=telemetryos.linear`. Use `operation_id=read` for `get`, `comments`, `whoami`, `mine`, `list`, `search`, `history`, `members`, and `download`. Use `operation_id=write` for `set-state`, `comment`, `update`, `start`, `create`, and `upload`; writes require Slack approval. Pass the helper verb and its flags as the `arguments` array. Never request, print, or pass a credential.
