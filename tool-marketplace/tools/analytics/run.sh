#!/usr/bin/env bash
set -euo pipefail

die() {
  printf 'analytics: %s\n' "$1" >&2
  exit 2
}

[[ "${TOS_TAG_OPERATION_ID:-}" == "read" ]] || die "only the read operation is supported"
command -v curl >/dev/null 2>&1 || die "curl is not installed"
command -v jq >/dev/null 2>&1 || die "jq is not installed"

base_url="${TELEMETRYOS_ANALYTICS_URL:-}"
base_url="${base_url%/}"
case "${base_url}" in
  https://api.telemetryos.com|https://qa-api.telemetryos.com) ;;
  *) die "TELEMETRYOS_ANALYTICS_URL must be an approved TelemetryOS Gateway origin" ;;
esac

site_token="${SITE_ANALYTICS_TOKEN:-}"
[[ "${site_token}" =~ ^s[0-9a-f]{32}$ ]] || die "SITE_ANALYTICS_TOKEN has an invalid format"

auth_config="$(mktemp "${TMPDIR:-/tmp}/tos-tag-analytics-auth.XXXXXX")"
response_file="$(mktemp "${TMPDIR:-/tmp}/tos-tag-analytics-response.XXXXXX")"
header_file="$(mktemp "${TMPDIR:-/tmp}/tos-tag-analytics-headers.XXXXXX")"
cleanup() {
  rm -f "${auth_config}" "${response_file}" "${header_file}"
}
trap cleanup EXIT
chmod 0600 "${auth_config}" "${response_file}" "${header_file}"
printf 'header = "Authorization: Token %s"\n' "${site_token}" >"${auth_config}"
unset site_token SITE_ANALYTICS_TOKEN

urlencode() {
  jq -nr --arg value "$1" '$value|@uri'
}

query=""
add_query() {
  local key="$1" value="$2" encoded
  encoded="$(urlencode "${value}")"
  if [[ -n "${query}" ]]; then
    query+="&"
  fi
  query+="${key}=${encoded}"
}

positive_int() {
  local value="$1" maximum="$2" label="$3"
  [[ "${value}" =~ ^[0-9]+$ ]] || die "${label} must be an integer"
  (( value >= 1 && value <= maximum )) || die "${label} is outside the allowed range"
}

safe_key() {
  [[ "$1" =~ ^[a-z0-9][a-z0-9_-]{0,63}$ ]] || die "$2 has an invalid format"
}

