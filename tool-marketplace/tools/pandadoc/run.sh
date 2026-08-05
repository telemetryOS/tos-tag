#!/usr/bin/env bash
set -euo pipefail

cli_version="1.0.0"

die() {
  printf 'pandadoc: %s\n' "$1" >&2
  exit 2
}

reject() {
  printf 'pandadoc: %s\n' "$1" >&2
  exit 3
}

if [[ "${1:-}" == version || "${1:-}" == --version ]]; then
  printf 'pandadoc-cli %s\n' "${cli_version}"
  exit 0
fi

if [[ "${1:-}" == help || "${1:-}" == --help || "${1:-}" == -h ]]; then
  printf '%s\n' 'usage: pandadoc <reviewed-command> [resource-id] [--query <json-object>] [--data <json-object>]'
  exit 0
fi

command -v curl >/dev/null 2>&1 || die "curl is not installed"
command -v jq >/dev/null 2>&1 || die "jq is not installed"
command -v base64 >/dev/null 2>&1 || die "base64 is not installed"

operation="${TOS_TAG_OPERATION_ID:-}"
if [[ -z "${operation}" ]]; then
  [[ "$(basename "$0")" == pandadoc ]] || die "TOS_TAG_OPERATION_ID is required"
  operation="cli"
fi

command_name="${1:-}"
[[ -n "${command_name}" ]] || die "a reviewed command is required"
shift

method=""
path=""
required_operation=""
expected_identifiers=0
query_allowed=false
body_required=false

case "${command_name}" in
  documents) method=GET; path=/public/v1/documents; required_operation=read; query_allowed=true ;;
  document) method=GET; path=/public/v1/documents/%s; required_operation=read; expected_identifiers=1 ;;
  document-details) method=GET; path=/public/v1/documents/%s/details; required_operation=read; expected_identifiers=1 ;;
  document-fields) method=GET; path=/public/v1/documents/%s/fields; required_operation=read; expected_identifiers=1 ;;
  document-sections) method=GET; path=/public/v1/documents/%s/sections; required_operation=read; expected_identifiers=1 ;;
  document-audit-trail) method=GET; path=/public/v2/documents/%s/audit-trail; required_operation=read; expected_identifiers=1 ;;
  document-settings) method=GET; path=/public/v2/documents/%s/settings; required_operation=read; expected_identifiers=1 ;;
  templates) method=GET; path=/public/v1/templates; required_operation=read; query_allowed=true ;;
  template) method=GET; path=/public/v1/templates/%s; required_operation=read; expected_identifiers=1 ;;
  template-details) method=GET; path=/public/v1/templates/%s/details; required_operation=read; expected_identifiers=1 ;;
  content-items) method=GET; path=/public/v1/content-library-items; required_operation=read; query_allowed=true ;;
  content-item) method=GET; path=/public/v1/content-library-items/%s; required_operation=read; expected_identifiers=1 ;;
  content-item-details) method=GET; path=/public/v1/content-library-items/%s/details; required_operation=read; expected_identifiers=1 ;;
  forms) method=GET; path=/public/v1/forms; required_operation=read; query_allowed=true ;;
  document-folders) method=GET; path=/public/v1/documents/folders; required_operation=read; query_allowed=true ;;
  template-folders) method=GET; path=/public/v1/templates/folders; required_operation=read; query_allowed=true ;;
  contacts) method=GET; path=/public/v1/contacts; required_operation=read; query_allowed=true ;;
  contact) method=GET; path=/public/v1/contacts/%s; required_operation=read; expected_identifiers=1 ;;
  members) method=GET; path=/public/v1/members; required_operation=read; query_allowed=true ;;
  current-member) method=GET; path=/public/v1/members/current; required_operation=read ;;
  member) method=GET; path=/public/v1/members/%s; required_operation=read; expected_identifiers=1 ;;
  create-document) method=POST; path=/public/v1/documents; required_operation=write; body_required=true ;;
  update-document) method=PATCH; path=/public/v1/documents/%s; required_operation=write; expected_identifiers=1; body_required=true ;;
  send-document) method=POST; path=/public/v1/documents/%s/send; required_operation=write; expected_identifiers=1; body_required=true ;;
  draft-document) method=POST; path=/public/v1/documents/%s/draft; required_operation=write; expected_identifiers=1; body_required=true ;;
  set-document-status) method=PATCH; path=/public/v1/documents/%s/status; required_operation=write; expected_identifiers=1; body_required=true ;;
  update-document-fields) method=PATCH; path=/public/v1/documents/%s/fields; required_operation=write; expected_identifiers=1; body_required=true ;;
  add-document-fields) method=POST; path=/public/v1/documents/%s/fields; required_operation=write; expected_identifiers=1; body_required=true ;;
  create-contact) method=POST; path=/public/v1/contacts; required_operation=write; body_required=true ;;
  update-contact) method=PATCH; path=/public/v1/contacts/%s; required_operation=write; expected_identifiers=1; body_required=true ;;
  delete-document) method=DELETE; path=/public/v1/documents/%s; required_operation=delete; expected_identifiers=1 ;;
  delete-contact) method=DELETE; path=/public/v1/contacts/%s; required_operation=delete; expected_identifiers=1 ;;
  *) die "unsupported command: ${command_name}" ;;
