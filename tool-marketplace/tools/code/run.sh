#!/usr/bin/env bash
set -euo pipefail
umask 077

die() {
  printf 'code: %s\n' "$*" >&2
  exit 1
}

[[ "${TOS_TAG_OPERATION_ID:-}" == "read" ]] || die "only the read operation is permitted"
for required_name in TAG_AION_DEVELOPER_PATH TAG_CODE_SNAPSHOT_ROOT TAG_CODE_INDEX_ROOT TAG_CODE_MODEL_PATH TAG_CODE_GH_CONFIG_DIR; do
  [[ -n "${!required_name:-}" ]] || die "source snapshot binding is unavailable"
done
[[ -d "${TAG_AION_DEVELOPER_PATH}" ]] || die "configured source root is unavailable"
[[ -d "${TAG_CODE_MODEL_PATH}" ]] || die "semantic model is unavailable"
[[ -f "${TAG_CODE_MODEL_PATH}/.tos-tag-model-revision" ]] || die "semantic model verification is unavailable"
[[ "$(<"${TAG_CODE_MODEL_PATH}/.tos-tag-model-revision")" == "e9d2a44ca6a05ac6685f3b23709ea57eb7352d5b" ]] || die "semantic model revision is invalid"
for command_name in date find flock git jq rg sed semble sha256sum tar; do
  command -v "${command_name}" >/dev/null 2>&1 || die "${command_name} is required"
done
[[ "$(semble --version 2>/dev/null)" == "0.5.3" ]] || die "Semble 0.5.3 is required"

source_root="$(cd "${TAG_AION_DEVELOPER_PATH}" && pwd -P)"
mkdir -p "${TAG_CODE_SNAPSHOT_ROOT}" "${TAG_CODE_INDEX_ROOT}"
snapshot_root="$(cd "${TAG_CODE_SNAPSHOT_ROOT}" && pwd -P)"
index_root="$(cd "${TAG_CODE_INDEX_ROOT}" && pwd -P)"
freshness_seconds=300

