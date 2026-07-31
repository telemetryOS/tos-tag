#!/usr/bin/env bash
set -euo pipefail

workspace_root="${TAG_WORKSPACE_ROOT:-/workspace}"
aion_developer_path="${TAG_AION_DEVELOPER_PATH:-${workspace_root}/code}"
projects_root="${workspace_root}/projects"
skills_root="${workspace_root}/skills"
state_root="${workspace_root}/state"
tools_root="${workspace_root}/tools"
aion_version="${TAG_AION_VERSION:-v2.0.5}"
aion_commit="${TAG_AION_COMMIT:-2b186d21cb14d7e030936750e69e7dbd1eeecab6}"
sync_aion=false
update_repositories=false

usage() {
  echo "usage: bootstrap-workspace [--sync] [--update]"
  echo "  --sync    run 'aion sync' after cloning the control-plane repositories"
  echo "  --update  fast-forward clean existing tos-tag and skill repositories"
}

for argument in "$@"; do
  case "${argument}" in
    --sync) sync_aion=true ;;
    --update) update_repositories=true ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: ${argument}" >&2; usage >&2; exit 2 ;;
  esac
done

mkdir -p "${aion_developer_path}" "${projects_root}" "${skills_root}" "${tools_root}" "${state_root}/logs" "${state_root}/workers" "${HOME}/go/bin"

if [[ ! -e "${workspace_root}/AGENTS.md" ]]; then
  install -m 0644 /usr/local/share/tos-tag/workspace-AGENTS.md "${workspace_root}/AGENTS.md"
fi

aion_config="${HOME}/.config/aion.toml"
mkdir -p "$(dirname "${aion_config}")"
if [[ ! -e "${aion_config}" ]]; then
  printf 'developer_path = "%s"\ncurrent_profile = "telemetryOS"\n' "${aion_developer_path}" >"${aion_config}"
elif ! grep -Fq "developer_path = \"${aion_developer_path}\"" "${aion_config}"; then
  echo "${aion_config} already exists with a different developer_path; update it to ${aion_developer_path}" >&2
  exit 1
fi

if ! gh auth status --hostname github.com >/dev/null 2>&1; then
  echo "GitHub CLI is not authenticated. Run: docker compose run --rm workspace gh auth login --web" >&2
  exit 1
fi

gh auth setup-git --hostname github.com
git config --global url.https://github.com/.insteadOf ssh://git@github.com/
git config --global --add url.https://github.com/.insteadOf git@github.com:

sync_repository() {
  local repository="$1"
  local destination="$2"
  if [[ ! -d "${destination}/.git" ]]; then
    gh repo clone "${repository}" "${destination}"
    return
  fi
  if [[ "${update_repositories}" != true ]]; then
    return
  fi
  if [[ -n "$(git -C "${destination}" status --porcelain)" ]]; then
    echo "skip update for dirty repository: ${destination}" >&2
    return
  fi
  git -C "${destination}" pull --ff-only
}

sync_repository telemetryOS/tos-tag "${projects_root}/tos-tag"
sync_repository telemetryOS/telemetryos-agent-skills "${skills_root}/telemetryos-agent-skills"
sync_repository telemetryOS/tag-agent-skills "${skills_root}/tag-agent-skills"

aion_source="${tools_root}/Aion"
if [[ ! -d "${aion_source}/.git" ]]; then
  gh repo clone telemetryOS/Aion "${aion_source}" -- --branch "${aion_version}"
fi
if [[ "$(git -C "${aion_source}" describe --tags --exact-match 2>/dev/null || true)" != "${aion_version}" ]]; then
  echo "Aion source at ${aion_source} is not pinned to ${aion_version}" >&2
  exit 1
fi
if [[ "$(git -C "${aion_source}" rev-parse HEAD)" != "${aion_commit}" ]]; then
  echo "Aion ${aion_version} does not match pinned commit ${aion_commit}" >&2
  exit 1
fi
(
  cd "${aion_source}"
  go build -trimpath -ldflags "-X github.com/telemetryos/aion/v2/version.Version=${aion_version}" -o "${HOME}/go/bin/aion" ./cmd/aion
)

if [[ "${sync_aion}" == true ]]; then
  aion sync
fi

echo "workspace ready: ${workspace_root}"
echo "tos-tag: ${projects_root}/tos-tag"
echo "Aion-managed code: ${aion_developer_path}"
echo "skills: ${skills_root}"
