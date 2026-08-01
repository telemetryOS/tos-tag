# TelemetryOS container workspace

This persistent workspace is shared by the operator-facing Codex shell and
the tos-tag control plane. Treat `/workspace` as an umbrella directory, not a
single repository.

## Layout

- `/workspace/projects/tos-tag` is the tos-tag control-plane repository.
- `/workspace/code` is Aion's `developer_path`; `aion sync` owns the
  TelemetryOS repositories beneath it.
- `/workspace/skills/telemetryos-agent-skills` is the headless TelemetryOS skill
  source.
- `/workspace/skills/tag-agent-skills` is the tos-tag `base` skill source.
- `/workspace/tools/Aion` is the pinned source used to build the Aion CLI.
- `/workspace/tools/telemetry-otel-fetch`, `Device-Log-Analyzer`, and
  `TelemetryOS-Mongo-Fetch` are pinned helper sources; their binaries are in
  `/home/tag/.local/bin`.
- `/workspace/state` contains local logs and disposable-worker roots.

## Working rules

- Read the target repository's `AGENTS.md` and `CLAUDE.md` before editing.
- Use `aion sync` to clone or refresh the Aion-managed code set. Aion skips
  dirty repositories; never reset or clean user work to force synchronization.
- Use `gh` for GitHub operations. Authentication lives in the persistent
  container home and must never be copied into repositories, prompts, logs, or
  worker environments.
- Put tos-tag-specific skills and Bash helper source in
  `/workspace/skills/tag-agent-skills`, following that repository's guidance.
- Keep the headless TelemetryOS workflow plugin in
  `/workspace/skills/telemetryos-agent-skills`.
- Behavioral skill scripts are not executable worker authority. Reviewed
  helper bundles live in `/workspace/projects/tos-tag/tool-marketplace` and
  run only through `tos_tag_tool`. The model must never select secret
  references; tos-tag derives manifest-declared bindings from its encrypted
  organization keystore and injects them only into the exact subprocess.
- The reviewed catalog contains Linear and Agent Wiki reads/writes, OTel and
  Mongo reads, device-log reads/writes, and `telemetryos.code` reads. Non-read
  operations require an exact Slack approval. `telemetryos.code` is the only
  source capability and provides bounded repository/file listing,
  fixed-string search, and line reads below `/workspace/code`; it is not a
  shell or write surface.
- Populate the ignored mode-0600 `runtime.env` with `make sync-tool-env` on the
  host. It imports only the documented Linear, Wiki, SigNoz, and DLA names and
  never prints their values. A host path in `TAG_AION_DEVELOPER_PATH` is
  intentionally overridden after the file is sourced so the container always
  uses `/workspace/code`; HTTP, Mongo, log, Codex, skill, and tool locations are
  similarly replaced with container-owned values. Never source that file in a
  general worker shell.
- Slack-triggered App Server workers remain disposable, tool-minimized, and
  separate from this durable operator workspace. The tos-tag control plane,
  sandbox, and capability gateway enforce the boundary together. Workers do
  not receive `/workspace/code`, the operator home, connector secrets, or the
  capability token.
- This environment intentionally has no host Docker socket. `aion sync`,
  `aion list`, and repository operations work here; Docker-backed `aion start`
  components require a separately reviewed container-runtime design.
- Store retained local artifacts under `/workspace/state`, redact secrets, and
  keep them out of Git.
- Keep the root README, architecture, status/checklist, security guidance,
  environment template, tool catalog documentation, and this file synchronized
  whenever container dependencies or boundaries change.
