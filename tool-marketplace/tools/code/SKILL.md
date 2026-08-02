---
name: telemetryos-code-tool
description: Inspect the Aion-managed TelemetryOS source tree through a reviewed, read-only tos_tag_tool capability.
---

Use `tool_id=telemetryos.code` and `operation_id=read` when a task needs current
source evidence. This capability can list repositories and files, perform a
fixed-string search, or read a bounded line range. It cannot write files, run
shell commands, access the network, traverse outside the configured source
root, or read local credential/runtime files. Never ask for or guess the
server-owned source root. Credential ledgers, private agent/tool state,
symlinks, and path traversal are also unavailable.

Supported argument arrays:

- `['repos']`
- `['files', '<relative-directory>', '<limit 1-500>']`
- `['search', '<fixed string>', '<relative-directory>', '<limit 1-500>']`
- `['read', '<relative-file>', '<start line>', '<end line>']` (at most 400 lines)

Use `search` first to locate symbols and then `read` the narrow relevant ranges.
File references in an answer should use the relative path and returned line
numbers. Do not repeatedly probe a path the tool reports as restricted.

If a requester asks to edit, implement, patch, commit, push, merge, deploy, or
otherwise change TelemetryOS source, do not attempt the change. Follow the
injected `code-change-intake` skill: explain that source access is read-only and
direct the requester to a Linear bug for broken existing behavior or a Linear
feature for new or changed behavior.

Example:

```json
{"tool_id":"telemetryos.code","operation_id":"read","arguments":["search","WorkerConcurrency","tos-tag","50"]}
```