validate_relative() {
  local relative="${1:-}" component base lowered
  [[ -n "${relative}" && "${relative}" != /* ]] || die "path must be relative"
  case "/${relative}/" in
    */../*|*/./*|*//* ) die "path traversal is not permitted" ;;
  esac
  IFS='/' read -r -a components <<<"${relative}"
  for component in "${components[@]}"; do
    lowered="$(printf '%s' "${component}" | tr '[:upper:]' '[:lower:]')"
    case "${lowered}" in
      ''|.|..|.git|.testruns|.config|.codex|.ssh|node_modules)
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

validate_limit() {
  local value="$1" maximum="$2"
  [[ "${value}" =~ ^[0-9]+$ ]] || die "limit must be numeric"
  (( value >= 1 && value <= maximum )) || die "limit is outside the allowed range"
}

list_repositories() {
  find "${source_root}" \
    \( -name .testruns -o -name .config -o -name .codex -o -name node_modules \) -prune -o \
    -mindepth 2 -maxdepth 4 -name .git -print \
    | sed -e "s#^${source_root}/##" -e 's#/.git$##' \
    | LC_ALL=C sort
}

resolve_repository() {
  local relative="$1" resolved top
  validate_relative "${relative}"
  [[ -d "${source_root}/${relative}" ]] || die "repository does not exist"
  resolved="$(cd "${source_root}/${relative}" && pwd -P)"
  case "${resolved}" in
    "${source_root}"/*) ;;
    *) die "repository escapes the configured source root" ;;
  esac
  top="$(git -C "${resolved}" rev-parse --show-toplevel 2>/dev/null || true)"
  [[ -n "${top}" && "${top}" == "${resolved}" ]] || die "path is not a repository root"
  printf '%s' "${relative}"
}

repository_for_path() {
  local relative="$1" candidate selected=""
  validate_relative "${relative}"
  while IFS= read -r candidate; do
    if [[ "${relative}" == "${candidate}" || "${relative}" == "${candidate}/"* ]]; then
      if (( ${#candidate} > ${#selected} )); then
        selected="${candidate}"
      fi
    fi
  done < <(list_repositories)
  [[ -n "${selected}" ]] || die "path is not within a known repository"
  printf '%s' "${selected}"
}

canonical_remote() {
  local remote="$1" path organization repository
  if [[ "${TAG_CODE_TEST_ALLOW_FILE_REMOTE:-}" == "1" && "${remote}" == file://* ]]; then
    printf '%s' "${remote}"
    return
  fi
  case "${remote}" in
    git@github.com:*) path="${remote#git@github.com:}" ;;
    ssh://git@github.com/*) path="${remote#ssh://git@github.com/}" ;;
    https://github.com/*) path="${remote#https://github.com/}" ;;
    *) die "repository origin is not an approved GitHub remote" ;;
  esac
  [[ "${path}" != *'?'* && "${path}" != *'#'* && "${path}" != *'@'* ]] || die "repository origin is invalid"
  path="${path%.git}"
  organization="${path%%/*}"
  repository="${path#*/}"
  [[ "${organization,,}" == "telemetryos" && "${repository}" != "${path}" && "${repository}" =~ ^[A-Za-z0-9._-]+$ ]] || die "repository origin is outside TelemetryOS"
  printf 'https://github.com/telemetryOS/%s.git' "${repository}"
}

git_remote() {
  local remote="$1"
  shift
  if [[ "${remote}" == file://* ]]; then
    git "$@"
    return
  fi
  command -v gh >/dev/null 2>&1 || die "GitHub CLI is required for source refresh"
  [[ -d "${TAG_CODE_GH_CONFIG_DIR}" ]] || die "GitHub authentication is unavailable"
  GH_CONFIG_DIR="${TAG_CODE_GH_CONFIG_DIR}" git -c credential.helper='!gh auth git-credential' "$@"
}

read_cached_receipt() {
  local repository="$1" receipt now fetched_at commit branch snapshot
  receipt="${snapshot_root}/receipts/${repository}.json"
  [[ -f "${receipt}" && ! -L "${receipt}" ]] || return 1
  fetched_at="$(jq -er '.fetched_at_epoch | numbers' "${receipt}" 2>/dev/null)" || return 1
  commit="$(jq -er '.commit | strings' "${receipt}" 2>/dev/null)" || return 1
  branch="$(jq -er '.default_branch | strings' "${receipt}" 2>/dev/null)" || return 1
  [[ "${commit}" =~ ^[0-9a-f]{40}$ && "${branch}" =~ ^[A-Za-z0-9._/-]+$ ]] || return 1
  now="$(date -u +%s)"
  (( now >= fetched_at && now - fetched_at <= freshness_seconds )) || return 1
  snapshot="${snapshot_root}/repos/${repository}/${commit}"
  [[ -d "${snapshot}" && ! -L "${snapshot}" ]] || return 1
  snapshot_repository="${repository}"
  snapshot_branch="${branch}"
  snapshot_commit="${commit}"
  snapshot_fetched_at_epoch="${fetched_at}"
  snapshot_fetched_at="$(jq -er '.fetched_at | strings' "${receipt}")"
  snapshot_path="${snapshot}"
}

write_semantic_ignore() {
  local destination="$1"
  cat >"${destination}/.sembleignore" <<'EOF'
.git/
.testruns/
.config/
.codex/
.ssh/
node_modules/
.env
.env.*
runtime.env
credentials.toml
credentials.json
.npmrc
.pypirc
id_rsa
id_rsa.*
id_ed25519
id_ed25519.*
.sembleignore
EOF
}

prune_old_snapshots() {
  local repository="$1" current_commit="$2" parent old_snapshot cache_key
  parent="${snapshot_root}/repos/${repository}"
  [[ -d "${parent}" ]] || return
  while IFS= read -r -d '' old_snapshot; do
    cache_key="$(printf '%s' "${old_snapshot}" | sha256sum | awk '{print $1}')"
    rm -rf "${index_root}/${cache_key}" "${old_snapshot}"
  done < <(find "${parent}" -mindepth 1 -maxdepth 1 -type d ! -name "${current_commit}" -mmin +10 -print0)
}

refresh_snapshot() {
  local repository="$1" source_repository remote remote_head branch mirror commit destination parent temporary receipt receipt_temporary fetched_at fetched_at_epoch lock_path lock_fd
  source_repository="${source_root}/${repository}"
  remote="$(git -C "${source_repository}" config --get remote.origin.url 2>/dev/null || true)"
  [[ -n "${remote}" ]] || die "repository has no origin remote"
  remote="$(canonical_remote "${remote}")"

  lock_path="${snapshot_root}/locks/${repository//\//__}.lock"
  mkdir -p "$(dirname "${lock_path}")"
  exec {lock_fd}>"${lock_path}"
  flock "${lock_fd}"
  if read_cached_receipt "${repository}"; then
    flock -u "${lock_fd}"
    return
  fi

  remote_head="$(git_remote "${remote}" ls-remote --symref "${remote}" HEAD 2>/dev/null)" || die "default branch refresh failed"
  branch="$(awk '$1 == "ref:" && $2 ~ /^refs\/heads\// && $3 == "HEAD" { sub(/^refs\/heads\//, "", $2); print $2; exit }' <<<"${remote_head}")"
  [[ -n "${branch}" && "${branch}" =~ ^[A-Za-z0-9._/-]+$ && "${branch}" != *'..'* ]] || die "remote default branch is invalid"

  mirror="${snapshot_root}/mirrors/${repository}.git"
  mkdir -p "$(dirname "${mirror}")"
  if [[ ! -d "${mirror}" ]]; then
    git init --bare --quiet "${mirror}"
  fi
  git_remote "${remote}" --git-dir="${mirror}" fetch --quiet --force --depth=1 "${remote}" "refs/heads/${branch}:refs/tos-tag/default" 2>/dev/null || die "default branch fetch failed"
  commit="$(git --git-dir="${mirror}" rev-parse 'refs/tos-tag/default^{commit}' 2>/dev/null || true)"
  [[ "${commit}" =~ ^[0-9a-f]{40}$ ]] || die "default branch commit is invalid"

  destination="${snapshot_root}/repos/${repository}/${commit}"
  if [[ ! -d "${destination}" ]]; then
    parent="$(dirname "${destination}")"
    mkdir -p "${parent}"
    temporary="$(mktemp -d "${parent}/.snapshot.XXXXXX")"
    if ! git --git-dir="${mirror}" archive "${commit}" | tar -x -C "${temporary}"; then
      rm -rf "${temporary}"
      die "default branch snapshot failed"
    fi
    write_semantic_ignore "${temporary}"
    mv "${temporary}" "${destination}"
  fi

  fetched_at_epoch="$(date -u +%s)"
  fetched_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  receipt="${snapshot_root}/receipts/${repository}.json"
  mkdir -p "$(dirname "${receipt}")"
  receipt_temporary="$(mktemp "${receipt}.XXXXXX")"
  jq -n \
    --arg repository "${repository}" \
    --arg default_branch "${branch}" \
    --arg commit "${commit}" \
    --arg fetched_at "${fetched_at}" \
    --argjson fetched_at_epoch "${fetched_at_epoch}" \
    '{repository:$repository,default_branch:$default_branch,commit:$commit,fetched_at:$fetched_at,fetched_at_epoch:$fetched_at_epoch,status:"current"}' \
    >"${receipt_temporary}"
  mv "${receipt_temporary}" "${receipt}"
  prune_old_snapshots "${repository}" "${commit}"
  flock -u "${lock_fd}"
  read_cached_receipt "${repository}" || die "freshness receipt could not be verified"
}

ensure_snapshot() {
  local repository="$1"
  if ! read_cached_receipt "${repository}"; then
    refresh_snapshot "${repository}"
  fi
}

resolve_snapshot_directory() {
  local relative="$1" repository inner resolved
  repository="$(repository_for_path "${relative}")"
  ensure_snapshot "${repository}"
  inner="${relative#"${repository}"}"
  inner="${inner#/}"
  [[ -n "${inner}" ]] || inner="."
  [[ -d "${snapshot_path}/${inner}" ]] || die "directory does not exist on the verified default branch"
  resolved="$(cd "${snapshot_path}/${inner}" && pwd -P)"
  case "${resolved}" in
    "${snapshot_path}"|"${snapshot_path}"/*) resolved_snapshot_directory="${resolved}" ;;
    *) die "path escapes the verified source snapshot" ;;
  esac
}

resolve_snapshot_file() {
  local relative="$1" repository inner directory target resolved_directory
  repository="$(repository_for_path "${relative}")"
  ensure_snapshot "${repository}"
  inner="${relative#"${repository}/"}"
  [[ "${inner}" != "${relative}" ]] || die "file path must include a repository"
  validate_relative "${inner}"
  directory="$(dirname "${inner}")"
  [[ -d "${snapshot_path}/${directory}" ]] || die "directory does not exist on the verified default branch"
  resolved_directory="$(cd "${snapshot_path}/${directory}" && pwd -P)"
  case "${resolved_directory}" in
    "${snapshot_path}"|"${snapshot_path}"/*) ;;
    *) die "path escapes the verified source snapshot" ;;
  esac
  target="${resolved_directory}/$(basename "${inner}")"
  [[ -f "${target}" && ! -L "${target}" ]] || die "file does not exist or is not a regular file"
  resolved_snapshot_file="${target}"
}

rg_files() {
  rg --files --hidden \
    --glob '!.git/**' --glob '!**/.git/**' \
    --glob '!.testruns/**' --glob '!**/.testruns/**' \
    --glob '!.config/**' --glob '!**/.config/**' \
    --glob '!.codex/**' --glob '!**/.codex/**' \
    --glob '!.ssh/**' --glob '!**/.ssh/**' \
    --glob '!node_modules/**' --glob '!**/node_modules/**' \
    --glob '!.env' --glob '!**/.env' --glob '!.env.*' --glob '!**/.env.*' \
    --glob '!runtime.env' --glob '!**/runtime.env' \
    --glob '!credentials.toml' --glob '!**/credentials.toml' \
    --glob '!credentials.json' --glob '!**/credentials.json' \
    --glob '!.sembleignore' --glob '!**/.sembleignore' \
    "$@"
}

verb="${1:-}"
shift || true
case "${verb}" in
  repos)
    [[ "$#" -eq 0 ]] || die "repos accepts no arguments"
    list_repositories
    ;;
  freshness)
    [[ "$#" -eq 1 ]] || die "freshness requires a repository"
    repository="$(resolve_repository "$1")"
    ensure_snapshot "${repository}"
    jq -n \
      --arg repository "${snapshot_repository}" \
      --arg default_branch "${snapshot_branch}" \
      --arg commit "${snapshot_commit}" \
      --arg fetched_at "${snapshot_fetched_at}" \
      --argjson max_age_seconds "${freshness_seconds}" \
      '{repository:$repository,default_branch:$default_branch,commit:$commit,fetched_at:$fetched_at,max_age_seconds:$max_age_seconds,status:"current"}'
    ;;
  files)
    [[ "$#" -le 2 ]] || die "files accepts a repository-scoped directory and optional limit"
    relative="${1:-}"
    limit="${2:-200}"
    validate_limit "${limit}" 500
    resolve_snapshot_directory "${relative}"
    directory="${resolved_snapshot_directory}"
    inner="${relative#"${snapshot_repository}"}"
    inner="${inner#/}"
    [[ -n "${inner}" ]] || inner="."
    (cd "${snapshot_path}" && { status=0; rg_files "${inner}" || status=$?; (( status <= 1 )) || exit "${status}"; }) \
      | awk -v repository="${snapshot_repository}" -v limit="${limit}" 'NR <= limit { print repository "/" $0 }'
    ;;
  search)
    [[ "$#" -ge 2 && "$#" -le 3 ]] || die "search requires a fixed string, repository-scoped directory, and optional limit"
    pattern="$1"
    relative="$2"
    limit="${3:-200}"
    [[ -n "${pattern}" && "${#pattern}" -le 256 ]] || die "search string must be 1-256 characters"
    validate_limit "${limit}" 500
    resolve_snapshot_directory "${relative}"
    directory="${resolved_snapshot_directory}"
    inner="${relative#"${snapshot_repository}"}"
    inner="${inner#/}"
    [[ -n "${inner}" ]] || inner="."
    (cd "${snapshot_path}" && { status=0; rg -F -n --no-heading --color never \
      --hidden \
      --glob '!.git/**' --glob '!**/.git/**' \
      --glob '!.testruns/**' --glob '!**/.testruns/**' \
      --glob '!.config/**' --glob '!**/.config/**' \
      --glob '!.codex/**' --glob '!**/.codex/**' \
      --glob '!.ssh/**' --glob '!**/.ssh/**' \
      --glob '!node_modules/**' --glob '!**/node_modules/**' \
      --glob '!.env' --glob '!**/.env' --glob '!.env.*' --glob '!**/.env.*' \
      --glob '!runtime.env' --glob '!**/runtime.env' \
      --glob '!credentials.toml' --glob '!**/credentials.toml' \
      --glob '!credentials.json' --glob '!**/credentials.json' \
      --glob '!.sembleignore' --glob '!**/.sembleignore' \
      -- "${pattern}" "${inner}" || status=$?; (( status <= 1 )) || exit "${status}"; }) \
      | awk -v repository="${snapshot_repository}" -v limit="${limit}" 'NR <= limit { print repository "/" $0 }'
    ;;
  semantic-search)
    [[ "$#" -ge 2 && "$#" -le 4 ]] || die "semantic-search requires a repository, query, optional result limit, and optional snippet-line limit"
    repository="$(resolve_repository "$1")"
    query="$2"
    top_k="${3:-5}"
    snippet_lines="${4:-12}"
    [[ -n "${query}" && "${#query}" -le 512 ]] || die "semantic query must be 1-512 characters"
    validate_limit "${top_k}" 8
    validate_limit "${snippet_lines}" 40
    ensure_snapshot "${repository}"
    semantic_error="$(mktemp "${TMPDIR:-/tmp}/tos-tag-semble.XXXXXX")"
    if ! semantic_output="$(
      env -u GH_CONFIG_DIR \
        SEMBLE_CACHE_LOCATION="${index_root}" \
        SEMBLE_GRAMMARS_CACHE_DIR="${index_root}/grammars" \
        SEMBLE_MODEL_NAME="${TAG_CODE_MODEL_PATH}" \
        HF_HUB_OFFLINE=1 \
        TRANSFORMERS_OFFLINE=1 \
        NO_COLOR=1 \
        semble search "${query}" "${snapshot_path}" --top-k "${top_k}" --max-snippet-lines "${snippet_lines}" --content all \
        2>"${semantic_error}"
    )"; then
      rm -f "${semantic_error}"
      die "semantic search failed"
    fi
    rm -f "${semantic_error}"
    jq -e '.results | arrays' <<<"${semantic_output}" >/dev/null 2>&1 || die "semantic search returned invalid output"
    while IFS= read -r result_path; do
      validate_relative "${result_path}"
    done < <(jq -r '.results[].file_path' <<<"${semantic_output}")
    jq \
      --arg repository "${snapshot_repository}" \
      --arg default_branch "${snapshot_branch}" \
      --arg commit "${snapshot_commit}" \
      --arg fetched_at "${snapshot_fetched_at}" \
      '{repository:$repository,default_branch:$default_branch,commit:$commit,fetched_at:$fetched_at,results:[.results[] | .file_path=($repository + "/" + .file_path)]}' \
      <<<"${semantic_output}"
    ;;
  versions)
    [[ "$#" -eq 2 ]] || die "versions requires a repository directory and ecosystem"
    repository="$(resolve_repository "$1")"
    ecosystem="$(printf '%s' "$2" | tr '[:upper:]' '[:lower:]')"
    ensure_snapshot "${repository}"
    directory="${snapshot_path}"
    case "${ecosystem}" in
      go)
        found=0
        for candidate in go.mod go.work .tool-versions Dockerfile Dockerfile.dev; do
          [[ -f "${directory}/${candidate}" && ! -L "${directory}/${candidate}" ]] || continue
          case "${candidate}" in
            go.mod|go.work)
              while IFS= read -r line; do
                printf '%s:%s\n' "${repository}/${candidate}" "${line}"
                found=1
              done < <(rg -n --no-heading --color never '^(go|toolchain)[[:space:]]+' "${directory}/${candidate}" || true)
              ;;
            .tool-versions)
              while IFS= read -r line; do
                printf '%s:%s\n' "${repository}/${candidate}" "${line}"
                found=1
              done < <(rg -n --no-heading --color never '^golang[[:space:]]+' "${directory}/${candidate}" || true)
              ;;
            Dockerfile|Dockerfile.dev)
              while IFS= read -r line; do
                printf '%s:%s\n' "${repository}/${candidate}" "${line}"
                found=1
              done < <(rg -n --no-heading --color never '(^|[[:space:]])(FROM[[:space:]]+)?golang:' "${directory}/${candidate}" || true)
              ;;
          esac
        done
        while IFS= read -r workflow; do
          while IFS= read -r line; do
            printf '%s:%s\n' "${repository}/${workflow#"${directory}/"}" "${line}"
            found=1
          done < <(rg -n --no-heading --color never '(go-version|GO_VERSION)' "${workflow}" || true)
        done < <(find "${directory}/.github/workflows" -maxdepth 1 -type f \( -name '*.yml' -o -name '*.yaml' \) 2>/dev/null | LC_ALL=C sort)
        (( found == 1 )) || die "no Go version evidence found"
        ;;
      *) die "supported version ecosystems: go" ;;
    esac
    ;;
  read)
    [[ "$#" -ge 1 && "$#" -le 3 ]] || die "read requires a repository-scoped file and optional start/end lines"
    relative="$1"
    start="${2:-1}"
    end="${3:-400}"
    [[ "${start}" =~ ^[0-9]+$ && "${end}" =~ ^[0-9]+$ ]] || die "line bounds must be numeric"
    (( start >= 1 && end >= start && end - start + 1 <= 400 )) || die "line range must contain 1-400 lines"
    resolve_snapshot_file "${relative}"
    file="${resolved_snapshot_file}"
    size="$(wc -c <"${file}" | tr -d '[:space:]')"
    (( size <= 2097152 )) || die "file exceeds the read size limit"
    sed -n "${start},${end}p" "${file}" \
      | awk -v line="${start}" '{ printf "%d:%s\n", line + NR - 1, $0 }'
    ;;
  *)
    die "supported verbs: repos, freshness, files, search, semantic-search, read, versions"
    ;;
esac
