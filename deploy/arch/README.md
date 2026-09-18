# Arch Linux host deployment

This deployment runs the Go API as a lingering user service. MongoDB is the
host's **single bare-metal** user unit `mongodb.service` (`~/.local/bin/mongod`,
replica set `rs0` on `127.0.0.1:27017`, authentication enabled); tos-tag is
the `tos_tag` database inside it. Secrets, including the Mongo credentials in
`TAG__MONGO__URI` (`mongodb://USER:PASSWORD@127.0.0.1:27017/tos_tag?replicaSet=rs0&authSource=admin`,
user/password from `~/.config/mongodb/credentials`), remain in the ignored
mode-0600 `runtime.env`; `tos-tag.env` contains only host-specific, non-secret
overrides and is sourced afterwards.

**Host rule: no databases in Docker on `arch`, and one MongoDB for the box.**
The Compose-managed `tos-tag-mongo-1` container was retired on 2026-09-18 and
its data now lives in `mongodb.service`. `docker-compose.yml` still defines a
`mongo` service for the disposable container workspace on other machines; do
not bring it up on `arch`, do not start another mongod, and do not add Mongo,
Valkey/Redis, Postgres or any other datastore as a container here. A new
datastore of another kind gets a `systemd --user` unit like `telemetryos-valkey`.

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

User lingering must already be enabled for boot startup, and the host's
`mongodb.service` must be enabled. The startup wrapper waits for MongoDB on
`127.0.0.1:27017` before launching the API; Docker is no longer involved. It also supplies a deterministic service `PATH` containing
`~/.local/bin`, where the per-user Codex CLI is installed; this does not depend
on interactive shell initialization. Disposable Codex workspaces live under
`~/.local/state/tos-tag/workers` so unrelated pressure on the shared `/tmp`
tmpfs cannot prevent worker provisioning.
