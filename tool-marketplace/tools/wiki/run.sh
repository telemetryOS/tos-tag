#!/usr/bin/env bash
#
# wiki — CLI for the TelemetryOS Agent Wiki (DESIGN.md §9).
#
# A single-file client of the wiki HTTP API (DESIGN §5). Dependencies are
# bash + curl + jq only — no install step beyond putting this file on PATH.
# Written for bash 3.2 (the macOS system bash) compatibility.
#
# Configuration (in precedence order — env wins, per DESIGN §8/§9.1):
#   1. WIKI_URL / WIKI_TOKEN environment variables.
#   2. ~/.config/telemetryos/wiki/config  (KEY=VALUE lines; also accepts the
#      TOML-ish `url = "..."` / `token = "..."` shapes — quotes/space stripped).
#   Override the config path with WIKI_CONFIG. Optional WIKI_AUTHOR sets the
#   X-Wiki-Author header (DESIGN §7) refining attribution.
#
# Reads are unauthenticated (DESIGN §7); the bearer token is sent only when
# present and is required for every write verb.
#
set -uo pipefail

PROG="wiki"

# ---------------------------------------------------------------------------
# small helpers
# ---------------------------------------------------------------------------

_die() { printf '%s: %s\n' "$PROG" "$*" >&2; exit 1; }
_lc()  { printf '%s' "$1" | tr '[:upper:]' '[:lower:]'; }
_uc()  { printf '%s' "$1" | tr '[:lower:]' '[:upper:]'; }

# Resolve the value for a value-taking flag. Supports the normal `--flag VALUE`
# form and, as an escape hatch for legitimately dash-leading values, the
# `--flag=VALUE` form. Errors via _die when no value follows OR when the next
# token is itself a flag (so `--title --md` fails loudly instead of titling the
# page "--md"). Results land in globals VAL (the value) and SHIFT (argv slots
# consumed: 1 for the =form, 2 for the spaced form).
#
# set -u SAFETY: callers pass the following token as "${2-}", so a missing value
# arrives here as "" — a bare, unbound $2 is never referenced (which would crash
# under `set -u` and leak the path). Argc is checked before the value is used.
#
#   usage:  --title|--title=*) _need_val "--title" "$1" "$#" "${2-}"
#                              title="$VAL"; shift "$SHIFT" ;;
VAL=""
SHIFT=1
_need_val() {
  local flag="$1" cur="$2" argc="$3" next="${4-}"
  case "$cur" in
    "$flag"=*) VAL="${cur#*=}"; SHIFT=1; return 0 ;;
  esac
  [ "$argc" -ge 2 ] || _die "$flag requires a value"
  case "$next" in
    -?*) _die "$flag requires a value (got '$next'; use $flag=VALUE to pass a dash-leading value)" ;;
  esac
  VAL="$next"; SHIFT=2
}

# Pretty-print a TSV stream as a table when `column` is available; otherwise
# pass it through unchanged.
_table() {
  if command -v column >/dev/null 2>&1; then
    column -t -s "$(printf '\t')"
  else
    cat
  fi
}

# Content-Type from a filename extension (DESIGN §4.4 accepted asset types).
_content_type() {
  case "$(_lc "${1##*.}")" in
    png)      echo image/png ;;
    jpg|jpeg) echo image/jpeg ;;
    webp)     echo image/webp ;;
    gif)      echo image/gif ;;
    svg)      echo image/svg+xml ;;
    mp4)      echo video/mp4 ;;
    webm)     echo video/webm ;;
    *)        echo application/octet-stream ;;
  esac
}

# ---------------------------------------------------------------------------
# configuration
# ---------------------------------------------------------------------------

_load_config() {
  local f="${WIKI_CONFIG:-$HOME/.config/telemetryos/wiki/config}"
  [ -f "$f" ] || return 0
  local line k v
  while IFS= read -r line || [ -n "$line" ]; do
    line="${line%%#*}"                       # strip trailing comment
    case "$line" in *=*) : ;; *) continue ;; esac
    k="${line%%=*}"; v="${line#*=}"
    k="$(printf '%s' "$k" | tr -d '[:space:]')"
    v="${v#"${v%%[![:space:]]*}"}"           # ltrim
    v="${v%"${v##*[![:space:]]}"}"           # rtrim
    v="${v%\"}"; v="${v#\"}"                  # strip double quotes
    v="${v%\'}"; v="${v#\'}"                  # strip single quotes
    case "$k" in
      url|URL|WIKI_URL)         [ -z "${WIKI_URL:-}" ]    && WIKI_URL="$v" ;;
      token|TOKEN|WIKI_TOKEN)   [ -z "${WIKI_TOKEN:-}" ]  && WIKI_TOKEN="$v" ;;
      author|WIKI_AUTHOR)       [ -z "${WIKI_AUTHOR:-}" ] && WIKI_AUTHOR="$v" ;;
    esac
  done < "$f"
}

_need_url() {
  [ -n "${WIKI_URL:-}" ] || _die "WIKI_URL not set (env or ~/.config/telemetryos/wiki/config)"
}
_need_token() {
  [ -n "${WIKI_TOKEN:-}" ] || _die "WIKI_TOKEN required for this write (env or config)"
}

# Human page URL. Pages are addressed by opaque id, not slug (/pages/{id}), so
# an un-indexed namespace's pages are reachable only by their id. Pass the API
# response JSON; the server returns the page id (and a ready-made .url).
_human_url() {
  local resp="$1" id
  id="$(printf '%s' "$resp" | jq -r '.id // empty' 2>/dev/null)"
  if [ -n "$id" ]; then printf '%s/pages/%s' "${WIKI_URL%/}" "$id"; else
    # Fallback: server too old to return an id — echo whatever .url it gave.
    printf '%s' "$resp" | jq -r '.url // empty' 2>/dev/null
  fi
}

