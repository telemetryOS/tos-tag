# tos-tag implementation design

The authoritative architecture is [architecture.md](architecture.md). This
file records how the source tree maps to that design while implementation is in
progress.

| Source area | Architecture responsibility |
| --- | --- |
| `cmd/api`, `core/core.go` | composition root and ordered lifecycle |
| `core/config`, `core/database`, `core/server` | configuration, MongoDB, readiness, HTTP |
| `core/slack`, `core/observer`, `core/intelligence` | stubbed transport, observations, organization timeline |
| `core/contextpacks`, `core/classifier` | immutable bounded context and tool-free decision admission |
| `core/sessions`, `core/jobs`, `core/deliveries` | thread generations, leased work, Slack output contract |
| `core/modelrouter`, `core/opencode`, `core/workers` | dynamic routing and disposable execution boundaries |
| `core/policy`, `core/toolgateway`, `core/toolrunner` | authorization and credential-free worker tools |
| `core/audit`, `core/janitor` | receipts, integrity, retention, deletion fan-out |
| `routes`, `core/web` | authenticated JSON and server-rendered management surfaces |

Live Slack uses the `core/slack` Socket Mode adapter behind explicit
configuration; the deterministic stub remains the default test boundary.
