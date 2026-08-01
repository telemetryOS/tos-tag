#!/bin/sh
set -eu
command -v dla >/dev/null 2>&1 || { echo "device-logs: dla is not installed" >&2; exit 2; }
for argument in "$@"; do
  case "$argument" in
    --api-key|--api-key=*|--api-base-url|--api-base-url=*|--env-file|--env-file=*|--env|--env=*)
      echo "device-logs: credential, endpoint, and environment overrides are not permitted" >&2
      exit 2 ;;
  esac
done
case "${TOS_TAG_OPERATION_ID:-}" in
  read)
    for argument in "$@"; do
      case "$argument" in
        device-log-level|codex|claude|--codex|--claude|--autofix|--track-issues|--track-issues=*)
          echo "device-logs: '$argument' is not permitted by the read operation" >&2
          exit 2 ;;
      esac
    done ;;
  write)
    [ "${1:-}" = device-log-level ] || { echo "device-logs: write permits only device-log-level" >&2; exit 2; } ;;
  *) echo "device-logs: TOS_TAG_OPERATION_ID must be read or write" >&2; exit 2 ;;
esac
exec dla "$@"