# Normalize a page read to include the canonical human URL alongside the full
# page JSON. The id comes from the reviewed API response; workers never infer or
# reconstruct opaque page URLs themselves.
_read_json() {
  local resp="$1" human_url
  human_url="$(_human_url "$resp")"
  printf '%s' "$resp" | jq --arg url "$human_url" '
    . + {url: (if ((.url // "") | length) > 0 then .url else $url end)}'
}

# ---------------------------------------------------------------------------
# HTTP core
# ---------------------------------------------------------------------------

# _api METHOD PATH [extra curl args...]
#   Issues an authenticated request against WIKI_URL. On success prints the
#   response body to stdout and returns 0. On any HTTP >=400 it prints the
#   server's {error,detail} (or the raw body) to stderr and returns 1.
#   A `warnings` array in the response body is always echoed to stderr so the
#   agent sees contract violations (DESIGN §5.1/§9) in the same turn.
_api() {
  _need_url
  local method="$1" path="$2"; shift 2
  local url="${WIKI_URL%/}$path"
  local hdr=()
  [ -n "${WIKI_TOKEN:-}" ]  && hdr+=(-H "Authorization: Bearer $WIKI_TOKEN")
  [ -n "${WIKI_AUTHOR:-}" ] && hdr+=(-H "X-Wiki-Author: $WIKI_AUTHOR")

  local tmp code
  tmp="$(mktemp)" || _die "mktemp failed"
  code="$(curl -sS --compressed -X "$method" \
      "${hdr[@]+"${hdr[@]}"}" \
      "$@" \
      -o "$tmp" -w '%{http_code}' \
      "$url")" || { rm -f "$tmp"; _die "request failed: $method $url"; }

  local body; body="$(cat "$tmp")"; rm -f "$tmp"

  # Surface server warnings (best effort — body may not be JSON).
  if printf '%s' "$body" | jq -e 'type=="object" and (.warnings|type=="array") and (.warnings|length>0)' >/dev/null 2>&1; then
    printf '%s' "$body" | jq -r '.warnings[] | "warning: " + (if type=="string" then . else tojson end)' >&2
  fi

  if [ "$code" -ge 400 ]; then
    local err detail
    err="$(printf '%s' "$body" | jq -r 'if type=="object" then (.error // empty) else empty end' 2>/dev/null)"
    detail="$(printf '%s' "$body" | jq -r 'if type=="object" then (.detail // empty) else empty end' 2>/dev/null)"
    if [ -n "$err" ]; then
      printf '%s: error: %s (HTTP %s)\n' "$PROG" "$err" "$code" >&2
      [ -n "$detail" ] && printf '%s: detail: %s\n' "$PROG" "$detail" >&2
    else
      printf '%s: error: HTTP %s\n%s\n' "$PROG" "$code" "$body" >&2
    fi
    # Distinguish the two auth failures — the bare "forbidden" is confusing.
    # _api is shared by reads and writes; when auth is enabled every /api/*
    # request needs a bearer, and a 403 can also deny a read via namespace View.
    case "$code" in
      401) printf '%s: hint: not authenticated — set WIKI_TOKEN to a valid /account API token when authentication is enabled.\n' "$PROG" >&2 ;;
      403) printf '%s: hint: the server-side Wiki account lacks permission for this page; an operator must manage that account outside tos-tag.\n' "$PROG" >&2 ;;
    esac
    return 1
  fi

  printf '%s' "$body"
}

# Write a JSON string field encoder helper for a request body to a temp file,
# returning the temp path on stdout. Callers rm the file. Using a file (not an
# argv string) keeps 4 MB bodies clear of ARG_MAX limits.
_json_body_file() {
  local tmp; tmp="$(mktemp)" || _die "mktemp failed"
  cat > "$tmp"
  printf '%s' "$tmp"
}

# ---------------------------------------------------------------------------
# ref parsing / namespace conventions
# ---------------------------------------------------------------------------

NS=""
SLUG=""
PAGE_ID=""

# Recognize the opaque human-link shape (/pages/{24-hex-id}) in a root-relative
# path or pasted URL. Query strings and fragments belong to the browser view and
# are intentionally ignored; CLI flags own revision/representation selection.
# Return 0 for a valid opaque ref, 1 for a normal namespace/slug ref, and 2 for
# a malformed opaque ref so callers never reinterpret /pages/not-an-id as the
# unrelated namespace/slug pair pages/not-an-id.
_page_id_from_ref() {
  local ref="$1" id path
  PAGE_ID=""
  ref="${ref%%#*}"
  ref="${ref%%\?*}"
  case "$ref" in
    *://*) path="${ref#*://}"; path="/${path#*/}" ;;
    /*) path="$ref" ;;
    *) return 1 ;;
  esac
  path="${path%/}"
  case "$path" in
    /pages/*) id="${path#/pages/}" ;;
    /pages) return 2 ;;
    *) return 1 ;;
  esac
  case "$id" in */*|"") return 2 ;; esac
  printf '%s' "$id" | grep -Eq '^[0-9a-fA-F]{24}$' || return 2
  PAGE_ID="$(_lc "$id")"
  return 0
}

# Uppercase the first path segment for the tickets namespace (eng-123 →
# ENG-123), per DESIGN §9.1 / §12.1. Other namespaces pass through unchanged.
_normalize_slug() {
  local ns="$1" slug="$2" first rest
  if [ "$(_lc "$ns")" = "tickets" ]; then
    first="${slug%%/*}"; rest=""
    case "$slug" in */*) rest="/${slug#*/}" ;; esac
    first="$(_uc "$first")"
    slug="$first$rest"
  fi
  printf '%s' "$slug"
}

# Split "<ns>/<slug>" into NS and SLUG (globals); normalizes the slug. Accepts a
# pasted namespaced URL (https://host/ns/slug) or a leading-slash ref
# (/ns/slug) by stripping scheme+host and any leading slash first. Public
# /pages/{id} links are handled separately by read verbs.
_split_ref() {
  local ref="$1"
  case "$ref" in *://*) ref="${ref#*://}"; ref="${ref#*/}" ;; esac  # strip scheme+host
  ref="${ref#/}"                                                    # strip leading slash
  case "$ref" in
    */*) NS="${ref%%/*}"; SLUG="${ref#*/}" ;;
    *)   _die "'$ref' is a namespace, not a page — try '$PROG tree $ref' or '$PROG ns get $ref'" ;;
  esac
  [ -n "$NS" ]   || _die "empty namespace in: $ref"
  [ -n "$SLUG" ] || _die "empty slug in: $ref"
  SLUG="$(_normalize_slug "$NS" "$SLUG")"
}

# jq expression comma-joining tags "a, b ,c" → JSON array, trimmed. This is a
# literal jq program, not a shell expansion.
# shellcheck disable=SC2016
_tags_expr='($tg|split(",")|map(gsub("^ +| +$";""))|map(select(length>0)))'

# ---------------------------------------------------------------------------
# read verbs
# ---------------------------------------------------------------------------

# Whole-wiki index — the site-root /llms.txt (every namespace + page), printed
# verbatim. NOT under /api/; _api prepends WIKI_URL so the site-root path works.
cmd_map() {
  case "${1:-}" in
    -h|--help) echo "usage: $PROG map    (aliases: overview, llms)"; return 0 ;;
    "") : ;;
    *) _die "map: unexpected arg: $1" ;;
  esac
  _api GET "/llms.txt" || return 1
}

