---
name: telemetryos-otel-tool
description: Run read-only TelemetryOS SigNoz and OpenTelemetry queries through the reviewed otel-fetch executable.
---

After following the injected `telemetry-otel-fetch` skill, call `tos_tag_tool` with `tool_id=telemetryos.otel`, `operation_id=read`, and the documented `otel-fetch` flags in `arguments`. Never request, print, or pass the SigNoz credential.
