#!/usr/bin/env bash
set -euo pipefail

die() {
  printf 'digitalocean: %s\n' "$1" >&2
  exit 2
}

reject() {
  printf 'digitalocean: %s\n' "$1" >&2
  exit 3
}

command -v jq >/dev/null 2>&1 || die "jq is not installed"

operation="${TOS_TAG_OPERATION_ID:-}"
command_name="${1:-}"
[[ -n "${command_name}" ]] || die "a reviewed command is required"
shift

doctl_args=()
expected_arguments=0
case "${operation}:${command_name}" in
  read:account) doctl_args=(account get) ;;
  read:rate-limit) doctl_args=(account ratelimit) ;;
  read:balance) doctl_args=(balance get) ;;
  read:billing-history) doctl_args=(billing-history list) ;;
  read:invoices) doctl_args=(invoice list) ;;
  read:invoice) doctl_args=(invoice get); expected_arguments=1 ;;
  read:invoice-summary) doctl_args=(invoice summary); expected_arguments=1 ;;
  read:droplets) doctl_args=(compute droplet list) ;;
  read:droplet) doctl_args=(compute droplet get); expected_arguments=1 ;;
  read:regions) doctl_args=(compute region list) ;;
  read:sizes) doctl_args=(compute size list) ;;
  read:images) doctl_args=(compute image list) ;;
  read:image) doctl_args=(compute image get); expected_arguments=1 ;;
  read:snapshots) doctl_args=(compute snapshot list) ;;
  read:snapshot) doctl_args=(compute snapshot get); expected_arguments=1 ;;
  read:volumes) doctl_args=(compute volume list) ;;
  read:volume) doctl_args=(compute volume get); expected_arguments=1 ;;
  read:ssh-keys) doctl_args=(compute ssh-key list) ;;
  read:ssh-key) doctl_args=(compute ssh-key get); expected_arguments=1 ;;
  read:firewalls) doctl_args=(compute firewall list) ;;
  read:firewall) doctl_args=(compute firewall get); expected_arguments=1 ;;
  read:load-balancers) doctl_args=(compute load-balancer list) ;;
  read:load-balancer) doctl_args=(compute load-balancer get); expected_arguments=1 ;;
  read:domains) doctl_args=(compute domain list) ;;
  read:domain) doctl_args=(compute domain get); expected_arguments=1 ;;
  read:domain-records) doctl_args=(compute domain records list); expected_arguments=1 ;;
  read:domain-record) doctl_args=(compute domain records get); expected_arguments=2 ;;
  read:clusters) doctl_args=(kubernetes cluster list) ;;
  read:cluster) doctl_args=(kubernetes cluster get); expected_arguments=1 ;;
  read:cluster-upgrades) doctl_args=(kubernetes cluster get-upgrades); expected_arguments=1 ;;
  read:cluster-resources) doctl_args=(kubernetes cluster list-associated-resources); expected_arguments=1 ;;
  read:projects) doctl_args=(projects list) ;;
  read:project) doctl_args=(projects get); expected_arguments=1 ;;
  read:project-resources) doctl_args=(projects resources list); expected_arguments=1 ;;
  read:vpcs) doctl_args=(vpcs list) ;;
  read:vpc) doctl_args=(vpcs get); expected_arguments=1 ;;
  write:reboot-droplet) doctl_args=(compute droplet-action reboot); expected_arguments=1 ;;
  write:power-cycle-droplet) doctl_args=(compute droplet-action power-cycle); expected_arguments=1 ;;
  write:power-off-droplet) doctl_args=(compute droplet-action power-off); expected_arguments=1 ;;
  write:power-on-droplet) doctl_args=(compute droplet-action power-on); expected_arguments=1 ;;
  write:shutdown-droplet) doctl_args=(compute droplet-action shutdown); expected_arguments=1 ;;
  write:restart-app) doctl_args=(apps restart); expected_arguments=1 ;;
  delete:delete-droplet) doctl_args=(compute droplet delete); expected_arguments=1 ;;
  delete:delete-app) doctl_args=(apps delete); expected_arguments=1 ;;
  delete:delete-database) doctl_args=(databases delete); expected_arguments=1 ;;
  delete:delete-cluster) doctl_args=(kubernetes cluster delete); expected_arguments=1 ;;
  delete:delete-project) doctl_args=(projects delete); expected_arguments=1 ;;
  delete:delete-vpc) doctl_args=(vpcs delete); expected_arguments=1 ;;
  delete:delete-firewall) doctl_args=(compute firewall delete); expected_arguments=1 ;;
  delete:delete-load-balancer) doctl_args=(compute load-balancer delete); expected_arguments=1 ;;
  delete:delete-domain) doctl_args=(compute domain delete); expected_arguments=1 ;;
  *) die "command '${command_name}' is not permitted by operation '${operation}'" ;;
