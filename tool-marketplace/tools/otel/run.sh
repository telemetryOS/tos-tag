#!/bin/sh
set -eu
[ "${TOS_TAG_OPERATION_ID:-}" = read ] || { echo "otel: only the read operation is supported" >&2; exit 2; }
for argument in "$@"; do
  case "$argument" in
    --api-key|--api-key=*|--bearer-token|--bearer-token=*|--header|--header=*|--url|--url=*)
      echo "otel: credential and endpoint overrides are not permitted" >&2
      exit 2 ;;
  esac
done
command -v otel-fetch >/dev/null 2>&1 || { echo "otel-fetch is not installed" >&2; exit 2; }
exec otel-fetch "$@"