cmd_ls() {
  local ns="" prefix="" tag="" limit=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --prefix|--prefix=*) _need_val "--prefix" "$1" "$#" "${2-}"; prefix="$VAL"; shift "$SHIFT" ;;
      --tag|--tag=*)       _need_val "--tag"    "$1" "$#" "${2-}"; tag="$VAL";    shift "$SHIFT" ;;
      --limit|--limit=*)   _need_val "--limit"  "$1" "$#" "${2-}"; limit="$VAL";  shift "$SHIFT" ;;
      -h|--help) echo "usage: $PROG ls <ns> [--prefix P] [--tag T] [--limit N]"; return 0 ;;
      -*) _die "ls: unknown flag: $1" ;;
      *) if [ -z "$ns" ]; then ns="$1"; else _die "ls: unexpected arg: $1"; fi; shift ;;
    esac
  done
  [ -n "$ns" ] || _die "usage: $PROG ls <ns> [--prefix P] [--tag T] [--limit N]"

  local base=""
  [ -n "$prefix" ] && base="$base&prefix=$prefix"
  [ -n "$tag" ]    && base="$base&tag=$tag"

  # By default auto-paginate the server's opaque cursor so `ls` returns the
  # COMPLETE list (a single response is capped at 200, default 50). An explicit
  # --limit is an intentional single-page cap: one request, no auto-paginate.
  local cursor="" sent="" out page_rows all_rows="" q
  while : ; do
    sent="$cursor"
    q="$base"
    [ -n "$limit" ]  && q="$q&limit=$limit"
    [ -n "$cursor" ] && q="$q&cursor=$(printf '%s' "$cursor" | jq -sRr @uri)"
    q="${q:+?${q#&}}"
    out="$(_api GET "/api/v1/pages/$ns$q")" || return 1
    page_rows="$(printf '%s' "$out" | jq -r '
      (if type=="array" then . else (.pages // .items // .results // .data // []) end)
      | .[] | [ (.slug // ""), (.title // ""), (.updated_at // .updated // "") ] | @tsv')"
    [ -n "$page_rows" ] && all_rows="${all_rows:+$all_rows
}$page_rows"
    [ -n "$limit" ] && break
    cursor="$(printf '%s' "$out" | jq -r '.cursor // empty')"
    [ -n "$cursor" ] || break
    # Safety: if the server returns a cursor identical to the one we just sent,
    # it isn't advancing — stop rather than loop forever.
    if [ "$cursor" = "$sent" ]; then
      printf '%s: warning: pagination cursor stopped advancing; results may be incomplete\n' "$PROG" >&2
      break
    fi
  done

  {
    printf 'SLUG\tTITLE\tUPDATED\n'
    [ -n "$all_rows" ] && printf '%s\n' "$all_rows"
  } | _table
}

cmd_tree() {
  local ns="" depth=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --depth|--depth=*) _need_val "--depth" "$1" "$#" "${2-}"; depth="$VAL"; shift "$SHIFT" ;;
      -h|--help) echo "usage: $PROG tree <ns> [--depth N]"; return 0 ;;
      -*) _die "tree: unknown flag: $1" ;;
      *) if [ -z "$ns" ]; then ns="$1"; else _die "tree: unexpected arg: $1"; fi; shift ;;
    esac
  done
  [ -n "$ns" ] || _die "usage: $PROG tree <ns> [--depth N]"

  local q=""; [ -n "$depth" ] && q="?depth=$depth"
  local out; out="$(_api GET "/api/v1/tree/$ns$q")" || return 1
  printf '%s' "$out" | jq -r '
    def render($d):
      ("  " * $d)
        + (.title // .name // .slug // "?")
        + (if (.has_page // .is_page // false) then "" else "/" end)
        + (if (.slug // null) != null then "  [" + .slug + "]" else "" end),
      ((.children // .nodes // [])[] | render($d + 1));
    (if type=="array" then . else (.children // .nodes // .tree // [.]) end) as $roots
    | $roots[] | render(0)'
}

cmd_get() {
  local ref="" as="html" rev=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --json) as="json"; shift ;;
      --rev|--rev=*) _need_val "--rev" "$1" "$#" "${2-}"; rev="$VAL"; shift "$SHIFT" ;;
      -h|--help) echo "usage: $PROG get <ns>/<slug>|<page-url> [--json] [--rev N]"; return 0 ;;
      -*) _die "get: unknown flag: $1" ;;
      *) if [ -z "$ref" ]; then ref="$1"; else _die "get: unexpected arg: $1"; fi; shift ;;
    esac
  done
  [ -n "$ref" ] || _die "usage: $PROG get <ns>/<slug>|<page-url> [--json] [--rev N]"
  if [ -n "$rev" ] && ! printf '%s' "$rev" | grep -Eq '^[1-9][0-9]*$'; then
    _die "get: --rev must be a positive integer"
  fi

  # The reviewed tos-tag gateway always returns the full page envelope for a
  # page read, even when a disposable worker omits --json. Besides the body,
  # that envelope carries the server-derived opaque human URL needed for a
  # useful Slack citation. Preserve the lean body-only default for direct CLI
  # use outside the gateway.
  local reviewed_read=0
  [ "${TOS_TAG_OPERATION_ID:-}" = "read" ] && reviewed_read=1

  # Human links are opaque /pages/{id} URLs. Resolve them through the bearer-
  # authenticated API instead of hitting the session-gated HTML route or
  # enumerating namespaces (which cannot rediscover un-indexed artifacts).
  if _page_id_from_ref "$ref"; then
    local idq=""
    [ -n "$rev" ] && idq="?rev=$rev"
    if [ "$as" = "json" ] || [ "$reviewed_read" -eq 1 ]; then
      local out; out="$(_api GET "/api/v1/page/$PAGE_ID$idq")" || return 1
      _read_json "$out"
    else
      if [ -n "$idq" ]; then idq="$idq&body-only"; else idq="?body-only"; fi
      _api GET "/api/v1/page/$PAGE_ID$idq"
    fi
    return $?
  elif [ "$?" -eq 2 ]; then
    _die "malformed opaque page URL: $ref"
  fi

  _split_ref "$ref"

  # Direct CLI default: lean body-only HTML. Reviewed gateway reads always use
  # the full JSON envelope so every page read has canonical-link provenance.
  # Storage is HTML-only; there is no Markdown source.
  if [ -n "$rev" ]; then
    local out; out="$(_api GET "/api/v1/pages/$NS/$SLUG/revisions/$rev")" || return 1
    if [ "$as" = "json" ] || [ "$reviewed_read" -eq 1 ]; then _read_json "$out"
    else printf '%s\n' "$(printf '%s' "$out" | jq -r '.body_html // empty')"; fi
    return 0
  fi
  if [ "$as" = "json" ] || [ "$reviewed_read" -eq 1 ]; then
    local out; out="$(_api GET "/api/v1/pages/$NS/$SLUG")" || return 1
    _read_json "$out"
  else
    # ?body-only returns the raw body fragment as text/html — print verbatim.
    _api GET "/api/v1/pages/$NS/$SLUG?body-only"
  fi
}

cmd_search() {
  local query="" ns="" limit=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --ns|--ns=*)       _need_val "--ns"    "$1" "$#" "${2-}"; ns="$VAL";    shift "$SHIFT" ;;
      --limit|--limit=*) _need_val "--limit" "$1" "$#" "${2-}"; limit="$VAL"; shift "$SHIFT" ;;
      -h|--help) echo "usage: $PROG search QUERY [--ns NS] [--limit N]"; return 0 ;;
      -*) _die "search: unknown flag: $1" ;;
      *) [ -z "$query" ] && query="$1" || query="$query $1"; shift ;;
    esac
  done
  [ -n "$query" ] || _die "usage: $PROG search QUERY [--ns NS] [--limit N]"

  local eq; eq="$(printf '%s' "$query" | jq -sRr @uri)"
  local q="?q=$eq"
  [ -n "$ns" ]    && q="$q&ns=$ns"
  [ -n "$limit" ] && q="$q&limit=$limit"

  local out; out="$(_api GET "/api/v1/search$q")" || return 1
  {
    printf 'NS\tSLUG\tTITLE\tSNIPPET\n'
    printf '%s' "$out" | jq -r '
      (if type=="array" then . else (.results // .hits // .items // .data // []) end)
      | .[] | [ (.ns // .namespace // ""), (.slug // ""), (.title // ""),
                ((.snippet // "") | gsub("[\n\t]+";" ")) ] | @tsv'
  } | _table
}

cmd_revs() {
  local ref=""
  while [ $# -gt 0 ]; do
    case "$1" in
      -h|--help) echo "usage: $PROG revs <ns>/<slug>"; return 0 ;;
      -*) _die "revs: unknown flag: $1" ;;
      *) if [ -z "$ref" ]; then ref="$1"; else _die "revs: unexpected arg: $1"; fi; shift ;;
    esac
  done
  [ -n "$ref" ] || _die "usage: $PROG revs <ns>/<slug>"
  _split_ref "$ref"

  local out; out="$(_api GET "/api/v1/pages/$NS/$SLUG/revisions")" || return 1
  {
    printf 'REV\tDATE\tAUTHOR\tNOTE\n'
    printf '%s' "$out" | jq -r '
      (if type=="array" then . else (.revisions // .items // .data // []) end)
      | .[] | [ (.revision // .rev // ""), (.created_at // ""), (.author // ""),
                ((.note // "") | gsub("[\n\t]+";" ")) ] | @tsv'
  } | _table
}

cmd_activity() {
  local ns="" slug="" actor="" limit="" action=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --ns|--ns=*)         _need_val "--ns"     "$1" "$#" "${2-}"; ns="$VAL";     shift "$SHIFT" ;;
      --slug|--slug=*)     _need_val "--slug"   "$1" "$#" "${2-}"; slug="$VAL";   shift "$SHIFT" ;;
      --actor|--actor=*)   _need_val "--actor"  "$1" "$#" "${2-}"; actor="$VAL";  shift "$SHIFT" ;;
      --action|--action=*) _need_val "--action" "$1" "$#" "${2-}"; action="$VAL"; shift "$SHIFT" ;;
      --limit|--limit=*)   _need_val "--limit"  "$1" "$#" "${2-}"; limit="$VAL";  shift "$SHIFT" ;;
      -h|--help) echo "usage: $PROG activity [--ns NS] [--slug S] [--actor A] [--action A] [--limit N]"; return 0 ;;
      -*) _die "activity: unknown flag: $1" ;;
      *) _die "activity: unexpected arg: $1" ;;
    esac
  done
  local q=""
  [ -n "$ns" ]     && q="$q&ns=$ns"
  [ -n "$slug" ]   && q="$q&slug=$(_normalize_slug "$ns" "$slug")"
  [ -n "$actor" ]  && q="$q&actor=$actor"
  [ -n "$action" ] && q="$q&action=$action"
  [ -n "$limit" ]  && q="$q&limit=$limit"
  q="${q:+?${q#&}}"

  local out; out="$(_api GET "/api/v1/activity$q")" || return 1
  {
    printf 'ID\tTS\tACTOR\tACTION\tTARGET\n'
    printf '%s' "$out" | jq -r '
      (if type=="array" then . else (.activity // .items // .results // .data // []) end)
      | .[] | [ (._id // .id // ""), (.ts // .created_at // ""), (.actor // ""),
                (.action // ""),
                (((.namespace // "") + (if (.slug // "") != "" then "/" + .slug else "" end))) ] | @tsv'
  } | _table
}

# ---------------------------------------------------------------------------
# write verbs
# ---------------------------------------------------------------------------

# Read a page body from FILE (arg) or stdin.
_read_body() {
  if [ -n "${1:-}" ]; then
    [ -f "$1" ] || _die "file not found: $1"
    cat "$1"
  else
    cat
  fi
}

# Emit the machine-readable {namespace,slug,revision,url,warnings} for a write
# verb's captured server response ($1). Falls back to the request's NS/SLUG and
# the computed human URL when the server omits a field.
_write_json() {
  printf '%s' "$1" | jq \
    --arg ns "$NS" --arg slug "$SLUG" --arg url "$(_human_url "$1")" \
    '{ namespace: (.namespace // $ns),
       slug:      (.slug // $slug),
       revision:  (.revision // .rev // null),
       url:       (.url // $url),
       warnings:  (.warnings // []) }'
}

cmd_put() {
  local ref="" title="" tags="" note="" fmt="html" mode="fragment" inline_body=""
  local have_title=0 have_inline_body=0 as_json=0 allow_empty=0
  while [ $# -gt 0 ]; do
    case "$1" in
      --title|--title=*) _need_val "--title" "$1" "$#" "${2-}"; title="$VAL"; have_title=1; shift "$SHIFT" ;;
      --tags|--tags=*)   _need_val "--tags"  "$1" "$#" "${2-}"; tags="$VAL";  shift "$SHIFT" ;;
      --note|--note=*)   _need_val "--note"  "$1" "$#" "${2-}"; note="$VAL";  shift "$SHIFT" ;;
      --body|--body=*)   _need_val "--body"  "$1" "$#" "${2-}"; inline_body="$VAL"; have_inline_body=1; shift "$SHIFT" ;;
      --md)          fmt="markdown"; shift ;;
      --interactive) mode="interactive"; shift ;;
      --allow-empty) allow_empty=1; shift ;;
      --json)        as_json=1; shift ;;
      -h|--help) echo "usage: $PROG put <ns>/<slug> --title T --body TEXT [--tags a,b] [--note N] [--md] [--interactive] [--allow-empty] [--json]"; return 0 ;;
      -*) _die "put: unknown flag: $1" ;;
      *) if [ -z "$ref" ]; then ref="$1"; else _die "put: file input is unavailable; pass page content with --body"; fi; shift ;;
    esac
  done
  [ -n "$ref" ] || _die "usage: $PROG put <ns>/<slug> --title T --body TEXT"
  [ "$have_title" -eq 1 ] || _die "put requires --title"
  [ "$have_inline_body" -eq 1 ] || _die "put requires inline --body; file and stdin input are unavailable"
  _need_token
  _split_ref "$ref"

  local body="$inline_body"
  # Refuse to silently blank a page. A whitespace-only body bumps the revision to
  # an empty page; require --allow-empty to say you meant it.
  if [ "$allow_empty" -ne 1 ] && [ -z "$(printf '%s' "$body" | tr -d '[:space:]')" ]; then
    _die "refusing to PUT an empty/whitespace-only body (pass --allow-empty to blank the page)"
  fi

  # Build the request envelope in a temp file with --rawfile so a multi-MB body
  # never rides on jq's argv (ARG_MAX would kill it and surface a bogus 400).
  local bf; bf="$(mktemp)" || _die "mktemp failed"
  printf '%s' "$body" > "$bf"
  local env; env="$(mktemp)" || { rm -f "$bf"; _die "mktemp failed"; }
  jq -n --rawfile body "$bf" --arg title "$title" --arg mode "$mode" \
        --arg note "$note" --arg tg "$tags" --arg fmt "$fmt" \
    '{title:$title, mode:$mode, body_html:$body}
      + (if $note != "" then {note:$note} else {} end)
      + (if $tg   != "" then {tags: '"$_tags_expr"'} else {} end)
      + (if $fmt == "markdown" then {format:"markdown"} else {} end)' > "$env" \
    || { rm -f "$bf" "$env"; _die 'failed to build request body'; }
  rm -f "$bf"

  local out
  out="$(_api PUT "/api/v1/pages/$NS/$SLUG" -H "Content-Type: application/json" --data-binary @"$env")" \
    || { rm -f "$env"; return 1; }
  rm -f "$env"
  if [ "$as_json" -eq 1 ]; then _write_json "$out"; return 0; fi
  printf 'put %s/%s (rev %s)\n' "$NS" "$SLUG" "$(printf '%s' "$out" | jq -r '.revision // .rev // "?"')"
  # Print the human link to share with the user. Interactive artifacts get the
  # #fullbleed link so it opens straight into full-bleed.
  local human; human="$(_human_url "$out")"
  [ "$mode" = "interactive" ] && human="$human#fullbleed"
  printf '%s\n' "$human"
}

cmd_append() {
  local ref="" note="" fmt="html" inline_body="" as_json=0 have_inline_body=0
  while [ $# -gt 0 ]; do
    case "$1" in
      --note|--note=*) _need_val "--note" "$1" "$#" "${2-}"; note="$VAL"; shift "$SHIFT" ;;
      --body|--body=*) _need_val "--body" "$1" "$#" "${2-}"; inline_body="$VAL"; have_inline_body=1; shift "$SHIFT" ;;
      --md)   fmt="markdown"; shift ;;
      --json) as_json=1; shift ;;
      -h|--help) echo "usage: $PROG append <ns>/<slug> --body TEXT [--note N] [--md] [--json]"; return 0 ;;
      -*) _die "append: unknown flag: $1" ;;
      *) if [ -z "$ref" ]; then ref="$1"; else _die "append: file input is unavailable; pass page content with --body"; fi; shift ;;
    esac
  done
  [ -n "$ref" ] || _die "usage: $PROG append <ns>/<slug> --body TEXT [--note N] [--md]"
  [ "$have_inline_body" -eq 1 ] || _die "append requires inline --body; file and stdin input are unavailable"
  _need_token
  _split_ref "$ref"

  local body="$inline_body"
  [ -n "$(printf '%s' "$body" | tr -d '[:space:]')" ] || _die "refusing to append an empty/whitespace-only body"
  # Build the envelope in a temp file via --rawfile so a multi-MB append body
  # never rides on jq's argv (ARG_MAX).
  local bf; bf="$(mktemp)" || _die "mktemp failed"
  printf '%s' "$body" > "$bf"
  local env; env="$(mktemp)" || { rm -f "$bf"; _die "mktemp failed"; }
  jq -n --rawfile body "$bf" --arg note "$note" --arg fmt "$fmt" \
    '{append_html:$body}
      + (if $note != "" then {note:$note} else {} end)
      + (if $fmt == "markdown" then {format:"markdown"} else {} end)' > "$env" \
    || { rm -f "$bf" "$env"; _die 'failed to build request body'; }
  rm -f "$bf"

  local out
  out="$(_api PATCH "/api/v1/pages/$NS/$SLUG" -H "Content-Type: application/json" --data-binary @"$env")" \
    || { rm -f "$env"; return 1; }
  rm -f "$env"
  if [ "$as_json" -eq 1 ]; then _write_json "$out"; return 0; fi
  printf 'append %s/%s (rev %s)\n' "$NS" "$SLUG" "$(printf '%s' "$out" | jq -r '.revision // .rev // "?"')"
  printf '%s\n' "$(_human_url "$out")"
}