esac

(( $# == expected_arguments )) || die "command '${command_name}' requires exactly ${expected_arguments} resource argument(s)"
for value in "$@"; do
  (( ${#value} >= 1 && ${#value} <= 255 )) || die "resource argument has unsupported length"
  [[ "${value}" != *$'\n'* && "${value}" != *$'\r'* && "${value}" != -* && "${value}" != *'..'* ]] || die "resource argument is malformed"
  [[ "${value}" =~ ^[A-Za-z0-9][A-Za-z0-9._:-]{0,254}$ ]] || die "resource argument has unsupported characters"
  doctl_args+=("${value}")
done

case "${operation}:${command_name}" in
  write:reboot-droplet|write:power-cycle-droplet|write:power-off-droplet|write:power-on-droplet|write:shutdown-droplet|write:restart-app)
    doctl_args+=(--wait)
    ;;
  delete:delete-cluster)
    doctl_args+=(--force --update-kubeconfig=false)
    ;;
  delete:*)
    doctl_args+=(--force)
    ;;
esac

api_key="${DIGITAL_OCEAN_API_KEY:-}"
(( ${#api_key} >= 20 && ${#api_key} <= 4096 )) || die "DIGITAL_OCEAN_API_KEY is missing or malformed"
[[ "${api_key}" != *$'\n'* && "${api_key}" != *$'\r'* && "${api_key}" =~ ^[-A-Za-z0-9._~]+$ ]] || die "DIGITAL_OCEAN_API_KEY is malformed"

doctl_binary="$(command -v doctl 2>/dev/null || true)"
[[ -n "${doctl_binary}" && -x "${doctl_binary}" ]] || die "official doctl CLI is not installed"

state_dir="$(mktemp -d "${TMPDIR:-/tmp}/tos-tag-digitalocean-home.XXXXXX")"
response_file="$(mktemp "${TMPDIR:-/tmp}/tos-tag-digitalocean-response.XXXXXX")"
error_file="$(mktemp "${TMPDIR:-/tmp}/tos-tag-digitalocean-error.XXXXXX")"
cleanup() {
  rm -rf "${state_dir}"
  rm -f "${response_file}" "${error_file}"
}
trap cleanup EXIT
chmod 0700 "${state_dir}"
chmod 0600 "${response_file}" "${error_file}"
mkdir -p "${state_dir}/config"
chmod 0700 "${state_dir}/config"

set +e
HOME="${state_dir}" XDG_CONFIG_HOME="${state_dir}/config" DIGITALOCEAN_ACCESS_TOKEN="${api_key}" \
  "${doctl_binary}" --output json --http-retry-max 2 "${doctl_args[@]}" >"${response_file}" 2>"${error_file}"
status=$?
set -e
unset api_key DIGITAL_OCEAN_API_KEY DIGITALOCEAN_ACCESS_TOKEN

if (( status != 0 )); then
  reject "CLI request failed with exit status ${status}"
fi

if [[ ! -s "${response_file}" ]]; then
  jq -n --arg command "${command_name}" '{ok:true,command:$command}'
  exit 0
fi
jq -e . "${response_file}" >/dev/null 2>&1 || die "doctl returned non-JSON output"
jq . "${response_file}"
