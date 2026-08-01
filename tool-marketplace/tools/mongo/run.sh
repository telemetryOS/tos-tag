#!/bin/sh
set -eu
[ "${TOS_TAG_OPERATION_ID:-}" = read ] || { echo "mongo: only the read operation is supported" >&2; exit 2; }
case "${1:-}" in connect|disconnect) echo "mongo: session mutation is not available to the agent" >&2; exit 2 ;; esac
for argument in "$@"; do
  case "$argument" in
    --uri|--uri=*|--mongosh-host|--mongosh-host=*|--proxy-jump|--proxy-jump=*)
      echo "mongo: credential and SSH endpoint overrides are not permitted" >&2
      exit 2 ;;
  esac
done
command -v mongo-fetch >/dev/null 2>&1 || { echo "mongo-fetch is not installed" >&2; exit 2; }
exec mongo-fetch "$@"
