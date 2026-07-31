#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
docker_config_root="${DOCKER_CONFIG:-${HOME}/.docker}"
docker_config_file="${docker_config_root}/config.json"

# A Colima installation can inherit Docker Desktop's credential-store setting
# after Docker Desktop has been removed. Keep the user's global Docker config
# untouched and use an empty registry config for this project's public images.
if [[ -f "${docker_config_file}" ]] \
  && grep -Eq '"credsStore"[[:space:]]*:[[:space:]]*"desktop"' "${docker_config_file}" \
  && ! command -v docker-credential-desktop >/dev/null 2>&1; then
  if ! command -v docker-compose >/dev/null 2>&1; then
    echo "Docker references unavailable docker-credential-desktop and no standalone docker-compose command is installed" >&2
    exit 1
  fi
  docker_host="${DOCKER_HOST:-$(docker context inspect --format '{{.Endpoints.docker.Host}}')}"
  DOCKER_CONFIG="${repository_root}/container/docker-public-config" \
    DOCKER_HOST="${docker_host}" \
    exec docker-compose "$@"
fi

exec docker compose "$@"
