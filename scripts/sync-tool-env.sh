#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
runtime_file="${1:-${repo_root}/runtime.env}"
telemetry_config="${TELEMETRYOS_CONFIG_HOME:-${HOME}/.config/telemetryos}"
code_root="${TAG_AION_DEVELOPER_PATH:-$(dirname "${repo_root}")}"
semantic_root="${TAG_CODE_SEMANTIC_ROOT:-${HOME}/.local/lib/tos-tag-semantic}"
semantic_release="${semantic_root}/releases/semble-0.5.3-model-e9d2a44ca6a05ac6685f3b23709ea57eb7352d5b"
code_snapshot_root="${TAG_CODE_SNAPSHOT_ROOT:-${HOME}/.local/state/tos-tag/code-snapshots}"
code_index_root="${TAG_CODE_INDEX_ROOT:-${HOME}/.local/state/tos-tag/code-indexes}"
code_model_path="${TAG_CODE_MODEL_PATH:-${semantic_release}/model}"
code_gh_config_dir="${TAG_CODE_GH_CONFIG_DIR:-${HOME}/.config/gh}"
semble_binary="${TAG_CODE_SEMBLE_BIN:-${HOME}/.local/bin/semble}"

if [[ ! -f "${runtime_file}" ]]; then
  echo "missing ${runtime_file}; copy runtime.env.example first" >&2
  exit 1
fi
chmod 0600 "${runtime_file}"

imported_names=()

