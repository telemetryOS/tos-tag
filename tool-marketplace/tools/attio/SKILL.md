---
name: attio-crm-tool
description: Read and manage approved Attio CRM data through a fixed-host, reviewed REST API wrapper with separate read, write, and delete operations.
---

# Attio CRM Tool

Call `tos_tag_tool` with `tool_id=attio.crm`, the active `attio` skill in
`skill_names`, and one reviewed operation:

- `read`: `get <documented-/v2-path> [--query <json-object>]` or
  `query <documented-read-POST-path> --data <json-object>`;
- `write`: `post|put|patch <documented-/v2-path> --data <json-object>`; or
- `delete`: `delete <documented-/v2-path>`.

Every command may add one `--query <json-object>`. The wrapper fixes the host
to `https://api.attio.com`, injects bearer authentication from the private
keystore, validates documented JSON endpoint shapes, rejects arbitrary headers
and URLs, and caps both arguments and output. It excludes OAuth exchanges and
binary file upload/download.

Use `read` for token identity, metadata, exact records, bounded lists, record
or entry queries, record search, and SQL queries. Use `write` only for an
explicit external mutation. Use `delete` only for an explicit deletion. Never
request or reveal `ATTIO_ACCESS_TOKEN`, and never include CRM content or
identifiers in `skill_names`.
