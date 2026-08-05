#!/usr/bin/env bash
set -euo pipefail

die() {
  printf 'curds: %s\n' "$*" >&2
  exit 2
}

[[ "${TOS_TAG_OPERATION_ID:-}" == "generate" ]] || die "only the generate operation is permitted"
[[ -n "${TOS_TAG_ARTIFACT_PATH:-}" ]] || die "the server-owned artifact path is required"
command -v curds >/dev/null 2>&1 || die "curds is not installed"

[[ "$#" -eq 3 ]] || die "generate requires prompt, aspect ratio, and quality"
prompt="$1"
aspect_ratio="$2"
quality="$3"

[[ -n "${prompt//[[:space:]]/}" && "${#prompt}" -le 12000 ]] || die "prompt must contain 1-12000 characters"
case "$aspect_ratio" in
  1:1|3:2|2:3|4:3|3:4|16:9|9:16|21:9|9:21|2:1|1:2) ;;
  *) die "unsupported aspect ratio" ;;
esac
case "$quality" in
  low|medium|high|auto) ;;
  *) die "quality must be low, medium, high, or auto" ;;
esac

exec curds \
  -no-tui \
  -inline off \
  -provider openai \
  -number-of-images 1 \
  -output-format webp \
  -timeout 4m30s \
  -aspect-ratio "$aspect_ratio" \
  -quality "$quality" \
  -prompt "$prompt" \
  -output "$TOS_TAG_ARTIFACT_PATH"
