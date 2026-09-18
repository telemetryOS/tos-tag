# Arch Linux host deployment

This deployment runs the Go API as a lingering user service. MongoDB runs
**bare metal** as the user unit `tos-tag-mongo.service` (`~/.local/bin/mongod`
on `127.0.0.1:27018`, data in `~/.local/share/tos-tag/mongo`). Secrets remain
in the ignored mode-0600 `runtime.env`; `tos-tag.env` contains only
host-specific, non-secret overrides and is sourced afterwards.

**Host rule: no databases in Docker on `arch`.** The former Compose-managed
`tos-tag-mongo-1` container was retired on 2026-09-18 and its data restored
into the bare-metal unit. `docker-compose.yml` still defines a `mongo` service
for the disposable container workspace on other machines; do not bring it up
on `arch`, and do not add Mongo, Valkey/Redis, Postgres or any other datastore
as a container here. New datastores get a `systemd --user` unit like the
existing `tos-tag-mongo`, `telemetryos-mongo` (rs0, :27017) and
`telemetryos-valkey` (:6379) units.

The checked-in listener override exposes the admin plane on all host
interfaces. It also explicitly allows unauthenticated non-loopback access,
making the host firewall the authorization boundary. Change
`TAG__HTTP__ADDR` or remove that opt-in before using these files on a host
without the same trusted-firewall posture.

Install or refresh the user unit:

```bash
make install-semantic-search
gh auth login
make sync-tool-env
mkdir -p ~/.config/systemd/user ~/.local/lib/tos-tag
go build -trimpath -buildvcs=false -o ~/.local/lib/tos-tag/api ./cmd/api
cp deploy/arch/tos-tag.service ~/.config/systemd/user/tos-tag.service
systemctl --user daemon-reload
systemctl --user enable --now tos-tag.service
```

User lingering must already be enabled for boot startup, and
`tos-tag-mongo.service` must be enabled (`systemctl --user enable --now
tos-tag-mongo.service`). The startup wrapper waits for MongoDB on
`127.0.0.1:27018` before launching the API; Docker is no longer involved. It also supplies a deterministic service `PATH` containing
`~/.local/bin`, where the per-user Codex CLI is installed; this does not depend
on interactive shell initialization. Disposable Codex workspaces live under
`~/.local/state/tos-tag/workers` so unrelated pressure on the shared `/tmp`
tmpfs cannot prevent worker provisioning.
