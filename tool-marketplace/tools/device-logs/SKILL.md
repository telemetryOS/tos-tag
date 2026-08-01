---
name: telemetryos-device-logs-tool
description: Query TelemetryOS device logs through the reviewed dla executable, with device verbosity changes separately approved.
---

After following the injected `device-log-analyzer` skill, use `tool_id=telemetryos.device-logs`. Normal sync, list, summary, audit, context, inspect, components, dictionaries, and config commands use `operation_id=read`. The bound `DLA_ENV` selects the environment, so omit `--env`; credential, endpoint, and dotenv overrides are rejected. A temporary `device-log-level` change uses `operation_id=write` and requires Slack approval. Agent-launch flags are unavailable. Never request, print, or pass an API key.