cmd_rm() {
  local ref=""
  while [ $# -gt 0 ]; do
    case "$1" in
      -h|--help) echo "usage: $PROG rm <ns>/<slug>"; return 0 ;;
      -*) _die "rm: unknown flag: $1" ;;
      *) if [ -z "$ref" ]; then ref="$1"; else _die "rm: unexpected arg: $1"; fi; shift ;;
    esac
  done
  [ -n "$ref" ] || _die "usage: $PROG rm <ns>/<slug>"
  _need_token
  _split_ref "$ref"
  _api DELETE "/api/v1/pages/$NS/$SLUG" >/dev/null || return 1
  printf 'deleted (soft) %s/%s — restorable for 30 days\n' "$NS" "$SLUG"
}

cmd_restore() {
  local ref=""
  while [ $# -gt 0 ]; do
    case "$1" in
      -h|--help) echo "usage: $PROG restore <ns>/<slug>"; return 0 ;;
      -*) _die "restore: unknown flag: $1" ;;
      *) if [ -z "$ref" ]; then ref="$1"; else _die "restore: unexpected arg: $1"; fi; shift ;;
    esac
  done
  [ -n "$ref" ] || _die "usage: $PROG restore <ns>/<slug>"
  _need_token
  _split_ref "$ref"
  _api POST "/api/v1/pages/$NS/$SLUG/restore" >/dev/null || return 1
  printf 'restored %s/%s\n' "$NS" "$SLUG"
}

