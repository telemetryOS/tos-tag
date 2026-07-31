# TelemetryOS container workspace

This persistent workspace is shared by the operator-facing OpenCode shell and
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
- Do not treat OpenCode permissions as a security boundary. Slack-triggered
  workers remain disposable, default-deny, and separate from this durable
  operator workspace.
- This environment intentionally has no host Docker socket. `aion sync`,
  `aion list`, and repository operations work here; Docker-backed `aion start`
  components require a separately reviewed container-runtime design.
- Store retained local artifacts under `/workspace/state`, redact secrets, and
  keep them out of Git.
