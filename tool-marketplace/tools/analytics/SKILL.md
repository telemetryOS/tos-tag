---
name: telemetryos-analytics-tool
description: Read privacy-filtered TelemetryOS acquisition, account, activation, and expansion evidence through the reviewed Site Analytics Token boundary.
---

# TelemetryOS Analytics Tool

Call `tos_tag_tool` with `tool_id=telemetryos.analytics`,
`operation_id=read`, the active marketing skills in `skill_names`, and exactly
one documented argument form:

```json
{"skill_names":["marketing-funnel-review"],"tool_id":"telemetryos.analytics","operation_id":"read","arguments":["pipeline"]}
{"skill_names":["marketing-funnel-review"],"tool_id":"telemetryos.analytics","operation_id":"read","arguments":["insights","--months","1","--segment","humans","--exclude-agentic","true"]}
{"skill_names":["marketing-funnel-review"],"tool_id":"telemetryos.analytics","operation_id":"read","arguments":["website","--months","1"]}
{"skill_names":["marketing-funnel-review"],"tool_id":"telemetryos.analytics","operation_id":"read","arguments":["accounts","--stalled","true","--page","1","--per-page","25"]}
{"skill_names":["marketing-account-journey"],"tool_id":"telemetryos.analytics","operation_id":"read","arguments":["account","<24-hex-account-id>"]}
{"skill_names":["marketing-funnel-review"],"tool_id":"telemetryos.analytics","operation_id":"read","arguments":["events","--from","<RFC3339>","--to","<RFC3339>","--source","sitelog","--page","1","--per-page","100"]}
{"skill_names":["marketing-funnel-review"],"tool_id":"telemetryos.analytics","operation_id":"read","arguments":["site-events","--from","<RFC3339>","--to","<RFC3339>","--exclude-bots","true","--page","1","--per-page","100"]}
```

Supported filters are intentionally narrow:

- `insights`: `--months 1..24`, `--segment humans|bots|all`, and
  `--exclude-agentic true|false`;
- `website`: `--months 1..24`;
- `accounts`: `--stage`, `--grade A..F`, `--flow self_signup|demo`,
  `--stalled true`, `--page`, and `--per-page 1..50`;
- `account`: one 24-hex account ID returned by an approved Analytics result;
- `events`: `--from`, `--to`, `--type`, `--source sitelog|applog`,
  `--account`, `--segment humans|bots|all`, `--page`, and
  `--per-page 1..100`.
- `site-events`: raw marketing-site instrumentation with `--from`, `--to`,
  comma-separated `--type`, exact public `--path`, exactly one required bot
  scope (`--exclude-bots true` or `--bots-only true`), `--page`, and
  `--per-page 1..100`. The helper wraps the source array as
  `{total,page,per_page,events}` using the API's `Total-Records-Count` header.

The capability never accepts internal-event inclusion, visitor/session lookup,
arbitrary paths, HTTP methods, headers, endpoints, exports, or credentials in
arguments. The reviewed helper removes direct identifiers and free-form event
properties before returning JSON. Raw `site-events` exists only for bounded
instrumentation audits. It omits visitor/session lookup and attribution
identifiers and must not be treated as an account journey source.

For an instrumentation audit, compare bounded `site-events` results with
normalized `events --source sitelog` over the same RFC3339 window and event
types. Report count/type differences as evidence of a collection or
normalization gap; do not assume raw and normalized records are one-to-one.

Never request or reveal the Site Analytics Token. Keep calls sequential,
minimize account-level reads, preserve N-gating and identity uncertainty, and
follow the active marketing skill's output and privacy contract.
