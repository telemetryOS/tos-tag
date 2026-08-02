# Reviewed TelemetryOS tools

This catalog is the executable counterpart to the behavioral skills injected
from `tag-agent-skills`. Codex App Server receives the bundle `SKILL.md` files
and typed `tos_tag_tool` capability only; it does not receive this directory,
the control-plane environment, or keystore references.

`telemetryos.code` is the only source-tree capability. Its server-owned root is
bound from `TAG_AION_DEVELOPER_PATH`; it permits repository/file listing,
fixed-string search, and bounded line reads while rejecting traversal,
symlinks, runtime environment files, credential ledgers, and private tool
state. It deliberately provides neither a generic shell nor a write operation.
The loader and executor both reject the bundle unless every operation remains
exactly `read` risk. Source-mutation intent is routed to Linear bug/feature
intake and cannot be converted into an approval.

## Catalog

| Tool ID | Operations | Approval | Declared environment |
| --- | --- | --- | --- |
| `telemetryos.code` | `read` | Risk-based | `TAG_AION_DEVELOPER_PATH` (server-owned path binding, not a credential) |
| `telemetryos.product-docs` | `read` | Never | None; fixed public TelemetryOS HTTPS sources only |
| `telemetryos.linear` | `read`, `write` | Risk-based | `LINEAR_API_KEY` |
| `telemetryos.wiki` | `read`, `write`, `delete` | Never for read/write; always for recoverable page soft-delete | `WIKI_URL`, `WIKI_TOKEN` |
| `telemetryos.otel` | `read` | Risk-based | `SIGNOZ_URL`, `SIGNOZ_API_KEY` |
| `telemetryos.device-logs` | `read`, `write` | Risk-based | `DLA_API_BASE_URL`, `DLA_API_KEY`, `DLA_ENV` |
| `telemetryos.mongo` | `read` | Risk-based | `MONGO_QA_URI` |

Every operation runs only when the current job capability permits it. Omitted
approval policy is risk-based, so `write` and `destructive` normally require a
durable, single-use exact-action Slack approval. Admin-risk worker operations
are rejected. The reviewed Wiki bundle explicitly declares `approval: never`
for page read/write authoring and `approval: always` for recoverable page
soft-delete. Namespace, asset, publish-file, cascading move, activity, generic
undo, and admin calls are not declared and are rejected. All permitted calls still produce
requested/completed audit receipts and retain every other gateway constraint.
Tool selection is also constrained by
the configured tool-ID allowlist and by each injected skill's declared
requirements.

`tools/linear/run.sh` and `tools/wiki/run.sh` are self-contained reviewed
tos-tag helpers with operation guards driven by the executor-owned
`TOS_TAG_OPERATION_ID`. The Wiki bundle's local `1.3.3`
revision accepts an exact inline body for source-derived publications, returns
the full page envelope with its canonical human URL for every reviewed `get`
(even when a worker omits `--json`), declares the validated credential-free
`WIKI_URL` binding public while retaining token redaction, and opts normal Wiki
read/write authoring out of per-action approval. OTel, DLA, and Mongo wrappers call
the separately pinned binaries documented in the root README and reject
credential, endpoint, dotenv, and agent-launch overrides.

`tools/product-docs/run.sh` is copied from
`tag-agent-skills/src/skills/product-knowledge/scripts/read-product-source.sh`.
It allows only the docs index, one constrained `docs/` or `reference/` Markdown
page on `docs.telemetryos.com`, or the fixed corporate `llms-full.txt`. It does
not follow redirects, accept headers or credentials, or expose a general URL
fetcher.
Classifier-marked product answers require a successful same-attempt
`docs-page`, `corporate-full`, or Agent Wiki full-page read before delivery;
an index, search result, arbitrary web result, Slack context, or memory is not
accepted as authoritative retrieval.

To update a bundle:

1. review the upstream diff and copy the helper into this catalog;
2. preserve or update its operation guard;
3. bump the bundle version and record the new upstream commit here;
4. run `go test ./core/tools ./integration`, `go test -race ./...`, `go vet
   ./...`, `make eval`, and `make security`; and
5. restart tos-tag so the immutable script hash and encrypted bindings are
   rebuilt before any job can use the new version.

Do not add a generic shell, accept credentials in argv, or let a model select a
secret reference. Add a narrow operation or a separate reviewed bundle instead.

When adding or changing a tool, also update the root `README.md`, `AGENTS.md`,
`CLAUDE.md`, `architecture.md`, implementation status/checklist,
`SECURITY.md`, and `runtime.env.example`, then verify every declared operation,
risk class, approval policy, environment name, timeout, output limit, and source hash still
matches `tool.json`.
