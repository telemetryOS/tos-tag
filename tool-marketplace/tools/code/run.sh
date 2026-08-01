#!/usr/bin/env bash
set -euo pipefail

die() {
  printf 'code: %s\n' "$*" >&2
  exit 1
}

[[ "${TOS_TAG_OPERATION_ID:-}" == "read" ]] || die "only the read operation is permitted"
[[ -n "${TAG_AION_DEVELOPER_PATH:-}" ]] || die "source root binding is unavailable"
[[ -d "${TAG_AION_DEVELOPER_PATH}" ]] || die "configured source root is unavailable"
command -v rg >/dev/null 2>&1 || die "ripgrep is required"

root="$(cd "${TAG_AION_DEVELOPER_PATH}" && pwd -P)"

validate_relative() {
  local relative="${1:-.}" component base lowered
  [[ -n "${relative}" && "${relative}" != /* ]] || die "path must be relative"
  case "/${relative}/" in
    */../*|*/./../*) die "path traversal is not permitted" ;;
  esac
  IFS='/' read -r -a components <<<"${relative}"
  for component in "${components[@]}"; do
    lowered="$(printf '%s' "${component}" | tr '[:upper:]' '[:lower:]')"
    case "${lowered}" in
      .git|.testruns|.config|.codex|.ssh|node_modules)
        die "restricted path component"
        ;;
    esac
  done
  base="${relative##*/}"
  lowered="$(printf '%s' "${base}" | tr '[:upper:]' '[:lower:]')"
  case "${lowered}" in
    .env|.env.*|runtime.env|credentials.toml|credentials.json|.npmrc|.pypirc|id_rsa|id_rsa.*|id_ed25519|id_ed25519.*)
      die "restricted credential or runtime file"
      ;;
  esac
}

resolve_directory() {
  local relative="${1:-.}" resolved
  validate_relative "${relative}"
  [[ -d "${root}/${relative}" ]] || die "directory does not exist"
  resolved="$(cd "${root}/${relative}" && pwd -P)"
  case "${resolved}" in
    "${root}"|"${root}"/*) printf '%s' "${resolved}" ;;
    *) die "path escapes the configured source root" ;;
  esac
}

resolve_file() {
  local relative="$1" directory resolved target
  validate_relative "${relative}"
  directory="$(dirname "${relative}")"
  resolved="$(resolve_directory "${directory}")"
  target="${resolved}/$(basename "${relative}")"
  [[ -f "${target}" && ! -L "${target}" ]] || die "file does not exist or is not a regular file"
  case "${target}" in
    "${root}"/*) printf '%s' "${target}" ;;
    *) die "path escapes the configured source root" ;;
  esac
}

validate_limit() {
  local value="$1" maximum="$2"
  [[ "${value}" =~ ^[0-9]+$ ]] || die "limit must be numeric"
  (( value >= 1 && value <= maximum )) || die "limit is outside the allowed range"
}

rg_files() {
  rg --files --hidden \
    --glob '!.git/**' --glob '!**/.git/**' \
    --glob '!.testruns/**' --glob '!**/.testruns/**' \
    --glob '!.config/**' --glob '!**/.config/**' \
    --glob '!.codex/**' --glob '!**/.codex/**' \
    --glob '!node_modules/**' --glob '!**/node_modules/**' \
    --glob '!.env' --glob '!**/.env' --glob '!.env.*' --glob '!**/.env.*' \
    --glob '!runtime.env' --glob '!**/runtime.env' \
    --glob '!credentials.toml' --glob '!**/credentials.toml' \
    --glob '!credentials.json' --glob '!**/credentials.json' \
    "$@"
}

verb="${1:-}"
shift || true
case "${verb}" in
  repos)
    [[ "$#" -eq 0 ]] || die "repos accepts no arguments"
    find "${root}" \
      \( -name .testruns -o -name .config -o -name .codex -o -name node_modules \) -prune -o \
      -mindepth 2 -maxdepth 3 -name .git -print \
      | sed -e "s#^${root}/##" -e 's#/.git$##' \
      | LC_ALL=C sort
    ;;
  files)
    [[ "$#" -le 2 ]] || die "files accepts a directory and optional limit"
    relative="${1:-.}"
    limit="${2:-200}"
    validate_limit "${limit}" 500
    directory="$(resolve_directory "${relative}")"
    scope="${directory#"${root}"}"
    scope="${scope#/}"
    [[ -n "${scope}" ]] || scope="."
    (cd "${root}" && { status=0; rg_files "${scope}" || status=$?; (( status <= 1 )) || exit "${status}"; }) \
      | awk -v limit="${limit}" 'NR <= limit { print }'
    ;;
  search)
    [[ "$#" -ge 1 && "$#" -le 3 ]] || die "search requires a fixed string, optional directory, and optional limit"
    pattern="$1"
    relative="${2:-.}"
    limit="${3:-200}"
    [[ -n "${pattern}" && "${#pattern}" -le 256 ]] || die "search string must be 1-256 characters"
    validate_limit "${limit}" 500
    directory="$(resolve_directory "${relative}")"
    scope="${directory#"${root}"}"
    scope="${scope#/}"
    [[ -n "${scope}" ]] || scope="."
    (cd "${root}" && { status=0; rg -F -n --no-heading --color never \
      --hidden \
      --glob '!.git/**' --glob '!**/.git/**' \
      --glob '!.testruns/**' --glob '!**/.testruns/**' \
      --glob '!.config/**' --glob '!**/.config/**' \
      --glob '!.codex/**' --glob '!**/.codex/**' \
      --glob '!node_modules/**' --glob '!**/node_modules/**' \
      --glob '!.env' --glob '!**/.env' --glob '!.env.*' --glob '!**/.env.*' \
      --glob '!runtime.env' --glob '!**/runtime.env' \
      --glob '!credentials.toml' --glob '!**/credentials.toml' \
      --glob '!credentials.json' --glob '!**/credentials.json' \
      -- "${pattern}" "${scope}" || status=$?; (( status <= 1 )) || exit "${status}"; }) \
      | awk -v limit="${limit}" 'NR <= limit { print }'
    ;;
  read)
    [[ "$#" -ge 1 && "$#" -le 3 ]] || die "read requires a file and optional start/end lines"
    relative="$1"
    start="${2:-1}"
    end="${3:-400}"
    [[ "${start}" =~ ^[0-9]+$ && "${end}" =~ ^[0-9]+$ ]] || die "line bounds must be numeric"
    (( start >= 1 && end >= start && end - start + 1 <= 400 )) || die "line range must contain 1-400 lines"
    file="$(resolve_file "${relative}")"
    size="$(wc -c <"${file}" | tr -d '[:space:]')"
    (( size <= 2097152 )) || die "file exceeds the read size limit"
    sed -n "${start},${end}p" "${file}" \
      | awk -v line="${start}" '{ printf "%d:%s\n", line + NR - 1, $0 }'
    ;;
  *)
    die "supported verbs: repos, files, search, read"
    ;;
esac
