#!/usr/bin/env bash
set -euo pipefail

die() {
  printf 'stripe: %s\n' "$1" >&2
  exit 2
}

reject() {
  printf 'stripe: %s\n' "$1" >&2
  exit 3
}

command -v jq >/dev/null 2>&1 || die "jq is not installed"

operation="${TOS_TAG_OPERATION_ID:-}"
command_name="${1:-}"
path="${2:-}"
[[ -n "${command_name}" && -n "${path}" ]] || die "usage: stripe.sh get|post|delete <documented-/v1-or-/v2-path> [reviewed options]"
shift 2

case "${operation}:${command_name}" in
  read:get|write:post|delete:delete) ;;
  *) die "command '${command_name}' is not permitted by operation '${operation}'" ;;
esac

(( ${#path} <= 512 )) || die "path is too long"
[[ "${path}" != *'?'* && "${path}" != *'#'* && "${path}" != *'..'* ]] || die "path must not contain query, fragment, or traversal"
[[ "${path}" =~ ^/v(1|2)/[A-Za-z0-9][-A-Za-z0-9._~/]{0,510}$ ]] || die "path must be a documented /v1 or /v2 API path"

stripe_args=("${command_name}" "${path}" "--live" "--color" "off")
data_count=0
expand_count=0
idempotency_set=false
pagination_set=false
while (( $# > 0 )); do
  case "$1" in
    --data)
      (( $# >= 2 )) || die "--data requires form-field=value"
      (( data_count < 64 )) || die "too many --data fields"
      (( ${#2} <= 4096 )) || die "--data field is too large"
      [[ "$2" != *$'\n'* && "$2" != *$'\r'* && "$2" == *=* ]] || die "--data must be a single-line form-field=value"
      data_key="${2%%=*}"
      data_key_pattern='^[A-Za-z][A-Za-z0-9_.-]*(\[[A-Za-z0-9_.-]*\])*$'
      [[ "${data_key}" =~ ${data_key_pattern} ]] || die "--data has an unsupported field name"
      stripe_args+=("--data" "$2")
      data_count=$((data_count + 1))
      shift 2
      ;;
    --expand)
      (( $# >= 2 )) || die "--expand requires a field"
      (( expand_count < 16 )) || die "too many --expand fields"
      [[ "$2" =~ ^[A-Za-z][A-Za-z0-9_.]{0,255}$ ]] || die "--expand has an unsupported field"
      stripe_args+=("--expand" "$2")
      expand_count=$((expand_count + 1))
      shift 2
      ;;
    --limit)
      [[ "${operation}" == read ]] || die "--limit is read-only"
      (( $# >= 2 )) || die "--limit requires an integer"
      [[ "$2" =~ ^[0-9]+$ ]] && (( 10#$2 >= 1 && 10#$2 <= 100 )) || die "--limit must be between 1 and 100"
      stripe_args+=("--limit" "$2")
      shift 2
      ;;
    --starting-after|--ending-before)
      [[ "${operation}" == read ]] || die "$1 is read-only"
      [[ "${pagination_set}" == false ]] || die "only one cursor option is permitted"
      (( $# >= 2 )) || die "$1 requires a resource ID"
      [[ "$2" =~ ^[A-Za-z0-9][A-Za-z0-9_-]{0,254}$ ]] || die "$1 has an unsupported resource ID"
      stripe_args+=("$1" "$2")
      pagination_set=true
      shift 2
      ;;
    --stripe-account)
      (( $# >= 2 )) || die "--stripe-account requires an account ID"
      [[ "$2" =~ ^acct_[A-Za-z0-9]{4,255}$ ]] || die "--stripe-account has an unsupported account ID"
      stripe_args+=("--stripe-account" "$2")
      shift 2
      ;;
    --stripe-context)
      (( $# >= 2 )) || die "--stripe-context requires a context ID"
      [[ "$2" =~ ^[A-Za-z0-9][A-Za-z0-9_./:-]{0,254}$ ]] || die "--stripe-context has an unsupported context ID"
      stripe_args+=("--stripe-context" "$2")
      shift 2
      ;;
    --stripe-version)
      (( $# >= 2 )) || die "--stripe-version requires a version"
      [[ "$2" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}(\.[A-Za-z0-9_-]+)?$ ]] || die "--stripe-version has an unsupported version"
      stripe_args+=("--stripe-version" "$2")
      shift 2
      ;;
    --idempotency)
      [[ "${operation}" == write || "${operation}" == delete ]] || die "--idempotency is mutation-only"
      [[ "${idempotency_set}" == false ]] || die "--idempotency may be supplied only once"
      (( $# >= 2 )) || die "--idempotency requires a key"
      (( ${#2} >= 8 && ${#2} <= 255 )) || die "--idempotency key must be 8 to 255 characters"
      [[ "$2" =~ ^[A-Za-z0-9][A-Za-z0-9._:-]+$ ]] || die "--idempotency key has unsupported characters"
      stripe_args+=("--idempotency" "$2")
      idempotency_set=true
      shift 2
      ;;
    *) die "unsupported argument: $1" ;;
  esac
done

if [[ "${operation}" == write || "${operation}" == delete ]]; then
  [[ "${idempotency_set}" == true ]] || die "${command_name} requires --idempotency"
  stripe_args+=("--confirm")
fi

api_key="${STRIPE_API_KEY:-}"
(( ${#api_key} >= 16 && ${#api_key} <= 4096 )) || die "STRIPE_API_KEY is missing or malformed"
[[ "${api_key}" =~ ^[-A-Za-z0-9._~]+$ ]] || die "STRIPE_API_KEY is malformed"
case "${api_key}" in
  sk_live_*|rk_live_*) ;;
  *) die "STRIPE_API_KEY must be a live-mode secret or restricted key" ;;
esac

stripe_binary="$(command -v stripe 2>/dev/null || true)"
[[ -n "${stripe_binary}" && -x "${stripe_binary}" ]] || die "official Stripe CLI is not installed"

state_dir="$(mktemp -d "${TMPDIR:-/tmp}/tos-tag-stripe-home.XXXXXX")"
response_file="$(mktemp "${TMPDIR:-/tmp}/tos-tag-stripe-response.XXXXXX")"
error_file="$(mktemp "${TMPDIR:-/tmp}/tos-tag-stripe-error.XXXXXX")"
cleanup() {
  rm -rf "${state_dir}"
  rm -f "${response_file}" "${error_file}"
}
trap cleanup EXIT
chmod 0700 "${state_dir}"
chmod 0600 "${response_file}" "${error_file}"

set +e
HOME="${state_dir}" STRIPE_API_KEY="${api_key}" "${stripe_binary}" "${stripe_args[@]}" >"${response_file}" 2>"${error_file}"
status=$?
set -e
unset api_key STRIPE_API_KEY

if (( status != 0 )); then
  reject "CLI request failed with exit status ${status}"
fi

jq -e . "${response_file}" >/dev/null 2>&1 || die "Stripe CLI returned non-JSON output"
if jq -e 'type == "object" and (.error | type == "object")' "${response_file}" >/dev/null 2>&1; then
  summary="$(jq -c '{type:(.error.type // null),code:(.error.code // null)}' "${response_file}")"
  reject "API rejected: ${summary}"
fi
jq . "${response_file}"
