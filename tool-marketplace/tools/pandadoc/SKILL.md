---
name: pandadoc-documents-tool
description: Inspect and manage approved PandaDoc document workflows through a fixed-host semantic shell CLI with separate read, write, and delete operations.
---

# PandaDoc Documents Tool

Call `tos_tag_tool` with `tool_id=pandadoc.documents`, the active `pandadoc`
skill in `skill_names`, and one reviewed operation:

- `read`: bounded document, template, content, form, folder, contact, or member
  retrieval;
- `write`: JSON-only document/contact creation or update, document send/draft/
  status, or field changes; or
- `delete`: one exact document or contact deletion.

The wrapper fixes the origin to `https://api.pandadoc.com`, supplies the private
`PANDA_DOC_API_KEY` through a mode-0600 curl configuration, stores write bodies
in a mode-0600 temporary file, validates semantic commands and bounded JSON,
and returns JSON. It excludes arbitrary paths, methods, URLs, headers, OAuth,
API-key/workspace administration, webhook secrets, editing/signing sessions,
recipient tokens, binary upload/download, template mutation, and arbitrary
flags.

Read the exact target before a mutation. Use `write` or `delete` only for an
explicit external effect, and freshly verify when possible. Sending a document
normally triggers recipient notifications and must never be inferred merely
from document creation. Never reveal access keys, recipient/session tokens,
signatures, document content, contact details, pricing, or unrelated metadata.
