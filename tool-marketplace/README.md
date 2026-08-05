# Reviewed TelemetryOS tools

This catalog is the executable counterpart to the behavioral skills injected
from `tag-agent-skills`. Codex App Server receives the bundle `SKILL.md` files,
the generic `tos_tag_tool` capability, and the dedicated typed `tos_tag_wiki`
page facade; it does not receive this directory,
the control-plane environment, or keystore references.

`telemetryos.code` is the only source-tree capability. It uses
`TAG_AION_DEVELOPER_PATH` only as a server-owned repository inventory, validates
the requested repository's fixed `telemetryOS` origin, and refreshes its remote
default branch into an immutable owner-only snapshot without touching the
developer worktree. It exposes bounded freshness, repository/file listing,
fixed-string search, pinned offline Semble discovery, version evidence, and
line reads while rejecting arbitrary remotes/branches, traversal, symlinks,
runtime environment files, credential ledgers, and private tool state. It
deliberately provides neither a generic shell nor a source-write operation.
The loader and executor both reject the bundle unless every operation remains
exactly `read` risk. Source-mutation intent is silently suppressed before
worker admission and cannot be converted into a response or approval.

## Catalog

| Tool ID | Operations | Approval | Declared environment |
| --- | --- | --- | --- |
| `telemetryos.code` | `read` | Risk-based | Aion inventory, owner-only snapshot/index/model paths, and GitHub CLI credential-store path; none are worker-visible |
| `telemetryos.product-docs` | `read` | Never | None; fixed public TelemetryOS HTTPS sources only |
| `telemetryos.linear` | `read`, `intake`, `write` | Never for bounded bug/feature intake; risk-based otherwise | `LINEAR_API_KEY` |
| `telemetryos.wiki` | `read`, `write`, `delete` | Never for read/write; always for recoverable page soft-delete | `WIKI_URL`, `WIKI_TOKEN` |
| `telemetryos.otel` | `read` | Risk-based | `SIGNOZ_URL`, `SIGNOZ_API_KEY` |
| `telemetryos.analytics` | `read` | Never | `TELEMETRYOS_ANALYTICS_URL` (validated public origin), `SITE_ANALYTICS_TOKEN` |
| `attio.crm` | `read`, `write`, `delete` | Risk-based | `ATTIO_ACCESS_TOKEN` |
| `stripe.billing` | `read`, `write`, `delete` | Risk-based | `STRIPE_API_KEY` |
| `digitalocean.cloud` | `read`, `write`, `delete` | Risk-based | `DIGITAL_OCEAN_API_KEY` |
| `telemetryos.device-logs` | `read`, `write` | Risk-based | `DLA_API_BASE_URL`, `DLA_API_KEY`, `DLA_ENV` |
| `telemetryos.mongo` | `read` | Risk-based | `MONGO_QA_URI` |

Every operation runs only when the current job capability permits it. Omitted
approval policy is risk-based, so `write` and `destructive` normally require a
durable, single-use exact-action Slack approval. Admin-risk worker operations
are rejected. The reviewed Linear bundle declares `approval: never` only for
its `intake` operation, whose wrapper permits bounded bug/feature creation,
evidence comments, feature normalization, and suitability follow-up; generic
Linear `write` remains risk-based. Linear title and description writes use a
separate issue read-back, with only line endings and trailing description
newlines normalized, so stale mutation payloads cannot produce false failures.
The reviewed Wiki bundle explicitly declares
`approval: never` for page read/write authoring and `approval: always` for
recoverable page soft-delete. Namespace, asset, publish-file, cascading move,
activity, generic undo, and admin calls are not declared and are rejected. All permitted calls still produce
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

`tools/analytics/run.sh` accepts only the fixed funnel `pipeline`, `insights`,
`website`, `accounts`, `account`, and normalized `events` GET operations, plus
bounded raw `site-events` instrumentation reads, on the production or QA
TelemetryOS Gateway. It does not accept arbitrary endpoints, headers,
credentials, internal-event inclusion, visitor/session lookup, or exports. The
Site Analytics Token is supplied to curl through a mode-0600 temporary config,
never argv, and returned JSON is filtered to remove direct identifiers,
free-form event properties, and self-reported customer text.
`tools/attio/run.sh` is copied from
`tag-agent-skills/src/skills/attio/scripts/attio.sh`. It fixes the origin to
`https://api.attio.com`, maps reviewed `get` and semantic `query` commands to
read authority, maps JSON `post`/`put`/`patch` to write authority, and isolates
deletes as destructive. Only documented v2 JSON path shapes are available;
arbitrary URLs, headers, OAuth exchanges, and binary upload/download are
rejected. Bearer authentication is passed through a mode-0600 curl config.
`tools/digitalocean/run.sh` is copied from
`tag-agent-skills/src/skills/digitalocean/scripts/digitalocean.sh`. It invokes
the official doctl CLI with an isolated empty home, maps the server-side key to
doctl's token environment, and exposes only fixed inventory reads, exact
Droplet power/App restart actions, and one-target deletes. It rejects raw
commands and flags, auth/config/profile changes, API-origin overrides, Apps
specs, database connection data, kubeconfig changes, creation/update,
multi-target deletion, credential export, and cascading Kubernetes deletion.
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

The code bundle pins Semble 0.5.3 through
`scripts/requirements-semantic-search.txt` and pins
`minishlab/potion-code-16M-v2` at revision
`e9d2a44ca6a05ac6685f3b23709ea57eb7352d5b` with per-file SHA-256 checks.
`make install-semantic-search` installs and verifies both before
`make sync-tool-env` binds their owner-only paths.

When adding or changing a tool, also update the root `README.md`, `AGENTS.md`,
`CLAUDE.md`, `architecture.md`, implementation status/checklist,
`SECURITY.md`, and `runtime.env.example`, then verify every declared operation,
risk class, approval policy, environment name, timeout, output limit, and source hash still
matches `tool.json`.