safe_key_csv() {
  local value="$1" label="$2" item
  local -a items
  IFS=',' read -r -a items <<<"${value}"
  (( ${#items[@]} >= 1 && ${#items[@]} <= 20 )) || die "${label} has too many values"
  for item in "${items[@]}"; do
    safe_key "${item}" "${label}"
  done
}

safe_site_path() {
  (( ${#1} <= 256 )) || die "$2 is too long"
  [[ "$1" =~ ^/[A-Za-z0-9._~/-]*$ ]] || die "$2 has an invalid format"
  [[ "$1" != *".."* ]] || die "$2 has an invalid format"
}

account_id() {
  [[ "$1" =~ ^[a-f0-9]{24}$ ]] || die "$2 must be a 24-hex account ID"
}

rfc3339() {
  [[ "$1" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?(Z|[+-][0-9]{2}:[0-9]{2})$ ]] || die "$2 must be RFC3339"
}

[[ "$#" -ge 1 ]] || die "a reviewed Analytics operation is required"
action="$1"
shift
path=""
site_events=false
site_events_page=1
site_events_per_page=50
site_events_bot_filter=""

case "${action}" in
  pipeline)
    [[ "$#" -eq 0 ]] || die "pipeline accepts no arguments"
    path="/reporting/funnel/pipeline"
    ;;
  insights)
    path="/reporting/funnel/insights"
    while [[ "$#" -gt 0 ]]; do
      case "$1" in
        --months)
          [[ "$#" -ge 2 ]] || die "--months requires a value"
          positive_int "$2" 24 "months"
          add_query months "$2"
          shift 2 ;;
        --segment)
          [[ "$#" -ge 2 ]] || die "--segment requires a value"
          [[ "$2" == "humans" || "$2" == "bots" || "$2" == "all" ]] || die "segment must be humans, bots, or all"
          add_query segment "$2"
          shift 2 ;;
        --exclude-agentic)
          [[ "$#" -ge 2 ]] || die "--exclude-agentic requires a value"
          [[ "$2" == "true" || "$2" == "false" ]] || die "exclude-agentic must be true or false"
          add_query exclude_agentic "$2"
          shift 2 ;;
        *) die "unsupported insights argument: $1" ;;
      esac
    done
    ;;
  website)
    path="/reporting/funnel/website-analytics"
    while [[ "$#" -gt 0 ]]; do
      case "$1" in
        --months)
          [[ "$#" -ge 2 ]] || die "--months requires a value"
          positive_int "$2" 24 "months"
          add_query months "$2"
          shift 2 ;;
        *) die "unsupported website argument: $1" ;;
      esac
    done
    ;;
  accounts)
    path="/reporting/funnel/accounts"
    while [[ "$#" -gt 0 ]]; do
      case "$1" in
        --stage)
          [[ "$#" -ge 2 ]] || die "--stage requires a value"
          safe_key "$2" "stage"
          add_query stage "$2"
          shift 2 ;;
        --grade)
          [[ "$#" -ge 2 ]] || die "--grade requires a value"
          [[ "$2" =~ ^[A-F]$ ]] || die "grade must be A through F"
          add_query grade "$2"
          shift 2 ;;
        --flow)
          [[ "$#" -ge 2 ]] || die "--flow requires a value"
          [[ "$2" == "self_signup" || "$2" == "demo" ]] || die "flow must be self_signup or demo"
          add_query flow "$2"
          shift 2 ;;
        --stalled)
          [[ "$#" -ge 2 && "$2" == "true" ]] || die "--stalled only accepts true"
          add_query stalled true
          shift 2 ;;
        --page)
          [[ "$#" -ge 2 ]] || die "--page requires a value"
          positive_int "$2" 10000 "page"
          add_query page "$2"
          shift 2 ;;
        --per-page)
          [[ "$#" -ge 2 ]] || die "--per-page requires a value"
          positive_int "$2" 50 "per-page"
          add_query perPage "$2"
          shift 2 ;;
        *) die "unsupported accounts argument: $1" ;;
      esac
    done
    ;;
  account)
    [[ "$#" -eq 1 ]] || die "account requires exactly one account ID"
    account_id "$1" "account"
    path="/reporting/funnel/accounts/$1"
    ;;
  events)
    path="/reporting/funnel/events"
    while [[ "$#" -gt 0 ]]; do
      case "$1" in
        --from|--to)
          [[ "$#" -ge 2 ]] || die "$1 requires a value"
          rfc3339 "$2" "${1#--}"
          add_query "${1#--}" "$2"
          shift 2 ;;
        --type)
          [[ "$#" -ge 2 ]] || die "--type requires a value"
          safe_key "$2" "type"
          add_query type "$2"
          shift 2 ;;
        --source)
          [[ "$#" -ge 2 ]] || die "--source requires a value"
          [[ "$2" == "sitelog" || "$2" == "applog" ]] || die "source must be sitelog or applog"
          add_query source "$2"
          shift 2 ;;
        --account)
          [[ "$#" -ge 2 ]] || die "--account requires a value"
          account_id "$2" "account"
          add_query account "$2"
          shift 2 ;;
        --segment)
          [[ "$#" -ge 2 ]] || die "--segment requires a value"
          [[ "$2" == "humans" || "$2" == "bots" || "$2" == "all" ]] || die "segment must be humans, bots, or all"
          add_query segment "$2"
          shift 2 ;;
        --page)
          [[ "$#" -ge 2 ]] || die "--page requires a value"
          positive_int "$2" 10000 "page"
          add_query page "$2"
          shift 2 ;;
        --per-page)
          [[ "$#" -ge 2 ]] || die "--per-page requires a value"
          positive_int "$2" 100 "per-page"
          add_query perPage "$2"
          shift 2 ;;
        *) die "unsupported events argument: $1" ;;
      esac
    done
    ;;
  site-events)
    path="/analytics/site"
    site_events=true
    while [[ "$#" -gt 0 ]]; do
      case "$1" in
        --from|--to)
          [[ "$#" -ge 2 ]] || die "$1 requires a value"
          rfc3339 "$2" "${1#--}"
          add_query "${1#--}" "$2"
          shift 2 ;;
        --type)
          [[ "$#" -ge 2 ]] || die "--type requires a value"
          safe_key_csv "$2" "type"
          add_query type "$2"
          shift 2 ;;
        --path)
          [[ "$#" -ge 2 ]] || die "--path requires a value"
          safe_site_path "$2" "path"
          add_query path "$2"
          shift 2 ;;
        --exclude-bots)
          [[ "$#" -ge 2 && "$2" == "true" ]] || die "--exclude-bots only accepts true"
          [[ -z "${site_events_bot_filter}" ]] || die "choose only one raw-event bot filter"
          site_events_bot_filter="exclude"
          add_query exclude_bots true
          shift 2 ;;
        --bots-only)
          [[ "$#" -ge 2 && "$2" == "true" ]] || die "--bots-only only accepts true"
          [[ -z "${site_events_bot_filter}" ]] || die "choose only one raw-event bot filter"
          site_events_bot_filter="only"
          add_query is_bot true
          shift 2 ;;
        --page)
          [[ "$#" -ge 2 ]] || die "--page requires a value"
          positive_int "$2" 10000 "page"
          site_events_page="$2"
          add_query page "$2"
          shift 2 ;;
        --per-page)
          [[ "$#" -ge 2 ]] || die "--per-page requires a value"
          positive_int "$2" 100 "per-page"
          site_events_per_page="$2"
          add_query per_page "$2"
          shift 2 ;;
        *) die "unsupported site-events argument: $1" ;;
      esac
    done
    [[ -n "${site_events_bot_filter}" ]] || die "site-events requires --exclude-bots true or --bots-only true"
    ;;
  *) die "unsupported Analytics operation: ${action}" ;;