read_env_file() {
  local file="$1" wanted="$2" line key value
  [[ -f "${file}" ]] || return 1
  while IFS= read -r line || [[ -n "${line}" ]]; do
    [[ "${line}" =~ ^[[:space:]]*# || "${line}" != *"="* ]] && continue
    key="${line%%=*}"
    value="${line#*=}"
    key="${key//[[:space:]]/}"
    [[ "${key}" = "${wanted}" ]] || continue
    value="${value#"${value%%[![:space:]]*}"}"
    value="${value%"${value##*[![:space:]]}"}"
    if [[ "${value}" == \"*\" && "${value}" == *\" ]]; then value="${value:1:${#value}-2}"; fi
    if [[ "${value}" == \'*\' && "${value}" == *\' ]]; then value="${value:1:${#value}-2}"; fi
    [[ -n "${value}" ]] || return 1
    printf '%s' "${value}"
    return 0
  done <"${file}"
  return 1
}

read_wiki_file() {
  local wanted="$1" alias="$2" value
  value="$(read_env_file "${telemetry_config}/wiki/config" "${wanted}" 2>/dev/null || true)"
  if [[ -z "${value}" ]]; then
    value="$(read_env_file "${telemetry_config}/wiki/config" "${alias}" 2>/dev/null || true)"
  fi
  [[ -n "${value}" ]] || return 1
  printf '%s' "${value}"
}

read_fish() {
  local name="$1"
  command -v fish >/dev/null 2>&1 || return 1
  fish -lc "printf %s \"\$$name\"" 2>/dev/null
}

resolve_value() {
  local name="$1" file="${2:-}" alias="${3:-}" value=""
  value="${!name-}"
  if [[ -z "${value}" && -n "${file}" ]]; then
    if [[ "${file}" = wiki ]]; then
      value="$(read_wiki_file "${name}" "${alias}" 2>/dev/null || true)"
    else
      value="$(read_env_file "${file}" "${name}" 2>/dev/null || true)"
    fi
  fi
  if [[ -z "${value}" ]]; then
    value="$(read_fish "${name}" 2>/dev/null || true)"
  fi
  [[ -n "${value}" ]] || return 1
  printf '%s' "${value}"
}

shell_quote() {
  local value="$1"
  [[ "${value}" != *$'\n'* && "${value}" != *$'\r'* ]] || {
    echo "multiline environment values are not supported" >&2
    return 1
  }
  value="${value//\'/\'\\\'\'}"
  printf "'%s'" "${value}"
}

upsert() {
  local key="$1" value="$2" temporary line found=false
  temporary="$(mktemp "${runtime_file}.XXXXXX")"
  chmod 0600 "${temporary}"
  while IFS= read -r line || [[ -n "${line}" ]]; do
    if [[ "${line}" == "${key}="* ]]; then
      printf '%s=%s\n' "${key}" "$(shell_quote "${value}")" >>"${temporary}"
      found=true
    else
      printf '%s\n' "${line}" >>"${temporary}"
    fi
  done <"${runtime_file}"
  if [[ "${found}" = false ]]; then
    printf '%s=%s\n' "${key}" "$(shell_quote "${value}")" >>"${temporary}"
  fi
  mv "${temporary}" "${runtime_file}"
  chmod 0600 "${runtime_file}"
}

import_one() {
  local name="$1" file="${2:-}" alias="${3:-}" value
  value="$(resolve_value "${name}" "${file}" "${alias}" 2>/dev/null || true)"
  if [[ -z "${value}" ]]; then
    echo "missing ${name}" >&2
    return 1
  fi
  upsert "${name}" "${value}"
  imported_names+=("${name}")
}

import_one LINEAR_API_KEY
import_one WIKI_URL wiki url
import_one WIKI_TOKEN wiki token
import_one SIGNOZ_URL "${telemetry_config}/otel-fetch.conf"
import_one SIGNOZ_API_KEY "${telemetry_config}/otel-fetch.conf"
import_one DLA_API_BASE_URL "${telemetry_config}/dla.conf"
import_one DLA_API_KEY "${telemetry_config}/dla.conf"
import_one DLA_ENV "${telemetry_config}/dla.conf"

injected_tools="telemetryos.linear,telemetryos.wiki,telemetryos.otel,telemetryos.device-logs,telemetryos.code,telemetryos.product-docs"
analytics_token="$(resolve_value SITE_ANALYTICS_TOKEN 2>/dev/null || true)"
if [[ -n "${analytics_token}" ]]; then
  analytics_url="$(resolve_value TELEMETRYOS_ANALYTICS_URL 2>/dev/null || true)"
  analytics_url="${analytics_url:-https://api.telemetryos.com}"
  upsert SITE_ANALYTICS_TOKEN "${analytics_token}"
  upsert TELEMETRYOS_ANALYTICS_URL "${analytics_url}"
  imported_names+=(SITE_ANALYTICS_TOKEN TELEMETRYOS_ANALYTICS_URL)
  injected_tools+=",telemetryos.analytics"
else
  echo "SITE_ANALYTICS_TOKEN not found; telemetryos.analytics remains disabled" >&2
fi

[[ -d "${code_root}" ]] || { echo "missing Aion developer path" >&2; exit 1; }
code_root="$(cd "${code_root}" && pwd -P)"
[[ -x "${semble_binary}" ]] || { echo "missing Semble; run make install-semantic-search" >&2; exit 1; }
[[ -f "${code_model_path}/.tos-tag-model-revision" ]] || { echo "missing verified semantic model; run make install-semantic-search" >&2; exit 1; }
[[ -d "${code_gh_config_dir}" ]] || { echo "missing GitHub CLI configuration; run gh auth login" >&2; exit 1; }
mkdir -p "${code_snapshot_root}" "${code_index_root}"
chmod 0700 "${code_snapshot_root}" "${code_index_root}"

if ! grep -Eq '^TAG__KEYSTORE__MASTER_KEY=.+$' "${runtime_file}"; then
  command -v openssl >/dev/null 2>&1 || { echo "openssl is required to generate the keystore master key" >&2; exit 1; }
  upsert TAG__KEYSTORE__MASTER_KEY "$(openssl rand -base64 32)"
fi

tool_path="$(dirname "${semble_binary}"):${HOME}/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"
upsert TAG__MARKETPLACES__TOOL_ROOT "${repo_root}/tool-marketplace"
upsert TAG__MARKETPLACES__TOOL_CATALOG_PATH catalog.json
upsert TAG__MARKETPLACES__INJECTED_TOOLS "${injected_tools}"
upsert TAG__MARKETPLACES__TOOL_PATH "${tool_path}"
upsert TAG__MARKETPLACES__TOOLS_ENABLED true
upsert TAG__KEYSTORE__ENABLED true
upsert TAG_AION_DEVELOPER_PATH "${code_root}"
upsert TAG_CODE_SNAPSHOT_ROOT "${code_snapshot_root}"
upsert TAG_CODE_INDEX_ROOT "${code_index_root}"
upsert TAG_CODE_MODEL_PATH "${code_model_path}"
upsert TAG_CODE_GH_CONFIG_DIR "${code_gh_config_dir}"

echo "configured reviewed tool environment in ${runtime_file}"
printf 'imported %s\n' "$(printf '%s\n' "${imported_names[@]}" | sort | paste -sd, -)"
echo "plaintext values remain only in the ignored mode-0600 runtime file and encrypted keystore"
