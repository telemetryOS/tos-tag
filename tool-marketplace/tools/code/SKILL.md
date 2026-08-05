---
name: telemetryos-code-tool
description: Inspect the Aion-managed TelemetryOS source tree through a reviewed, read-only tos_tag_tool capability.
---

Use `tool_id=telemetryos.code` and `operation_id=read` when a task needs current
source evidence. The server refreshes only the requested TelemetryOS
repository into an immutable default-branch snapshot, records the exact commit
and fetch time, and then performs bounded exact or semantic inspection against
that snapshot. The worker receives neither the checkout nor GitHub credentials
and cannot select a remote, branch, or source revision.

The capability cannot mutate a source checkout, run a generic shell command,
traverse outside the configured source roots, or read local credential/runtime
files. Its restricted GitHub access can only refresh a repository's approved
`telemetryOS` origin into server-owned snapshot state. Semantic search runs
offline with a pinned local model and stores its source-bearing index only in
the protected server cache. Never ask for or guess those server-owned paths.

Supported argument arrays:

- `['repos']`
- `['freshness', '<repository>']`
- `['files', '<relative-directory>', '<limit 1-500>']`
- `['search', '<fixed string>', '<relative-directory>', '<limit 1-500>']`
- `['semantic-search', '<repository>', '<natural-language query>', '<results 1-8>', '<snippet lines 1-40>']`
- `['read', '<relative-file>', '<start line>', '<end line>']` (at most 400 lines)
- `['versions', '<repository>', 'go']`

Use `freshness` when the question is specifically about source currency. Use
`semantic-search` once when the concept is known but its exact symbol or phrase
is not, then use one exact `read` for the decisive lines. Prefer fixed-string
`search` when an exact route, symbol, log text, or configuration key is known.
Every source operation verifies a receipt no older than five minutes and
refreshes only that repository when necessary. File references in an answer
should use the relative path and returned line numbers, and source-backed
claims should name the returned commit when freshness matters.

If a requester asks to edit, implement, patch, commit, push, merge, deploy, or
otherwise change TelemetryOS source, do not attempt the change. Follow the
injected `code-change-intake` skill: explain that source access is read-only and
direct the requester to a Linear bug for broken existing behavior or a Linear
feature for new or changed behavior.

Example:

```json
{"tool_id":"telemetryos.code","operation_id":"read","arguments":["search","WorkerConcurrency","tos-tag","50"]}
```
