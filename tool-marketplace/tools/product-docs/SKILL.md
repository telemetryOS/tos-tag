---
name: telemetryos-product-docs-tool
description: Read TelemetryOS public documentation and corporate product content through a reviewed, domain-allowlisted tos_tag_tool capability.
---

Use `tool_id=telemetryos.product-docs` and `operation_id=read` for current
public TelemetryOS product sources. The helper performs bounded HTTPS GETs only
to fixed TelemetryOS hosts. It accepts no credentials, arbitrary URLs, request
headers, methods, redirects, or shell commands.

Supported argument arrays:

- `['docs-index']`: fetch `https://docs.telemetryos.com/llms.txt` for discovery.
- `['docs-page', '<path-or-exact-docs-URL>']`: fetch one Markdown page under
  `docs/` or `reference/` on `docs.telemetryos.com`. Use a path returned by the
  index rather than guessing.
- `['corporate-full']`: fetch
  `https://www.telemetryos.com/llms-full.txt` for published positioning,
  product-page content, and use cases.

The docs index is a map, not detailed evidence. Fetch the linked page before
making a technical or operational claim. The corporate source is already full
content. Use the `product-knowledge` skill to decide when to combine these
public sources with the Agent Wiki Primer.

Examples:

```json
{"tool_id":"telemetryos.product-docs","operation_id":"read","arguments":["docs-index"]}
```

```json
{"tool_id":"telemetryos.product-docs","operation_id":"read","arguments":["docs-page","docs/node-mini.md"]}
```