cmd_mv() {
  local ref="" dest=""
  while [ $# -gt 0 ]; do
    case "$1" in
      -h|--help) echo "usage: $PROG mv <ns>/<slug> <new-slug>   (same namespace; live child pages move too)"; return 0 ;;
      -*) _die "mv: unknown flag: $1" ;;
      *)
        if [ -z "$ref" ]; then ref="$1"
        elif [ -z "$dest" ]; then dest="$1"
        else _die "mv: unexpected arg: $1"; fi
        shift ;;
    esac
  done
  [ -n "$ref" ] && [ -n "$dest" ] || _die "usage: $PROG mv <ns>/<slug> <new-slug>"
  _need_token
  _split_ref "$ref"
  # Accept the destination with or without the namespace prefix.
  case "$dest" in "$NS"/*) dest="${dest#"$NS"/}" ;; esac

  local tmp out
  tmp="$(jq -n --arg to "$dest" '{to:$to}' | _json_body_file)"
  out="$(_api POST "/api/v1/pages/$NS/$SLUG/move" \
      -H "Content-Type: application/json" --data-binary @"$tmp")" \
    || { rm -f "$tmp"; return 1; }
  rm -f "$tmp"
  printf 'moved %s/%s -> %s/%s\n' "$NS" "$SLUG" "$NS" \
    "$(printf '%s' "$out" | jq -r '.slug // "?"')"
  printf '%s\n' "$(printf '%s' "$out" | jq -r '.url // empty')"
}

cmd_revert() {
  local ref="" rev="" note=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --rev|--rev=*)   _need_val "--rev"  "$1" "$#" "${2-}"; rev="$VAL";  shift "$SHIFT" ;;
      --note|--note=*) _need_val "--note" "$1" "$#" "${2-}"; note="$VAL"; shift "$SHIFT" ;;
      -h|--help) echo "usage: $PROG revert <ns>/<slug> --rev N [--note WHY]"; return 0 ;;
      -*) _die "revert: unknown flag: $1" ;;
      *) if [ -z "$ref" ]; then ref="$1"; else _die "revert: unexpected arg: $1"; fi; shift ;;
    esac
  done
  [ -n "$ref" ] || _die "usage: $PROG revert <ns>/<slug> --rev N [--note WHY]"
  [ -n "$rev" ] || _die "revert requires --rev N"
  _need_token
  _split_ref "$ref"

  # Read-modify-write: fetch current revision and send it as If-Match (§5.1).
  local cur currev
  cur="$(_api GET "/api/v1/pages/$NS/$SLUG")" || return 1
  currev="$(printf '%s' "$cur" | jq -r '.revision // .rev // empty')"

  local tmp
  tmp="$(jq -n --argjson rev "$rev" --arg note "$note" \
    '{revision:$rev} + (if $note != "" then {note:$note} else {} end)' | _json_body_file)"

  local ifm=()
  [ -n "$currev" ] && ifm=(-H "If-Match: $currev")
  local out
  out="$(_api POST "/api/v1/pages/$NS/$SLUG/revert" \
      "${ifm[@]+"${ifm[@]}"}" \
      -H "Content-Type: application/json" --data-binary @"$tmp")" \
    || { rm -f "$tmp"; return 1; }
  rm -f "$tmp"
  printf 'reverted %s/%s to rev %s (now rev %s)\n' "$NS" "$SLUG" "$rev" \
    "$(printf '%s' "$out" | jq -r '.revision // .rev // "?"')"
}

cmd_undo() {
  local id=""
  while [ $# -gt 0 ]; do
    case "$1" in
      -h|--help) echo "usage: $PROG undo ACTIVITY-ID"; return 0 ;;
      -*) _die "undo: unknown flag: $1" ;;
      *) if [ -z "$id" ]; then id="$1"; else _die "undo: unexpected arg: $1"; fi; shift ;;
    esac
  done
  [ -n "$id" ] || _die "usage: $PROG undo ACTIVITY-ID"
  _need_token
  local out; out="$(_api POST "/api/v1/activity/$id/undo")" || return 1
  printf 'undone activity %s\n' "$id"
  printf '%s' "$out" | jq -e . >/dev/null 2>&1 && printf '%s\n' "$(printf '%s' "$out" | jq -c '{action: (.action // "?"), namespace: (.namespace // null), slug: (.slug // null)}')"
  return 0
}

# ---------------------------------------------------------------------------
# assets
# ---------------------------------------------------------------------------

# Upload a single file, returning the raw JSON response on stdout.
_asset_upload() {
  local file="$1"
  [ -f "$file" ] || _die "asset not found: $file"
  local ct; ct="$(_content_type "$file")"
  _api POST "/api/v1/assets" -H "Content-Type: $ct" --data-binary @"$file"
}

cmd_asset() {
  local sub="${1:-}"; shift || true
  case "$sub" in
    put)
      local file=""
      while [ $# -gt 0 ]; do
        case "$1" in
          -h|--help) echo "usage: $PROG asset put FILE"; return 0 ;;
          -*) _die "asset put: unknown flag: $1" ;;
          *) if [ -z "$file" ]; then file="$1"; else _die "asset put: unexpected arg: $1"; fi; shift ;;
        esac
      done
      [ -n "$file" ] || _die "usage: $PROG asset put FILE"
      _need_token
      local out; out="$(_asset_upload "$file")" || return 1
      local url ct
      url="$(printf '%s' "$out" | jq -r '.url // empty')"
      ct="$(printf '%s' "$out"  | jq -r '.content_type // empty')"
      [ -n "$url" ] || _die "asset upload returned no url"
      printf '%s\n' "$url"
      case "$ct" in
        video/*) printf '<video src="%s" controls></video>\n' "$url" ;;
        *)       printf '<img src="%s" alt="">\n' "$url" ;;
      esac
      ;;
    ""|-h|--help) echo "usage: $PROG asset put FILE" ;;
    *) _die "unknown asset subcommand: $sub (expected: put)" ;;
  esac
}

# ---------------------------------------------------------------------------
# publish — pages with images (DESIGN §9.1)
# ---------------------------------------------------------------------------
#
# Reference extraction is intentionally simple: grep for src="…", href="…",
# and poster="…" attributes. LIMITATIONS (documented per DESIGN §9.1):
#   * Only DOUBLE-QUOTED attribute values are detected — src='x' (single
#     quotes) and unquoted attributes are ignored.
#   * The whole attribute (name="value") must sit on one line — a value split
#     across newlines is not matched.
#   * srcset, CSS url(), and <source src> inside <picture>/<video> are not
#     parsed. Reference such assets with a plain src/href/poster attribute, or
#     upload them with `wiki asset put` and hardcode the /a/ URL.
#   * Relative href="other-page" links are treated as asset references; if no
#     local file matches, publish fails. Use an absolute path (/ns/slug) for
#     inter-page links so they are skipped.
# Refs that are absolute URLs, /a/…, /lib/…, data:, //…, or #anchors are
# skipped. Remaining relative refs are resolved against the HTML file's
# directory; a missing file aborts BEFORE any network write (no half-publish).

cmd_publish() {
  local title="" tags="" note="" mode="fragment"
  local pos=()
  local have_title=0 as_json=0
  while [ $# -gt 0 ]; do
    case "$1" in
      --title|--title=*) _need_val "--title" "$1" "$#" "${2-}"; title="$VAL"; have_title=1; shift "$SHIFT" ;;
      --tags|--tags=*)   _need_val "--tags"  "$1" "$#" "${2-}"; tags="$VAL";  shift "$SHIFT" ;;
      --note|--note=*)   _need_val "--note"  "$1" "$#" "${2-}"; note="$VAL";  shift "$SHIFT" ;;
      --interactive) mode="interactive"; shift ;;
      --json)        as_json=1; shift ;;
      -h|--help) echo "usage: $PROG publish <ns>/<slug> FILE --title T [--tags a,b] [--interactive] [--note N] [--json]"; return 0 ;;
      --) shift; while [ $# -gt 0 ]; do pos+=("$1"); shift; done ;;
      -*) _die "publish: unknown flag: $1" ;;
      *) pos+=("$1"); shift ;;
    esac
  done
  [ "${#pos[@]}" -ge 2 ] || _die "usage: $PROG publish <ns>/<slug> FILE --title T"
  [ "$have_title" -eq 1 ] || _die "publish requires --title"
  local ref="${pos[0]}" file="${pos[1]}"
  [ -f "$file" ] || _die "html file not found: $file"
  _need_token
  _split_ref "$ref"

  local dir; dir="$(cd "$(dirname "$file")" && pwd)" || _die "cannot resolve directory of $file"
  local content; content="$(cat "$file")"

  # 1. extract candidate refs.
  local refs
  refs="$(grep -oE '(src|href|poster)="[^"]*"' "$file" 2>/dev/null \
          | sed -E 's/^(src|href|poster)="//; s/"$//' | sort -u)"

  # 2. filter to relative local candidates.
  local candidates=()
  local r
  while IFS= read -r r; do
    [ -n "$r" ] || continue
    case "$r" in
      /*|\#*) continue ;;                                      # absolute path / anchor
    esac
    if printf '%s' "$r" | grep -qE '^[a-zA-Z][a-zA-Z0-9+.-]*:' ; then continue; fi   # scheme (http:, data:, mailto:)
    candidates+=("$r")
  done <<REFS
$refs
REFS

  # 3. fail before any write if a referenced file is missing.
  local missing=()
  for r in "${candidates[@]+"${candidates[@]}"}"; do
    [ -f "$dir/$r" ] || missing+=("$r")
  done
  if [ "${#missing[@]}" -gt 0 ]; then
    printf '%s: error: %d referenced file(s) missing — nothing uploaded:\n' "$PROG" "${#missing[@]}" >&2
    for r in "${missing[@]}"; do printf '  %s\n' "$r" >&2; done
    return 1
  fi

  # 4. upload each unique file, rewrite its refs to the /a/ URL.
  local manifest="" seen=" " resp url
  for r in "${candidates[@]+"${candidates[@]}"}"; do
    case "$seen" in *" $r "*) continue ;; esac
    seen="$seen$r "
    resp="$(_asset_upload "$dir/$r")" || return 1
    url="$(printf '%s' "$resp" | jq -r '.url // empty')"
    [ -n "$url" ] || _die "asset upload returned no url for $r"
    content="${content//\"$r\"/\"$url\"}"
    manifest="${manifest}  ${r} -> ${url}"$'\n'
  done

  # 5. PUT the rewritten page. Build the envelope in a temp file via --rawfile so
  # a multi-MB page body never rides on jq's argv (ARG_MAX).
  local bf; bf="$(mktemp)" || _die "mktemp failed"
  printf '%s' "$content" > "$bf"
  local env; env="$(mktemp)" || { rm -f "$bf"; _die "mktemp failed"; }
  jq -n --rawfile body "$bf" --arg title "$title" --arg mode "$mode" \
        --arg note "$note" --arg tg "$tags" \
    '{title:$title, mode:$mode, body_html:$body}
      + (if $note != "" then {note:$note} else {} end)
      + (if $tg   != "" then {tags: '"$_tags_expr"'} else {} end)' > "$env" \
    || { rm -f "$bf" "$env"; _die 'failed to build request body'; }
  rm -f "$bf"
  local out
  out="$(_api PUT "/api/v1/pages/$NS/$SLUG" -H "Content-Type: application/json" --data-binary @"$env")" \
    || { rm -f "$env"; return 1; }
  rm -f "$env"

  if [ "$as_json" -eq 1 ]; then _write_json "$out"; return 0; fi
  printf 'published %s/%s (rev %s)\n' "$NS" "$SLUG" "$(printf '%s' "$out" | jq -r '.revision // .rev // "?"')"
  if [ -n "$manifest" ]; then
    printf '\nasset manifest (local -> wiki):\n%s' "$manifest"
  else
    printf '\n(no local assets referenced)\n'
  fi
  local human; human="$(_human_url "$out")"
  [ "$mode" = "interactive" ] && human="$human#fullbleed"
  printf '\n%s\n' "$human"
}

# ---------------------------------------------------------------------------
# namespaces
# ---------------------------------------------------------------------------

cmd_ns() {
  local sub="${1:-}"; shift || true
  case "$sub" in
    ls)
      local out; out="$(_api GET "/api/v1/namespaces")" || return 1
      {
        printf 'NAME\tTITLE\tINDEX_MODE\tSORT\tNAV\n'
        printf '%s' "$out" | jq -r '
          (if type=="array" then . else (.namespaces // .items // .data // []) end)
          | .[] | [ (.name // ""), (.title // ""), (.index_mode // ""),
                    (.sort // ""), (.nav_order // "" | tostring) ] | @tsv'
      } | _table
      ;;
    get)
      local name="${1:-}"; [ -n "$name" ] || _die "usage: $PROG ns get NAME"
      _api GET "/api/v1/namespaces/$name" | jq .
      ;;
    add|set)
      local name="" title="" desc="" sort="" index_mode="" slug_pattern="" nav_order=""
      local have_title=0 have_desc=0 have_sort=0 have_index=0 have_pattern=0 have_nav=0
      while [ $# -gt 0 ]; do
        case "$1" in
          --title|--title=*)               _need_val "--title"        "$1" "$#" "${2-}"; title="$VAL";        have_title=1;   shift "$SHIFT" ;;
          --desc|--desc=*)                 _need_val "--desc"         "$1" "$#" "${2-}"; desc="$VAL";         have_desc=1;    shift "$SHIFT" ;;
          --sort|--sort=*)                 _need_val "--sort"         "$1" "$#" "${2-}"; sort="$VAL";         have_sort=1;    shift "$SHIFT" ;;
          --index-mode|--index-mode=*)     _need_val "--index-mode"   "$1" "$#" "${2-}"; index_mode="$VAL";   have_index=1;   shift "$SHIFT" ;;
          --slug-pattern|--slug-pattern=*) _need_val "--slug-pattern" "$1" "$#" "${2-}"; slug_pattern="$VAL"; have_pattern=1; shift "$SHIFT" ;;
          --nav-order|--nav-order=*)       _need_val "--nav-order"    "$1" "$#" "${2-}"; nav_order="$VAL";    have_nav=1;     shift "$SHIFT" ;;
          -h|--help) echo "usage: $PROG ns $sub NAME [--title T] [--desc D] [--sort alpha|updated|key-desc] [--index-mode auto|manual] [--slug-pattern RE] [--nav-order N]"; return 0 ;;
          -*) _die "ns $sub: unknown flag: $1" ;;
          *) if [ -z "$name" ]; then name="$1"; else _die "ns $sub: unexpected arg: $1"; fi; shift ;;
        esac
      done
      [ -n "$name" ] || _die "usage: $PROG ns $sub NAME [flags]"

      local tmp
      tmp="$(jq -n \
        --arg name "$name" --arg title "$title" --arg desc "$desc" \
        --arg sort "$sort" --arg im "$index_mode" --arg sp "$slug_pattern" \
        --arg nav "$nav_order" \
        --argjson add "$([ "$sub" = "add" ] && echo true || echo false)" \
        --argjson hT "$have_title" --argjson hD "$have_desc" --argjson hS "$have_sort" \
        --argjson hI "$have_index" --argjson hP "$have_pattern" --argjson hN "$have_nav" \
        '(if $add then {name:$name} else {} end)
          + (if $hT==1 then {title:$title} else {} end)
          + (if $hD==1 then {description:$desc} else {} end)
          + (if $hS==1 then {sort:$sort} else {} end)
          + (if $hI==1 then {index_mode:$im} else {} end)
          + (if $hP==1 then {slug_pattern:$sp} else {} end)
          + (if $hN==1 then {nav_order:($nav|tonumber)} else {} end)' | _json_body_file)"

      if [ "$sub" = "add" ]; then
        _api POST "/api/v1/namespaces" -H "Content-Type: application/json" --data-binary @"$tmp" >/dev/null \
          || { rm -f "$tmp"; return 1; }
        rm -f "$tmp"; printf 'created namespace %s\n' "$name"
      else
        _api PATCH "/api/v1/namespaces/$name" -H "Content-Type: application/json" --data-binary @"$tmp" >/dev/null \
          || { rm -f "$tmp"; return 1; }
        rm -f "$tmp"; printf 'updated namespace %s\n' "$name"
      fi
      ;;
    sort)
      # Explicit tree order (sort hints): paths listed in the desired order get
      # ascending weights; every index view (sidebar, landing, API tree) shows
      # hinted paths first, the rest in the namespace's natural order. Paths may
      # name interior nodes (slug prefixes) to order whole subtrees, and deeper
      # paths ("guides/advanced") order within their own level. Replaces the
      # whole hint set each time.
      local name="" clear=0; local -a paths=()
      while [ $# -gt 0 ]; do
        case "$1" in
          --clear) clear=1; shift ;;
          -h|--help)
            cat <<EOF
usage: $PROG ns sort NAME                 show current sort hints
       $PROG ns sort NAME PATH [PATH...]  set order: paths first, in this order
       $PROG ns sort NAME --clear         remove all hints (natural order)
EOF
            return 0 ;;
          -*) _die "ns sort: unknown flag: $1" ;;
          *) if [ -z "$name" ]; then name="$1"; else paths+=("$1"); fi; shift ;;
        esac
      done
      [ -n "$name" ] || _die "usage: $PROG ns sort NAME [PATH...] | --clear"
      if [ "$clear" = 0 ] && [ "${#paths[@]}" -eq 0 ]; then
        _api GET "/api/v1/namespaces/$name" \
          | jq -r '(.sort_hints // {}) | to_entries | sort_by(.value) | .[] | "\(.value)\t\(.key)"' \
          | { printf 'WEIGHT\tPATH\n'; cat; } | _table
        return 0
      fi
      _need_token
      local hints tmp
      if [ "$clear" = 1 ]; then
        hints='{}'
      else
        hints="$(printf '%s\n' "${paths[@]}" | jq -R . | jq -s 'to_entries | map({(.value): ((.key + 1) * 10)}) | add')"
      fi
      tmp="$(jq -n --argjson h "$hints" '{sort_hints: $h}' | _json_body_file)"
      _api PATCH "/api/v1/namespaces/$name" -H "Content-Type: application/json" --data-binary @"$tmp" >/dev/null \
        || { rm -f "$tmp"; return 1; }
      rm -f "$tmp"
      if [ "$clear" = 1 ]; then printf 'cleared sort hints on %s\n' "$name"
      else printf 'set %d sort hint(s) on %s\n' "${#paths[@]}" "$name"; fi
      ;;
    rm)
      local name="" cascade=""
      while [ $# -gt 0 ]; do
        case "$1" in
          --cascade) cascade="1"; shift ;;
          -h|--help) echo "usage: $PROG ns rm NAME [--cascade]"; return 0 ;;
          -*) _die "ns rm: unknown flag: $1" ;;
          *) if [ -z "$name" ]; then name="$1"; else _die "ns rm: unexpected arg: $1"; fi; shift ;;
        esac
      done
      [ -n "$name" ] || _die "usage: $PROG ns rm NAME [--cascade]"
      _need_token
      local q=""; [ -n "$cascade" ] && q="?cascade=1"
      _api DELETE "/api/v1/namespaces/$name$q" >/dev/null || return 1
      printf 'deleted (soft) namespace %s\n' "$name"
      ;;
    restore)
      local name="${1:-}"; [ -n "$name" ] || _die "usage: $PROG ns restore NAME"
      _need_token
      _api POST "/api/v1/namespaces/$name/restore" >/dev/null || return 1
      printf 'restored namespace %s\n' "$name"
      ;;
    ""|-h|--help) echo "usage: $PROG ns ls|get|add|set|sort|rm|restore [...]" ;;
    *) _die "unknown ns subcommand: $sub (expected: ls, get, add, set, sort, rm, restore)" ;;
  esac
}

# add/set need a token up front; sort checks inside (its no-arg form is a read).
_ns_needs_token() { case "${1:-}" in add|set) _need_token ;; esac; }

cmd_url() {
  local ref="${1:-}"
  [ -n "$ref" ] || _die "usage: $PROG url <ns>/<slug>|<page-url>"
  _need_url
  if _page_id_from_ref "$ref"; then
    local resolved; resolved="$(_api GET "/api/v1/page/$PAGE_ID")" || return 1
    printf '%s\n' "$(_human_url "$resolved")"
    return 0
  elif [ "$?" -eq 2 ]; then
    _die "malformed opaque page URL: $ref"
  fi
  _split_ref "$ref"
  # Pages are id-addressed, so resolve the slug to its id via the API.
  local out; out="$(_api GET "/api/v1/pages/$NS/$SLUG")" || return 1
  printf '%s\n' "$(_human_url "$out")"
}

# ---------------------------------------------------------------------------
# usage / dispatch
# ---------------------------------------------------------------------------

_usage() {
  cat <<'USAGE'
wiki — TelemetryOS Agent Wiki CLI

Reads:
  wiki map                                             whole-wiki index (every namespace + page) — start here
  wiki ls   <ns> [--prefix P] [--tag T] [--limit N]   flat page list (auto-paginated; --limit caps to one page)
  wiki tree <ns> [--depth N]                           document tree
  wiki get  <ns>/<slug>|<page-url> [--json] [--rev N]  page body / JSON
  wiki search QUERY [--ns NS] [--limit N]              full-text search
  wiki revs <ns>/<slug>                                revision list
  wiki activity [--ns NS] [--slug S] [--actor A] [--action A] [--limit N]
  wiki url  <ns>/<slug>|<page-url>                     print the human page URL

Writes (need WIKI_TOKEN):
  wiki put    <ns>/<slug> --title T --body TEXT [--tags a,b] [--note N] [--md] [--interactive] [--allow-empty] [--json]
  wiki append <ns>/<slug> --body TEXT [--note N] [--md] [--json]   atomic append
  wiki rm     <ns>/<slug>                              soft delete (30-day retention)
  wiki restore <ns>/<slug>                             undelete within the window
  wiki revert <ns>/<slug> --rev N [--note WHY]         copy an old rev forward (sends If-Match)

  This reviewed wrapper exposes page CRUD only. Namespace, asset, publish-file,
  cascading move, activity, generic undo, and admin commands are unavailable.

Config: WIKI_URL + WIKI_TOKEN (env), or ~/.config/telemetryos/wiki/config
        (url=... / token=... lines). Env wins. WIKI_AUTHOR sets X-Wiki-Author.
USAGE
}

main() {
  _load_config
  local cmd="${1:-}"; shift || true
  case "${TOS_TAG_OPERATION_ID:-}" in
    read)
      case "$cmd" in map|overview|llms|ls|tree|get|search|revs|url) ;;
        *) _die "command '$cmd' is not permitted by the read operation" ;;
      esac ;;
    write)
      case "$cmd" in put|append|restore|revert) ;;
        *) _die "command '$cmd' is not permitted by the write operation" ;;
      esac ;;
    delete) [ "$cmd" = rm ] || _die "only recoverable page soft-delete is permitted by the delete operation" ;;
    *) _die "operation is unavailable; only page CRUD operations read, write, and delete are supported" ;;
  esac
  case "$cmd" in
    map|overview|llms) cmd_map "$@" ;;
    ls)       cmd_ls "$@" ;;
    tree)     cmd_tree "$@" ;;
    get)      cmd_get "$@" ;;
    put)      cmd_put "$@" ;;
    append)   cmd_append "$@" ;;
    rm)       cmd_rm "$@" ;;
    restore)  cmd_restore "$@" ;;
    search)   cmd_search "$@" ;;
    revs)     cmd_revs "$@" ;;
    revert)   cmd_revert "$@" ;;
    url)      cmd_url "$@" ;;
    ""|-h|--help|help) _usage ;;
    *) _die "unknown command: $cmd (try: $PROG --help)" ;;
  esac
}

main "$@"