esac

[[ "${operation}" == cli || "${operation}" == "${required_operation}" ]] || die "command '${command_name}' is not permitted by operation '${operation}'"

identifiers=()
while (( ${#identifiers[@]} < expected_identifiers )); do
  (( $# > 0 )) || die "command '${command_name}' requires ${expected_identifiers} resource identifier(s)"
  identifier="$1"
  (( ${#identifier} >= 1 && ${#identifier} <= 128 )) || die "resource identifier has unsupported length"
  [[ "${identifier}" != -* && "${identifier}" != *'..'* && "${identifier}" != *$'\n'* && "${identifier}" != *$'\r'* ]] || die "resource identifier is malformed"
  [[ "${identifier}" =~ ^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$ ]] || die "resource identifier has unsupported characters"
  identifiers+=("${identifier}")
  shift
done

for identifier in "${identifiers[@]}"; do
  path="${path/\%s/${identifier}}"
done

query_json=""
body_json=""
while (( $# > 0 )); do
  case "$1" in
    --query)
      [[ "${query_allowed}" == true ]] || die "--query is not supported by '${command_name}'"
      [[ -z "${query_json}" ]] || die "--query may be supplied only once"
      (( $# >= 2 )) || die "--query requires a JSON object"
      query_json="$2"
      shift 2
      ;;
    --data)
      [[ "${body_required}" == true ]] || die "--data is not supported by '${command_name}'"
      [[ -z "${body_json}" ]] || die "--data may be supplied only once"
      (( $# >= 2 )) || die "--data requires a JSON object"
      body_json="$2"
      shift 2
      ;;
    *) die "unsupported argument: $1" ;;
  esac
done

if [[ -n "${query_json}" ]]; then
  (( ${#query_json} <= 8192 )) || die "--query is too large"
  [[ "${query_json}" != *$'\n'* && "${query_json}" != *$'\r'* ]] || die "--query must be single-line JSON"
  jq -e '
    type == "object" and length <= 24 and
    all(to_entries[];
      (.key | test("^(count|page|q|tag|status|status__ne|deleted|template_id|form_id|folder_uuid|contact_id|membership_id|order_by|created_from|created_to|completed_from|completed_to|modified_from|modified_to|email|metadata_[A-Za-z0-9_.-]{1,64})$")) and
      (.value | (type == "string" or type == "number" or type == "boolean"))
    ) and
    ((.count // 1) | type == "number" and floor == . and . >= 1 and . <= 100) and
    ((.page // 1) | type == "number" and floor == . and . >= 1) and
    ((.status // 0) | type == "number" and floor == . and . >= 0 and . <= 14) and
    ((.status__ne // 0) | type == "number" and floor == . and . >= 0 and . <= 14)
  ' <<<"${query_json}" >/dev/null || die "--query has unsupported fields or values"
fi

if [[ "${body_required}" == true ]]; then
  [[ -n "${body_json}" ]] || die "command '${command_name}' requires --data"
  (( ${#body_json} <= 65536 )) || die "--data is too large"
  [[ "${body_json}" != *$'\n'* && "${body_json}" != *$'\r'* ]] || die "--data must be single-line JSON"
  jq -e 'type == "object"' <<<"${body_json}" >/dev/null || die "--data must be a JSON object"
fi

api_key="${PANDA_DOC_API_KEY:-}"
(( ${#api_key} >= 16 && ${#api_key} <= 4096 )) || die "PANDA_DOC_API_KEY is missing or malformed"
[[ "${api_key}" != *$'\n'* && "${api_key}" != *$'\r'* && "${api_key}" =~ ^[-A-Za-z0-9._~]+$ ]] || die "PANDA_DOC_API_KEY is malformed"

auth_config="$(mktemp "${TMPDIR:-/tmp}/tos-tag-pandadoc-auth.XXXXXX")"
body_file="$(mktemp "${TMPDIR:-/tmp}/tos-tag-pandadoc-body.XXXXXX")"
response_file="$(mktemp "${TMPDIR:-/tmp}/tos-tag-pandadoc-response.XXXXXX")"
error_file="$(mktemp "${TMPDIR:-/tmp}/tos-tag-pandadoc-error.XXXXXX")"
cleanup() {
  rm -f "${auth_config}" "${body_file}" "${response_file}" "${error_file}"
}
trap cleanup EXIT
chmod 0600 "${auth_config}" "${body_file}" "${response_file}" "${error_file}"
printf 'header = "Authorization: API-Key %s"\n' "${api_key}" >"${auth_config}"
if [[ -n "${body_json}" ]]; then
  printf '%s' "${body_json}" >"${body_file}"
fi

curl_args=(
  --config "${auth_config}"
  --silent
  --show-error
  --max-time 90
  --max-filesize 1048576
  --request "${method}"
  --output "${response_file}"
  --write-out '%{http_code}'
  --header 'Accept: application/json'
)

if [[ -n "${query_json}" ]]; then
  curl_args+=(--get)
  while IFS= read -r encoded; do
    entry="$(printf '%s' "${encoded}" | base64 --decode)"
    key="$(jq -r '.key' <<<"${entry}")"
    value="$(jq -r '.value | tostring' <<<"${entry}")"
    curl_args+=(--data-urlencode "${key}=${value}")
  done < <(jq -r 'to_entries[] | @base64' <<<"${query_json}")
fi

if [[ -n "${body_json}" ]]; then
  curl_args+=(--header 'Content-Type: application/json' --data-binary "@${body_file}")
fi

endpoint="https://api.pandadoc.com${path}"
set +e
http_status="$(curl "${curl_args[@]}" "${endpoint}" 2>"${error_file}")"
curl_status=$?
set -e
unset api_key PANDA_DOC_API_KEY

if (( curl_status != 0 )); then
  reject "API request failed before receiving a valid response"
fi
[[ "${http_status}" =~ ^[0-9]{3}$ ]] || reject "API request returned an invalid status"
if (( 10#${http_status} < 200 || 10#${http_status} >= 300 )); then
  reject "API request failed with HTTP ${http_status}"
fi

if [[ ! -s "${response_file}" ]]; then
  jq -n --arg command "${command_name}" --arg status "${http_status}" '{ok:true,command:$command,http_status:($status|tonumber)}'
  exit 0
fi
jq -e . "${response_file}" >/dev/null 2>&1 || die "PandaDoc returned non-JSON output"
jq . "${response_file}"
