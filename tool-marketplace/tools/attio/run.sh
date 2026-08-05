#!/usr/bin/env bash
set -euo pipefail

die() {
  printf 'attio: %s\n' "$1" >&2
  exit 2
}

reject() {
  printf 'attio: %s\n' "$1" >&2
  exit 3
}

command -v curl >/dev/null 2>&1 || die "curl is not installed"
command -v jq >/dev/null 2>&1 || die "jq is not installed"

operation="${TOS_TAG_OPERATION_ID:-}"
command_name="${1:-}"
path="${2:-}"
[[ -n "${command_name}" && -n "${path}" ]] || die "usage: attio.sh get|query|post|put|patch|delete <documented-/v2-path> [--query <json-object>] [--data <json-object>]"
shift 2

case "${operation}:${command_name}" in
  read:get|read:query|write:post|write:put|write:patch|delete:delete) ;;
  *) die "command '${command_name}' is not permitted by operation '${operation}'" ;;
esac

(( ${#path} <= 512 )) || die "path is too long"
[[ "${path}" == /v2/* && "${path}" != *'?'* && "${path}" != *'#'* && "${path}" != *'..'* ]] || die "path must be a documented /v2 path without query, fragment, or traversal"
[[ "${path}" =~ ^/v2/[-A-Za-z0-9._~/]+$ ]] || die "path contains unsupported characters"

query_json='{}'
data_json=''
query_set=false
data_set=false
while (( $# > 0 )); do
  case "$1" in
    --query)
      (( $# >= 2 )) || die "--query requires a JSON object"
      [[ "${query_set}" == false ]] || die "--query may be supplied only once"
      query_json="$2"
      query_set=true
      shift 2
      ;;
    --data)
      (( $# >= 2 )) || die "--data requires a JSON object"
      [[ "${data_set}" == false ]] || die "--data may be supplied only once"
      data_json="$2"
      data_set=true
      shift 2
      ;;
    *) die "unsupported argument: $1" ;;
  esac
done

(( ${#query_json} <= 16384 )) || die "query JSON is too large"
jq -e 'type == "object"' <<<"${query_json}" >/dev/null 2>&1 || die "--query must be a valid JSON object"
case "${command_name}" in
  get|delete)
    [[ "${data_set}" == false ]] || die "${command_name} does not accept --data"
    ;;
  query|post|put|patch)
    [[ "${data_set}" == true ]] || die "${command_name} requires --data"
    (( ${#data_json} <= 65536 )) || die "request JSON is too large"
    jq -e 'type == "object"' <<<"${data_json}" >/dev/null 2>&1 || die "--data must be a valid JSON object"
    ;;
esac

segment='[A-Za-z0-9][-A-Za-z0-9._~]{0,127}'

valid_get_path() {
  [[ "${path}" == "/v2/objects" || "${path}" == "/v2/lists" || "${path}" == "/v2/workspace_members" ||
     "${path}" == "/v2/notes" || "${path}" == "/v2/tasks" || "${path}" == "/v2/threads" ||
     "${path}" == "/v2/emails" || "${path}" == "/v2/meetings" || "${path}" == "/v2/files" ||
     "${path}" == "/v2/webhooks" || "${path}" == "/v2/self" ]] && return 0
  [[ "${path}" =~ ^/v2/objects/${segment}$ || "${path}" =~ ^/v2/objects/${segment}/views$ ]] && return 0
  [[ "${path}" =~ ^/v2/(objects|lists)/${segment}/attributes$ ||
     "${path}" =~ ^/v2/(objects|lists)/${segment}/attributes/${segment}$ ||
     "${path}" =~ ^/v2/(objects|lists)/${segment}/attributes/${segment}/(options|statuses)$ ]] && return 0
  [[ "${path}" =~ ^/v2/objects/${segment}/records/${segment}$ ||
     "${path}" =~ ^/v2/objects/${segment}/records/${segment}/entries$ ||
     "${path}" =~ ^/v2/objects/${segment}/records/${segment}/attributes/${segment}/values$ ]] && return 0
  [[ "${path}" =~ ^/v2/lists/${segment}$ || "${path}" =~ ^/v2/lists/${segment}/views$ ||
     "${path}" =~ ^/v2/lists/${segment}/entries/${segment}$ ||
     "${path}" =~ ^/v2/lists/${segment}/entries/${segment}/attributes/${segment}/values$ ]] && return 0
  [[ "${path}" =~ ^/v2/workspace_members/${segment}$ || "${path}" =~ ^/v2/notes/${segment}$ ||
     "${path}" =~ ^/v2/tasks/${segment}$ || "${path}" =~ ^/v2/threads/${segment}$ ||
     "${path}" =~ ^/v2/comments/${segment}$ || "${path}" =~ ^/v2/meetings/${segment}$ ||
     "${path}" =~ ^/v2/files/${segment}$ || "${path}" =~ ^/v2/webhooks/${segment}$ ]] && return 0
  [[ "${path}" =~ ^/v2/meetings/${segment}/call_recordings$ ||
     "${path}" =~ ^/v2/meetings/${segment}/call_recordings/${segment}$ ||
     "${path}" =~ ^/v2/meetings/${segment}/call_recordings/${segment}/transcript$ ]] && return 0
  return 1
}

valid_query_path() {
  [[ "${path}" == "/v2/objects/records/search" || "${path}" == "/v2/sql" ]] && return 0
  [[ "${path}" =~ ^/v2/objects/${segment}/records/query$ ||
     "${path}" =~ ^/v2/lists/${segment}/entries/query$ ]] && return 0
  return 1
}

valid_post_path() {
  [[ "${path}" == "/v2/objects" || "${path}" == "/v2/lists" || "${path}" == "/v2/notes" ||
     "${path}" == "/v2/tasks" || "${path}" == "/v2/comments" || "${path}" == "/v2/meetings" ||
     "${path}" == "/v2/webhooks" ]] && return 0
  [[ "${path}" =~ ^/v2/(objects|lists)/${segment}/attributes$ ||
     "${path}" =~ ^/v2/(objects|lists)/${segment}/attributes/${segment}/(options|statuses)$ ]] && return 0
  [[ "${path}" =~ ^/v2/objects/${segment}/records$ ||
     "${path}" =~ ^/v2/objects/${segment}/records/merge$ ||
     "${path}" =~ ^/v2/lists/${segment}/entries$ ||
     "${path}" =~ ^/v2/meetings/${segment}/call_recordings$ ]] && return 0
  return 1
}

valid_put_path() {
  [[ "${path}" =~ ^/v2/objects/${segment}/records$ ||
     "${path}" =~ ^/v2/objects/${segment}/records/${segment}$ ||
     "${path}" =~ ^/v2/lists/${segment}/entries$ ||
     "${path}" =~ ^/v2/lists/${segment}/entries/${segment}$ ]]
}

valid_patch_path() {
  [[ "${path}" =~ ^/v2/objects/${segment}$ || "${path}" =~ ^/v2/lists/${segment}$ ||
     "${path}" =~ ^/v2/tasks/${segment}$ || "${path}" =~ ^/v2/webhooks/${segment}$ ]] && return 0
  [[ "${path}" =~ ^/v2/(objects|lists)/${segment}/attributes/${segment}$ ||
     "${path}" =~ ^/v2/(objects|lists)/${segment}/attributes/${segment}/options/${segment}$ ||
     "${path}" =~ ^/v2/(objects|lists)/${segment}/attributes/${segment}/statuses/${segment}$ ]] && return 0
  [[ "${path}" =~ ^/v2/objects/${segment}/records/${segment}$ ||
     "${path}" =~ ^/v2/lists/${segment}/entries/${segment}$ ]] && return 0
  return 1
}

valid_delete_path() {
  [[ "${path}" =~ ^/v2/objects/${segment}/records/${segment}$ ||
     "${path}" =~ ^/v2/lists/${segment}/entries/${segment}$ ||
     "${path}" =~ ^/v2/notes/${segment}$ || "${path}" =~ ^/v2/tasks/${segment}$ ||
     "${path}" =~ ^/v2/comments/${segment}$ || "${path}" =~ ^/v2/files/${segment}$ ||
     "${path}" =~ ^/v2/webhooks/${segment}$ ||
     "${path}" =~ ^/v2/meetings/${segment}/call_recordings/${segment}$ ]]
}

case "${command_name}" in
  get) valid_get_path || die "GET path is not in the reviewed Attio JSON endpoint catalog" ;;
  query) valid_query_path || die "read-query path is not in the reviewed Attio endpoint catalog" ;;
  post) valid_post_path || die "POST path is not in the reviewed Attio JSON endpoint catalog" ;;
  put) valid_put_path || die "PUT path is not in the reviewed Attio endpoint catalog" ;;
  patch) valid_patch_path || die "PATCH path is not in the reviewed Attio endpoint catalog" ;;
  delete) valid_delete_path || die "DELETE path is not in the reviewed Attio endpoint catalog" ;;
esac

case "${command_name}" in
  get) method=GET ;;
  query|post) method=POST ;;
  put) method=PUT ;;
  patch) method=PATCH ;;
  delete) method=DELETE ;;
esac

query_pairs="$(jq -r '
  def scalar:
    if type == "string" or type == "number" or type == "boolean" then tostring
    else error("query values must be scalar or arrays of scalars") end;
  to_entries[] |
  .key as $key |
  if ($key | test("^[A-Za-z][A-Za-z0-9_]{0,63}$") | not) then error("invalid query key")
  elif (.value | type) == "array" then .value[] | "\($key | @uri)=\(scalar | @uri)"
  else "\($key | @uri)=\(.value | scalar | @uri)"
  end
' <<<"${query_json}")" || die "--query contains an unsupported key or value"
query_string=''
while IFS= read -r pair; do
  [[ -n "${pair}" ]] || continue
  if [[ -n "${query_string}" ]]; then query_string+='&'; fi
  query_string+="${pair}"
done <<<"${query_pairs}"

endpoint="https://api.attio.com${path}"
if [[ -n "${query_string}" ]]; then endpoint+="?${query_string}"; fi

access_token="${ATTIO_ACCESS_TOKEN:-}"
(( ${#access_token} >= 16 && ${#access_token} <= 4096 )) || die "ATTIO_ACCESS_TOKEN is missing or malformed"
[[ "${access_token}" =~ ^[-A-Za-z0-9._~]+$ ]] || die "ATTIO_ACCESS_TOKEN is malformed"

auth_config="$(mktemp "${TMPDIR:-/tmp}/tos-tag-attio-auth.XXXXXX")"
response_file="$(mktemp "${TMPDIR:-/tmp}/tos-tag-attio-response.XXXXXX")"
cleanup() {
  rm -f "${auth_config}" "${response_file}"
}
trap cleanup EXIT
chmod 0600 "${auth_config}" "${response_file}"
printf 'header = "Authorization: Bearer %s"\n' "${access_token}" >"${auth_config}"
unset access_token ATTIO_ACCESS_TOKEN

curl_args=(
  --config "${auth_config}"
  --silent
  --show-error
  --proto '=https'
  --tlsv1.2
  --connect-timeout 10
  --max-time 75
  --request "${method}"
  --header 'Accept: application/json'
  --output "${response_file}"
  --write-out '%{http_code}'
  --url "${endpoint}"
)
if [[ "${data_set}" == true ]]; then
  curl_args+=(--header 'Content-Type: application/json' --data-binary "${data_json}")
fi

status="$(curl "${curl_args[@]}")" || die "request failed"
[[ "${status}" =~ ^[0-9]{3}$ ]] || die "request returned an invalid HTTP status"
if (( status < 200 || status >= 300 )); then
  if jq -e 'type == "object"' "${response_file}" >/dev/null 2>&1; then
    summary="$(jq -c --argjson http_status "${status}" '{http_status:$http_status,type:(.type // null),code:(.code // null),message:(.message // "Attio rejected the request")}' "${response_file}")"
    reject "API rejected: ${summary}"
  fi
  reject "API rejected with HTTP ${status}"
fi

if [[ ! -s "${response_file}" ]]; then
  jq -n --argjson status_code "${status}" '{ok:true,status_code:$status_code}'
  exit 0
fi
jq -e . "${response_file}" >/dev/null 2>&1 || die "Attio returned non-JSON output"
jq . "${response_file}"
