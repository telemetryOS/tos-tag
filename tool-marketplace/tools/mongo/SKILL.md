---
name: telemetryos-mongo-tool
description: Run hard read-only TelemetryOS QA Mongo queries through the reviewed mongo-fetch executable when a human-opened session exists.
---

After following the injected `telemetry-mongo-fetch` skill, use `tool_id=telemetryos.mongo` and `operation_id=read`. `status` and the documented structured read flags are allowed; `connect` and `disconnect` are unavailable because the required human security-key touch stays outside the agent. Never request, print, or pass the Mongo URI.
