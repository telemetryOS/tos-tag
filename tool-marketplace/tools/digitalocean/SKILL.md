---
name: digitalocean-cloud-tool
description: Inspect and manage approved DigitalOcean resources through a reviewed wrapper around the official doctl CLI with separate read, write, and delete operations.
---

# DigitalOcean Cloud Tool

Call `tos_tag_tool` with `tool_id=digitalocean.cloud`, the active
`digitalocean` skill in `skill_names`, and one reviewed operation:

- `read`: one documented logical inventory command from the behavioral skill;
- `write`: one exact Droplet power action or App restart; or
- `delete`: one exact single-resource deletion.

The wrapper invokes the official `doctl` CLI with isolated configuration,
maps the private `DIGITAL_OCEAN_API_KEY` to doctl's documented token variable,
fixes JSON output, and validates every command and resource argument. It does
not expose arbitrary commands or flags, API origins, authentication or context
management, Apps specs, database connection data, kubeconfigs, creation or
configuration updates, multi-target deletion, or cascading Kubernetes delete.

Use the narrowest read before a mutation. Use `write` or `delete` only for an
explicitly requested operational effect, then freshly verify the target state
when possible. Never request or reveal access tokens, database credentials,
kubeconfigs, registry credentials, private keys, App secrets, or unrelated
infrastructure data.