esac

endpoint="${base_url}${path}"
if [[ -n "${query}" ]]; then
  endpoint+="?${query}"
fi

curl_args=(
  --config "${auth_config}"
  --silent
  --show-error
  --proto '=https'
  --tlsv1.2
  --connect-timeout 10
  --max-time 75
  --request GET
  --header 'Accept: application/json'
  --output "${response_file}"
  --write-out '%{http_code}'
  --url "${endpoint}"
)
if [[ "${site_events}" == true ]]; then
  curl_args+=(--dump-header "${header_file}")
fi

status="$(curl "${curl_args[@]}")" || die "request failed"
[[ "${status}" == "200" ]] || die "request failed with HTTP ${status}"
jq -e . "${response_file}" >/dev/null 2>&1 || die "Gateway returned invalid JSON"

if [[ "${site_events}" == true ]] && ! jq -e 'type == "array"' "${response_file}" >/dev/null 2>&1; then
  die "Gateway returned an invalid raw site-event response"
fi

if [[ "${action}" == "account" ]] && jq -e '.account.internal == true' "${response_file}" >/dev/null 2>&1; then
  die "internal accounts are not available through this capability"
fi

jq_filter='
  walk(
    if type == "object" then
      del(
        .email,
        .ip,
        .visitor_token,
        .session_id,
        .event_id,
        .query_params,
        .click_ids,
        .props,
        .heard_about_us,
        .demo_code,
        .demo_owner,
        .token,
        .code,
        .raw
      )
      | if (.url? | type) == "string" then .url |= sub("[?#].*$"; "") else . end
      | if (.referrer? | type) == "string" then .referrer |= sub("[?#].*$"; "") else . end
    else . end
  )
  | if type == "object" then del(.self_reported_attribution) else . end
'

if [[ "${site_events}" == true ]]; then
  total=""
  while IFS=: read -r header value; do
    if [[ "${header,,}" == "total-records-count" ]]; then
      total="${value//$'\r'/}"
      total="${total//[[:space:]]/}"
    fi
  done <"${header_file}"
  [[ "${total}" =~ ^[0-9]+$ ]] || die "Gateway omitted a valid Total-Records-Count header"
  jq --argjson total "${total}" --argjson page "${site_events_page}" \
    --argjson per_page "${site_events_per_page}" \
    "${jq_filter} | {total: \$total, page: \$page, per_page: \$per_page, events: .}" \
    "${response_file}"
else
  jq "${jq_filter}" "${response_file}"
fi
