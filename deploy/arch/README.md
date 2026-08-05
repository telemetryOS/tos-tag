# Arch Linux host deployment

This deployment runs the Go API as a lingering user service and MongoDB in the
repository's Compose stack. Secrets remain in the ignored mode-0600
`runtime.env`; `tos-tag.env` contains only host-specific, non-secret
overrides and is sourced afterwards.

The checked-in listener override exposes the admin plane on all host
interfaces. It also explicitly allows unauthenticated non-loopback access,
making the host firewall the authorization boundary. Change
`TAG__HTTP__ADDR` or remove that opt-in before using these files on a host
without the same trusted-firewall posture.

Install or refresh the user unit:

```bash
make install-semantic-search
make sync-tool-env
mkdir -p ~/.config/systemd/user ~/.local/lib/tos-tag
go build -trimpath -buildvcs=false -o ~/.local/lib/tos-tag/api ./cmd/api
cp deploy/arch/tos-tag.service ~/.config/systemd/user/tos-tag.service
systemctl --user daemon-reload
systemctl --user enable --now tos-tag.service
```

User lingering and the system Docker service must already be enabled for boot
startup. The startup wrapper waits for Docker and MongoDB health before
launching the API. It also supplies a deterministic service `PATH` containing
`~/.local/bin`, where the per-user Codex CLI is installed; this does not depend
on interactive shell initialization. Disposable Codex workspaces live under
`~/.local/state/tos-tag/workers` so unrelated pressure on the shared `/tmp`
tmpfs cannot prevent worker provisioning.
