#!/usr/bin/env bash
set -euo pipefail

die() {
  printf 'product-knowledge: %s\n' "$*" >&2
  exit 1
}

[[ "${TOS_TAG_OPERATION_ID:-}" == "read" ]] || die "only the read operation is permitted"
command -v curl >/dev/null 2>&1 || die "curl is required"

fetch() {
  local url="$1"
  curl --fail --silent --show-error --max-time 20 --proto '=https' "$url"
}

verb="${1:-}"
shift || true
case "$verb" in
  docs-index)
    [[ "$#" -eq 0 ]] || die "docs-index accepts no arguments"
    fetch "https://docs.telemetryos.com/llms.txt"
    ;;
  docs-page)
    [[ "$#" -eq 1 ]] || die "docs-page requires exactly one path or docs.telemetryos.com URL"
    ref="$1"
    case "$ref" in
      https://docs.telemetryos.com/*) path="${ref#https://docs.telemetryos.com/}" ;;
      *://*) die "only docs.telemetryos.com URLs are permitted" ;;
      *) path="${ref#/}" ;;
    esac
    [[ "${#path}" -le 240 ]] || die "documentation path is too long"
    case "$path" in
      *..*|*//*|*\\*|*\?*|*\#*) die "invalid documentation path" ;;
    esac
    [[ "$path" =~ ^(docs|reference)/[A-Za-z0-9._/-]+\.md$ ]] || die "path must be a Markdown page from the documentation index"
    fetch "https://docs.telemetryos.com/${path}"
    ;;
  corporate-full)
    [[ "$#" -eq 0 ]] || die "corporate-full accepts no arguments"
    fetch "https://www.telemetryos.com/llms-full.txt"
    ;;
  *)
    die "supported verbs: docs-index, docs-page, corporate-full"
    ;;
esac
