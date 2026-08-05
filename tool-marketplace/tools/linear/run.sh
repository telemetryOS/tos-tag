#!/usr/bin/env bash
# shellcheck disable=SC2016,SC2018,SC2019
#
# linear.sh — compact Linear GraphQL reader and lifecycle writer for ENG work.
# Shared runtime helper (called by lifecycle steps in $investigate / $fix /
# $pr / $deploy / $qapass / $qafail); replaces expensive reader/writer
# subagent spawns for bounded operations. The judgment — WHICH state, WHAT
# evidence, and whether history needs interpretation — stays with the agent;
# this only removes the mechanical API dance. State/label UUIDs are pinned in
# ../.references/linear-state-ids.md.
#
# Auth: requires LINEAR_API_KEY in the environment (personal key; Linear takes
# it as a raw `Authorization:` header, no Bearer). The key is never printed or
# logged; it reaches curl via a 0600 temp header file (removed on exit), never
# argv, so `ps` cannot read it from the live process. LINEAR_API_URL may
# override the endpoint (tests); defaults to https://api.linear.app/graphql.
#
# Usage:
#   linear.sh get --issue ENG-1234 [--description-limit <chars>]
#     → compact multi-line issue brief with capped description, image locations, links, and branch
#   linear.sh comments --issue ENG-1234 [--limit <count>] [--body-limit <chars>]
#     → capped recent comments, oldest → newest, with author and timestamps
#   linear.sh whoami
#     → VIEWER=<uuid> NAME=<display-name>
#   linear.sh mine [--team <key>] [--state <name>]... [--limit <count>]
#     → the viewer's actionable issues (default team ENG, limit 50), pre-sorted
#       into workday order for $myissues: active work first, Failed QA second,
#       then triage/backlog/unstarted; Urgent→High→Medium→Low→None within each
#       group; most recently updated first within equal priority. Completed and
#       canceled issues are excluded server-side; repeatable --state <name>
#       replaces the default actionable-type scope with exact state names
#       (the $myissues call passes Failed QA and the To Do variants). Three
#       lines per issue (ISSUE= / ISSUE_TITLE= / ISSUE_LABELS=) after a
#       VIEWER= header line.
#   linear.sh list [--team <key>] [--state <name>]... [--assignee me|none|<uuid|email>] [--label <name>] [--limit <count>]
#     → selector read over team issues (default team ENG, limit 50), same
#       workday-sorted three-lines-per-issue shape as `mine` plus ASSIGNEE_ID=.
#       Default scope is actionable states (triage/backlog/unstarted/started);
#       repeatable --state <name> replaces that with exact state names. The
#       $suitability batch-selector path.
#   linear.sh search --query <text> [--team <key>|all] [--state <name>]... [--assignee me|none|<uuid|email>] [--label <name>] [--include-archived] [--limit <count>]
#     → relevance-ranked full-text search (Linear searchIssues) over issue
#       titles, descriptions, and comments; default team ENG, limit 25. Unlike
#       `list`/`mine` there is NO default state scope: completed and canceled
#       issues match too, which is what duplicate-hunting before $bug/$feature
#       and "find the ticket about X" reads need. `--team all` drops the team
#       filter; `--include-archived` adds archived issues; repeatable --state
#       and the assignee/label flags narrow exactly like `list`. Same
#       three-lines-per-issue shape as `list` but in API relevance order
#       (ORDER=relevance, best match first — do not re-sort), with TOTAL=
#       reporting the full match count beyond the returned page.
#   linear.sh history --issue ENG-1234 [--limit <count>]
#     → capped recent state/assignee/priority change events, oldest → newest,
#       two lines per event (EVENT= with actor ids, EVENT_CHANGE= with the
#       from → to names). The $qafail prior-developer evidence path; nodes
#       with no tracked change (title edits etc.) are filtered out.
#   linear.sh members [--query <text>] [--limit <count>]
#     → active workspace users (id, email, display handle, name), optionally
#       narrowed by a name/display/email substring. Resolves a person to the
#       uuid/email that `update --assign` takes.
#   linear.sh set-state --issue ENG-1234 --state-id <uuid>
#     → ISSUE=ENG-1234 STATE_APPLIED=<uuid> STATE_NAME=<name>
#   linear.sh comment --issue ENG-1234 (--body <text> | --body-file <path>)
#     → ISSUE=ENG-1234 COMMENT_ID=<uuid>
#   linear.sh update --issue ENG-1234 [--title <text> | --title-file <path>] [--description <text> | --description-file <path>] [--comment <text> | --comment-file <path>] [--assign-me | --assign <uuid|email>] [--priority <0-4>] [--add-label <name>] [--remove-label <name>]
#     → ISSUE=ENG-1234 [TITLE_APPLIED=1 TITLE_CHARS=<n>] [DESCRIPTION_APPLIED=1 DESCRIPTION_CHARS=<n>] [ASSIGNEE=<uuid>] [PRIORITY=<n>] [STATE_PRESERVED=1 LABELS_PRESERVED=1] [LABELS=<comma-joined names>]
#       --assign takes a user UUID (from `history` events or `members`) or an
#       email resolved against active workspace users (the $qafail
#       assign-back-to-developer path).
#       Inline values are the disposable-worker path; existing non-empty files
#       remain available to trusted local callers. Titles must contain one line.
#       Output reports read-back equality and character counts without printing
#       title or description contents.
#   linear.sh start --issue ENG-1234 --assign-me --state-id <uuid> (--comment <text> | --comment-file <path>)
#     → ISSUE=ENG-1234 [ASSIGNEE=<uuid>] [PRIORITY=<n>] [STATE_APPLIED=<uuid>] [COMMENT_ID=<uuid>] STATE_NAME=<name>
#   linear.sh update --issue ENG-1234 [--assign-me] [--priority <0-4>] [--state-id <uuid>] [--comment-file <path>] [...]
#     → same one-line contract; one issueUpdate mutation, then one commentCreate mutation when requested
#   linear.sh create --title <text> (--description <text> | --description-file <path>) [--parent <ENG-key|uuid>] [--team-id <uuid>] [--state-id <uuid>] [--priority <0-4>] [--label-id <uuid>]... [--label <name>]...
#     → ISSUE=ENG-1234 URL=<issue url> STATE_APPLIED=<uuid> PRIORITY=<n> TITLE_APPLIED=1 TITLE_CHARS=<n> DESCRIPTION_APPLIED=1 DESCRIPTION_CHARS=<n> PARENT_APPLIED=1 PARENT=<ENG-key|none> PARENT_ID=<uuid|none> [LABELS_APPLIED=1 LABELS=<comma-joined names> | STATE_NAME=<name>]
#   linear.sh upload --file <path>
#     → SIZE=<bytes> ASSET_URL=<uploads.linear.app url> FILE=<filename>
#   linear.sh download --url <uploads.linear.app asset url> --out <path>
#     → SIZE=<bytes> CONTENT_TYPE=<mime> FILE=<path>
#
# `upload` stores a file (screenshot or short recording evidence, usually) in Linear's workspace
# file storage via the fileUpload mutation and a signed PUT. Embed the returned
# ASSET_URL in a later `comment --body-file` body as `![label](ASSET_URL)` for
# screenshots or `[validation video](ASSET_URL)` for recordings. Assets are
# available to authenticated workspace members only.
#
# `download` fetches a ticket-attached asset (screenshots embedded in issue
# descriptions/comments, usually) to a local file so the agent can view it —
# see Ticket Image Intake in ../.references/linear-lifecycle.md. The API key
# is sent only to uploads.linear.app; any other host is refused.
#
# Output parsing: fixed `KEY=value` tokens come first; any free-text field
# (for example TITLE=, DESCRIPTION=, AUTHOR=, BODY=, NAME=, LABELS=, or FILE=)
# is always LAST on its line and may contain spaces. Parse leading fixed tokens
# and treat the remainder as the free-text value.
#
# `--add-label` / `--remove-label` fetch the issue's current labelIds and
# union/subtract the named label before writing, because `labelIds` REPLACES
# the set (same subtlety linear-state-ids.md documents for the MCP path).
# An unknown `--add-label` name is an error; an unknown `--remove-label` name
# is a no-op (the desired end state already holds).
#
# `create` files a new issue (the $bug / $feature path). Defaults: ENG team,
# Triage state, Medium priority (3), unassigned — override with --team-id /
# --state-id / --priority (UUIDs pinned in ../.references/linear-state-ids.md).
# `--label <name>` resolves against the target team or workspace and an unknown
# name is an error (this script does not create labels); `--label-id <uuid>`
# values are passed through after a UUID shape check. Both flags repeat.
# `--parent` creates a true Linear sub-issue. It accepts an ENG key or issue
# UUID, resolves the issue before creation, passes the resolved UUID as
# `parentId`, and verifies the returned parent. Invalid or missing parents fail
# before issueCreate. State and label inputs are still applied and verified.
# LABELS= is printed (last) when labels were requested, else STATE_NAME=.
# If the requested state does not take, the result line is still printed
# (STATE_APPLIED= reports the actual state) before exit 3, so the caller
# never loses the reference to the issue that was already created.
#
# The read verbs (`get`, `comments`, `mine`, `list`, `search`, `history`,
# `members`)
# deliberately return selected, capped fields rather than raw Linear JSON. `get` scans the description and the newest 50 comments for
# uploads.linear.app image URLs and reports when that scan is truncated.
# `comments --limit N` returns the newest N comments in oldest-to-newest order.
#
# Exit: 0 ok · 2 usage/precondition (missing key, bad args, unknown issue or
# label, transport/network failure) · 3 rejected by the API — any GraphQL
# errors[] response, including auth rejections from an invalid key.

set -uo pipefail
die()    { echo "linear: $*" >&2; exit 2; }
reject() { echo "linear: API rejected: $*" >&2; exit 3; }

API_URL="${LINEAR_API_URL:-https://api.linear.app/graphql}"

cmd="${1:-}"; [ $# -gt 0 ] && shift
case "${TOS_TAG_OPERATION_ID:-}" in
  read)
    case "$cmd" in get|comments|whoami|mine|list|search|history|members|download) ;;
      *) die "command '$cmd' is not permitted by the read operation" ;;
    esac ;;
  intake)
    case "$cmd" in create|comment|update) ;;
      *) die "command '$cmd' is not permitted by the intake operation" ;;
    esac ;;
  write)
    case "$cmd" in set-state|comment|update|start|create|upload) ;;
      *) die "command '$cmd' is not permitted by the write operation" ;;
    esac ;;
  *) die "TOS_TAG_OPERATION_ID must be read, intake, or write" ;;
esac

# The approval-free intake operation is intentionally narrower than generic
# Linear write authority. It supports only explicit bug/feature creation,
# evidence comments, feature normalization, and the suitability label/comment
# follow-up owned by those workflows. Assignment, state, priority changes on
# existing issues, file access/uploads, and arbitrary label mutations remain on
# the approval-gated write operation.
if [ "${TOS_TAG_OPERATION_ID:-}" = intake ]; then
  intake_args=("$@")
  case "$cmd" in
    create)
      intake_type=""
      while [ "$#" -gt 0 ]; do
        case "$1" in
          --title|--description)
            [ "$#" -ge 2 ] || die "$1 needs a value"
            shift 2 ;;
          --priority)
            [ "$#" -ge 2 ] || die "--priority needs a value"
            case "$2" in 2|3) ;; *) die "intake create priority must be High (2) or Medium (3)" ;; esac
            shift 2 ;;
          --label)
            [ "$#" -ge 2 ] || die "--label needs a value"
            case "$2" in
              Bug|Feature)
                [ -z "$intake_type" ] || [ "$intake_type" = "$2" ] || die "intake create cannot combine Bug and Feature labels"
                intake_type="$2" ;;
            esac
            shift 2 ;;
          *) die "argument '$1' is not permitted by the intake create operation" ;;
        esac
      done
      [ -n "$intake_type" ] || die "intake create requires a Bug or Feature label" ;;
    comment)
      while [ "$#" -gt 0 ]; do
        case "$1" in
          --issue|--body)
            [ "$#" -ge 2 ] || die "$1 needs a value"
            shift 2 ;;
          *) die "argument '$1' is not permitted by the intake comment operation" ;;
        esac
      done ;;
    update)
      while [ "$#" -gt 0 ]; do
        case "$1" in
          --issue|--description|--comment)
            [ "$#" -ge 2 ] || die "$1 needs a value"
            shift 2 ;;
          --add-label|--remove-label)
            [ "$#" -ge 2 ] || die "$1 needs a value"
            case "$2" in
              Bug|Feature|agent-suitable|agent-unsuitable) ;;
              *) die "label '$2' is not permitted by the intake update operation" ;;
            esac
            shift 2 ;;
          *) die "argument '$1' is not permitted by the intake update operation" ;;
        esac
      done ;;
  esac
  set -- "${intake_args[@]}"
fi
case "$cmd" in
  get|comments|whoami|mine|list|search|history|members|set-state|comment|update|start|create|upload|download) ;;
  *) die "usage: linear.sh get/comments/search/... | comment --issue ENG-1234 (--body <text> | --body-file <path>) | update --issue ENG-1234 [--title <text>] [--description <text>] [--comment <text>] [...] | create --title <text> (--description <text> | --description-file <path>) [...]" ;;
esac

[ -n "${LINEAR_API_KEY:-}" ] || die "LINEAR_API_KEY not set in the environment"
command -v curl >/dev/null 2>&1 || die "curl not found"
command -v jq   >/dev/null 2>&1 || die "jq not found"

# Pass the Authorization header via a 0600 temp file (`-H @file`, curl >= 7.55)
# instead of argv, where `ps` could read the key from the live curl process.
# The file is removed on exit; the key still never reaches stdout/stderr/logs.
AUTH_HDR="$(umask 077 && mktemp "${TMPDIR:-/tmp}/linear-hdr.XXXXXX")" || die "mktemp for auth header failed"
trap 'rm -f "$AUTH_HDR"' EXIT
printf 'Authorization: %s\n' "$LINEAR_API_KEY" >"$AUTH_HDR" || die "writing auth header file failed"

# gql <query> <variables-json> → response body on stdout.
# Dies (exit 2) on transport failure; rejects (exit 3) on GraphQL errors.
gql() {
  local payload resp errs
  payload="$(jq -n --arg q "$1" --argjson v "$2" '{query:$q, variables:$v}')" || die "failed to build payload"
  resp="$(curl -sS --max-time 30 \
    -H "Content-Type: application/json" \
    -H "@$AUTH_HDR" \
    -d "$payload" "$API_URL")" || die "request to $API_URL failed (network/auth?)"
  errs="$(jq -r '[.errors[]?.message] | join("; ")' <<<"$resp" 2>/dev/null)" || die "non-JSON response from $API_URL"
  [ -z "$errs" ] || reject "$errs"
  printf '%s' "$resp"
}

# resolve_issue <key> → snapshots the issue id, state, team, and label ids.
resolve_issue() {
  local resp
  resp="$(gql 'query($id: String!){ issue(id: $id){ id identifier labelIds state { id } team { id } } }' \
              "$(jq -n --arg id "$1" '{id:$id}')")" || exit $?
  ISSUE_UUID="$(jq -r '.data.issue.id // empty' <<<"$resp")"
  ISSUE_IDENTIFIER="$(jq -r '.data.issue.identifier // empty' <<<"$resp")"
  ISSUE_LABEL_IDS="$(jq -c '.data.issue.labelIds // []' <<<"$resp")"
  ISSUE_STATE_ID="$(jq -r '.data.issue.state.id // empty' <<<"$resp")"
  ISSUE_TEAM_ID="$(jq -r '.data.issue.team.id // empty' <<<"$resp")"
  [ -n "$ISSUE_UUID" ] || die "issue '$1' not found"
}

normalize_parent_ref() {
  PARENT_REF="$1"
  case "$PARENT_REF" in
    ENG-[0-9]*)
      printf '%s\n' "$PARENT_REF" | grep -Eq '^ENG-[0-9]+$' || die "--parent must be an ENG key or issue UUID, got '$PARENT_REF'" ;;
    ????????-????-????-????-????????????)
      PARENT_REF="$(printf '%s' "$PARENT_REF" | tr 'A-Z' 'a-z')" ;;
    *) die "--parent must be an ENG key or issue UUID, got '$PARENT_REF'" ;;
  esac
}

# Resolve a parent separately so update's state/label snapshot is untouched.
resolve_parent() {
  local presp
  normalize_parent_ref "$1"
  presp="$(gql 'query ResolveParent($id: String!){ issue(id: $id){ id identifier } }' \
                "$(jq -n --arg id "$PARENT_REF" '{id:$id}')")" || exit $?
  PARENT_UUID="$(jq -r '.data.issue.id // empty' <<<"$presp")"
  PARENT_IDENTIFIER="$(jq -r '.data.issue.identifier // empty' <<<"$presp")"
  [ -n "$PARENT_UUID" ] && [ -n "$PARENT_IDENTIFIER" ] || die "parent issue '$PARENT_REF' not found"
}

# resolve_label_id <name> → sets LABEL_ID (empty when no match). Labels are
# team-scoped or workspace-scoped (team=null). Same-name labels can exist
# across teams, so restrict to this issue's team or workspace, preferring the
# team-scoped match when both exist. Requires resolve_issue to have run.
resolve_label_id() {
  local lresp
  lresp="$(gql 'query($n: String!){ issueLabels(filter:{name:{eqIgnoreCase:$n}}){ nodes { id name team { id } } } }' \
               "$(jq -n --arg n "$1" '{n:$n}')")" || exit $?
  LABEL_ID="$(jq -r --arg tid "$ISSUE_TEAM_ID" '
    [.data.issueLabels.nodes[] | select(.team == null or .team.id == $tid)]
    | (map(select(.team != null)) + map(select(.team == null)))
    | .[0].id // empty' <<<"$lresp")"
}

normalize_state_id() {
  STATE_ID="$(printf '%s' "$1" | tr 'A-Z' 'a-z')"
  case "$STATE_ID" in
    ????????-????-????-????-????????????) ;;
    *) die "--state-id must be a state UUID (see linear-state-ids.md), got '$STATE_ID'" ;;
  esac
}

validate_count() {
  local flag="$1" value="$2" max="$3"
  case "$value" in
    ''|*[!0-9]*) die "$flag must be an integer from 1 to $max" ;;
  esac
  [ "$value" -ge 1 ] && [ "$value" -le "$max" ] || die "$flag must be an integer from 1 to $max"
}

# resolve_assignee <uuid|email> → sets ASSIGNEE_UUID. A UUID passes through
# after a shape check; an email resolves against active workspace users.
resolve_assignee() {
  local target lower uresp
  target="$1"
  lower="$(printf '%s' "$target" | tr 'A-Z' 'a-z')"
  case "$lower" in
    ????????-????-????-????-????????????)
      ASSIGNEE_UUID="$lower"; return 0 ;;
    *@*)
      uresp="$(gql 'query ResolveUserEmail($email: String!){ users(filter: { active: { eq: true }, email: { eqIgnoreCase: $email } }, first: 2){ nodes { id name } } }' \
                   "$(jq -n --arg email "$target" '{email:$email}')")" || exit $?
      case "$(jq -r '.data.users.nodes | length' <<<"$uresp")" in
        0) die "no active user with email '$target' (try linear.sh members --query <name>)" ;;
        1) ASSIGNEE_UUID="$(jq -r '.data.users.nodes[0].id' <<<"$uresp")" ;;
        *) die "email '$target' matched more than one user; pass the user UUID instead" ;;
      esac ;;
    *) die "--assign takes a user UUID or an email, got '$target' (use linear.sh members to look one up)" ;;
  esac
}

validate_inline_text() {
  local flag="$1" value="$2" max="$3"
  [ -n "$value" ] || die "$flag must not be empty"
  [ "${#value}" -le "$max" ] || die "$flag exceeds the $max character limit"
}

create_comment() {
  local body="$1" vars resp ok cid
  validate_inline_text "comment body" "$body" 20000
  vars="$(jq -n --arg id "$ISSUE_UUID" --arg body "$body" '{input:{issueId:$id, body:$body}}')" || die "failed to encode comment body"
  resp="$(gql 'mutation($input: CommentCreateInput!){ commentCreate(input: $input){ success comment { id } } }' "$vars")" || exit $?
  ok="$(jq -r '.data.commentCreate.success // false' <<<"$resp")"
  cid="$(jq -r '.data.commentCreate.comment.id // empty' <<<"$resp")"
  [ "$ok" = "true" ] && [ -n "$cid" ] || reject "commentCreate returned success=$ok"
  COMMENT_ID="$cid"
}

case "$cmd" in

get)
  ISSUE=""; DESCRIPTION_LIMIT="1200"
  while [ $# -gt 0 ]; do
    case "$1" in
      --issue)             [ $# -ge 2 ] || die "--issue needs a value"; ISSUE="$2"; shift 2 ;;
      --description-limit) [ $# -ge 2 ] || die "--description-limit needs a value"; DESCRIPTION_LIMIT="$2"; shift 2 ;;
      *) die "unknown arg: $1" ;;
    esac
  done
  [ -n "$ISSUE" ] || die "--issue <ENG-1234> required"
  validate_count "--description-limit" "$DESCRIPTION_LIMIT" 4000
  resp="$(gql 'query CompactIssue($id: String!){ issue(id: $id){ id identifier url title description priority priorityLabel branchName state { id name type } assignee { id name } labels { nodes { id name } } attachments(first: 50){ nodes { id title url sourceType metadata } pageInfo { hasNextPage } } comments(first: 50, orderBy: createdAt){ nodes { id body } pageInfo { hasNextPage } } } }' \
              "$(jq -n --arg id "$ISSUE" '{id:$id}')")" || exit $?
  [ "$(jq -r '.data.issue.id // empty' <<<"$resp")" ] || die "issue '$ISSUE' not found"
  jq -r --argjson dlimit "$DESCRIPTION_LIMIT" '
    def compact: gsub("[\\r\\n\\t ]+"; " ") | sub("^ "; "") | sub(" $"; "");
    def clipped($n): compact | if length > $n then .[0:$n] + "…" else . end;
    def bit: if . then 1 else 0 end;
    def image_urls: scan("https://uploads\\.linear\\.app/[^)[:space:]]+") | sub("[>\\]\\\"]+$"; "");
    .data.issue as $i
    | ($i.description // "" | compact) as $description
    | ([($i.description // "" | image_urls) as $url
          | {location:"description", comment:"-", url:$url}]
       + [$i.comments.nodes[] as $comment
          | ($comment.body // "" | image_urls) as $url
          | {location:"comment", comment:$comment.id, url:$url}]
       | unique_by([.location, .comment, .url])) as $images
    | "ISSUE=\($i.identifier) URL=\($i.url) TITLE=\($i.title | clipped(240))",
      "STATE_ID=\($i.state.id) STATE_TYPE=\($i.state.type) STATUS=\($i.state.name | clipped(120))",
      "ASSIGNEE_ID=\($i.assignee.id // "none") ASSIGNEE=\(($i.assignee.name // "Unassigned") | clipped(160))",
      "PRIORITY=\($i.priority // 0) PRIORITY_LABEL=\(($i.priorityLabel // "None") | clipped(80))",
      "LABEL_COUNT=\($i.labels.nodes | length) LABELS=\(if ($i.labels.nodes | length) == 0 then "none" else [$i.labels.nodes[].name] | join(",") end)",
      "BRANCH=\($i.branchName // "none")",
      "DESCRIPTION_CHARS=\($description | length) DESCRIPTION_TRUNCATED=\(($description | length > $dlimit) | bit) DESCRIPTION=\($description | clipped($dlimit))",
      "LINK_COUNT=\($i.attachments.nodes | length) LINKS_TRUNCATED=\($i.attachments.pageInfo.hasNextPage | bit)",
      ($i.attachments.nodes[]
        | "LINK ID=\(.id) SOURCE=\(.sourceType // "unknown") BRANCH=\(.metadata.branch // "none") TARGET=\(.metadata.targetBranch // "none") STATUS=\(.metadata.status // "unknown") URL=\(.url) TITLE=\((.title // "Untitled") | clipped(240))"),
      "IMAGE_COUNT=\($images | length) COMMENT_IMAGE_SCAN_COUNT=\($i.comments.nodes | length) COMMENT_IMAGE_SCAN_TRUNCATED=\($i.comments.pageInfo.hasNextPage | bit)",
      ($images[] | "IMAGE LOCATION=\(.location) COMMENT=\(.comment) URL=\(.url)")
  ' <<<"$resp"
  ;;

comments)
  ISSUE=""; LIMIT="10"; BODY_LIMIT="500"
  while [ $# -gt 0 ]; do
    case "$1" in
      --issue)      [ $# -ge 2 ] || die "--issue needs a value"; ISSUE="$2"; shift 2 ;;
      --limit)      [ $# -ge 2 ] || die "--limit needs a value"; LIMIT="$2"; shift 2 ;;
      --body-limit) [ $# -ge 2 ] || die "--body-limit needs a value"; BODY_LIMIT="$2"; shift 2 ;;
      *) die "unknown arg: $1" ;;
    esac
  done
  [ -n "$ISSUE" ] || die "--issue <ENG-1234> required"
  validate_count "--limit" "$LIMIT" 100
  validate_count "--body-limit" "$BODY_LIMIT" 2000
  resp="$(gql 'query CompactComments($id: String!, $limit: Int!){ issue(id: $id){ id identifier comments(first: $limit, orderBy: createdAt){ nodes { id body createdAt updatedAt user { id name } parent { id } } pageInfo { hasNextPage } } } }' \
              "$(jq -n --arg id "$ISSUE" --argjson limit "$LIMIT" '{id:$id, limit:$limit}')")" || exit $?
  [ "$(jq -r '.data.issue.id // empty' <<<"$resp")" ] || die "issue '$ISSUE' not found"
  jq -r --argjson limit "$LIMIT" --argjson blimit "$BODY_LIMIT" '
    def compact: gsub("[\\r\\n\\t ]+"; " ") | sub("^ "; "") | sub(" $"; "");
    def clipped($n): compact | if length > $n then .[0:$n] + "…" else . end;
    def bit: if . then 1 else 0 end;
    .data.issue as $i
    | ($i.comments.nodes | sort_by(.createdAt)) as $comments
    | "ISSUE=\($i.identifier) LIMIT=\($limit) BODY_LIMIT=\($blimit) RETURNED=\($comments | length) MORE=\($i.comments.pageInfo.hasNextPage | bit) ORDER=oldest-first",
      ($comments[]
        | (.body // "" | compact) as $body
        | "COMMENT=\(.id) CREATED=\(.createdAt) UPDATED=\(.updatedAt) PARENT=\(.parent.id // "none") AUTHOR_ID=\(.user.id // "none") AUTHOR=\((.user.name // "Unknown") | clipped(160))",
          "COMMENT_BODY=\(.id) CHARS=\($body | length) TRUNCATED=\(($body | length > $blimit) | bit) BODY=\($body | clipped($blimit))")
  ' <<<"$resp"
  ;;

whoami)
  [ $# -eq 0 ] || die "whoami takes no arguments"
  resp="$(gql 'query{ viewer { id name } }' '{}')" || exit $?
  vid="$(jq -r '.data.viewer.id // empty' <<<"$resp")"
  vname="$(jq -r '.data.viewer.name // "?"' <<<"$resp")"
  [ -n "$vid" ] || die "viewer query returned no id"
  echo "VIEWER=$vid NAME=$vname"
  ;;

mine)
  LIMIT="50"; TEAM="ENG"; STATE_NAMES=()
  while [ $# -gt 0 ]; do
    case "$1" in
      --limit) [ $# -ge 2 ] || die "--limit needs a value"; LIMIT="$2"; shift 2 ;;
      --team)  [ $# -ge 2 ] || die "--team needs a value"; TEAM="$2"; shift 2 ;;
      --state) [ $# -ge 2 ] || die "--state needs a value"; STATE_NAMES+=("$2"); shift 2 ;;
      *) die "unknown arg: $1" ;;
    esac
  done
  validate_count "--limit" "$LIMIT" 100
  case "$TEAM" in
    ''|*[!A-Za-z0-9]*) die "--team must be a team key like ENG, got '$TEAM'" ;;
  esac
  FILTER="$(jq -nc --arg team "$TEAM" '{team:{key:{eq:$team}}}')"
  if [ "${#STATE_NAMES[@]}" -gt 0 ]; then
    names="$(printf '%s\n' "${STATE_NAMES[@]}" | jq -R . | jq -sc .)"
    FILTER="$(jq -c --argjson names "$names" '. + {state:{name:{in:$names}}}' <<<"$FILTER")"
  else
    FILTER="$(jq -c '. + {state:{type:{in:["triage","backlog","unstarted","started"]}}}' <<<"$FILTER")"
  fi
  resp="$(gql 'query MineCompact($filter: IssueFilter, $limit: Int!){ viewer { id name assignedIssues(filter: $filter, first: $limit, orderBy: updatedAt){ nodes { identifier title priority updatedAt state { id name type } labels { nodes { name } } } pageInfo { hasNextPage } } } }' \
              "$(jq -n --argjson filter "$FILTER" --argjson limit "$LIMIT" '{filter:$filter, limit:$limit}')")" || exit $?
  [ "$(jq -r '.data.viewer.id // empty' <<<"$resp")" ] || die "viewer query returned no id"
  jq -r --arg team "$TEAM" '
    def compact: gsub("[\\r\\n\\t ]+"; " ") | sub("^ "; "") | sub(" $"; "");
    def clipped($n): compact | if length > $n then .[0:$n] + "…" else . end;
    def bit: if . then 1 else 0 end;
    # Workday order for $myissues: active work first, Failed QA (by name; its
    # state type is workflow-defined) second, then triage/backlog/unstarted.
    def status_rank:
      (.state.name | ascii_downcase) as $n
      | if $n == "failed qa" then 1
        elif .state.type == "started" then 0
        elif (.state.type == "triage" or .state.type == "backlog" or .state.type == "unstarted") then 2
        else 3 end;
    def prio_rank: if (.priority // 0) == 0 then 5 else .priority end;
    def prio_name: {"0":"None","1":"Urgent","2":"High","3":"Medium","4":"Low"}[(.priority // 0) | tostring] // "None";
    .data.viewer as $v
    # explode|map(-.) inverts the ISO timestamp so ascending sort_by yields
    # most-recently-updated first within equal status and priority.
    | ($v.assignedIssues.nodes
       | sort_by([status_rank, prio_rank, (.updatedAt // "" | explode | map(-.))])) as $issues
    | "VIEWER=\($v.id) TEAM=\($team) RETURNED=\($issues | length) MORE=\($v.assignedIssues.pageInfo.hasNextPage | bit) ORDER=workday NAME=\($v.name | clipped(160))",
      ($issues[]
        | "ISSUE=\(.identifier) PRIORITY=\(.priority // 0) PRIORITY_NAME=\(prio_name) STATE_TYPE=\(.state.type) UPDATED=\(.updatedAt) STATUS=\(.state.name | clipped(80))",
          "ISSUE_TITLE=\(.identifier) TITLE=\(.title | clipped(200))",
          "ISSUE_LABELS=\(.identifier) LABELS=\(if (.labels.nodes | length) == 0 then "none" else [.labels.nodes[].name] | join(",") end)")
  ' <<<"$resp"
  ;;

list)
  LIMIT="50"; TEAM="ENG"; ASSIGNEE=""; LABEL=""; STATE_NAMES=()
  while [ $# -gt 0 ]; do
    case "$1" in
      --limit)    [ $# -ge 2 ] || die "--limit needs a value"; LIMIT="$2"; shift 2 ;;
      --team)     [ $# -ge 2 ] || die "--team needs a value"; TEAM="$2"; shift 2 ;;
      --state)    [ $# -ge 2 ] || die "--state needs a value"; STATE_NAMES+=("$2"); shift 2 ;;
      --assignee) [ $# -ge 2 ] || die "--assignee needs a value"; ASSIGNEE="$2"; shift 2 ;;
      --label)    [ $# -ge 2 ] || die "--label needs a value"; LABEL="$2"; shift 2 ;;
      *) die "unknown arg: $1" ;;
    esac
  done
  validate_count "--limit" "$LIMIT" 100
  case "$TEAM" in
    ''|*[!A-Za-z0-9]*) die "--team must be a team key like ENG, got '$TEAM'" ;;
  esac
  FILTER="$(jq -nc --arg team "$TEAM" '{team:{key:{eq:$team}}}')"
  if [ "${#STATE_NAMES[@]}" -gt 0 ]; then
    names="$(printf '%s\n' "${STATE_NAMES[@]}" | jq -R . | jq -sc .)"
    FILTER="$(jq -c --argjson names "$names" '. + {state:{name:{in:$names}}}' <<<"$FILTER")"
  else
    FILTER="$(jq -c '. + {state:{type:{in:["triage","backlog","unstarted","started"]}}}' <<<"$FILTER")"
  fi
  if [ -n "$ASSIGNEE" ]; then
    lower="$(printf '%s' "$ASSIGNEE" | tr 'A-Z' 'a-z')"
    case "$lower" in
      me)   FILTER="$(jq -c '. + {assignee:{isMe:{eq:true}}}' <<<"$FILTER")" ;;
      none) FILTER="$(jq -c '. + {assignee:{null:true}}' <<<"$FILTER")" ;;
      ????????-????-????-????-????????????)
            FILTER="$(jq -c --arg a "$lower" '. + {assignee:{id:{eq:$a}}}' <<<"$FILTER")" ;;
      *@*)  FILTER="$(jq -c --arg a "$ASSIGNEE" '. + {assignee:{email:{eqIgnoreCase:$a}}}' <<<"$FILTER")" ;;
      *) die "--assignee must be me, none, a user UUID, or an email, got '$ASSIGNEE'" ;;
    esac
  fi
  if [ -n "$LABEL" ]; then
    FILTER="$(jq -c --arg l "$LABEL" '. + {labels:{some:{name:{eqIgnoreCase:$l}}}}' <<<"$FILTER")"
  fi
  resp="$(gql 'query CompactList($filter: IssueFilter, $limit: Int!){ issues(filter: $filter, first: $limit, orderBy: updatedAt){ nodes { identifier title priority updatedAt assignee { id } state { id name type } labels { nodes { name } } } pageInfo { hasNextPage } } }' \
              "$(jq -n --argjson filter "$FILTER" --argjson limit "$LIMIT" '{filter:$filter, limit:$limit}')")" || exit $?
  jq -r --arg team "$TEAM" --argjson filter "$FILTER" '
    def compact: gsub("[\\r\\n\\t ]+"; " ") | sub("^ "; "") | sub(" $"; "");
    def clipped($n): compact | if length > $n then .[0:$n] + "…" else . end;
    def bit: if . then 1 else 0 end;
    def status_rank:
      (.state.name | ascii_downcase) as $n
      | if $n == "failed qa" then 1
        elif .state.type == "started" then 0
        elif (.state.type == "triage" or .state.type == "backlog" or .state.type == "unstarted") then 2
        else 3 end;
    def prio_rank: if (.priority // 0) == 0 then 5 else .priority end;
    def prio_name: {"0":"None","1":"Urgent","2":"High","3":"Medium","4":"Low"}[(.priority // 0) | tostring] // "None";
    .data.issues as $conn
    | ($conn.nodes
       | sort_by([status_rank, prio_rank, (.updatedAt // "" | explode | map(-.))])) as $issues
    | "TEAM=\($team) RETURNED=\($issues | length) MORE=\($conn.pageInfo.hasNextPage | bit) ORDER=workday FILTER=\($filter | tojson)",
      ($issues[]
        | "ISSUE=\(.identifier) PRIORITY=\(.priority // 0) PRIORITY_NAME=\(prio_name) STATE_TYPE=\(.state.type) ASSIGNEE_ID=\(.assignee.id // "none") UPDATED=\(.updatedAt) STATUS=\(.state.name | clipped(80))",
          "ISSUE_TITLE=\(.identifier) TITLE=\(.title | clipped(200))",
          "ISSUE_LABELS=\(.identifier) LABELS=\(if (.labels.nodes | length) == 0 then "none" else [.labels.nodes[].name] | join(",") end)")
  ' <<<"$resp"
  ;;

search)
  LIMIT="25"; TEAM="ENG"; QUERY=""; ASSIGNEE=""; LABEL=""; STATE_NAMES=(); ARCHIVED="false"
  while [ $# -gt 0 ]; do
    case "$1" in
      --query)            [ $# -ge 2 ] || die "--query needs a value"; QUERY="$2"; shift 2 ;;
      --limit)            [ $# -ge 2 ] || die "--limit needs a value"; LIMIT="$2"; shift 2 ;;
      --team)             [ $# -ge 2 ] || die "--team needs a value"; TEAM="$2"; shift 2 ;;
      --state)            [ $# -ge 2 ] || die "--state needs a value"; STATE_NAMES+=("$2"); shift 2 ;;
      --assignee)         [ $# -ge 2 ] || die "--assignee needs a value"; ASSIGNEE="$2"; shift 2 ;;
      --label)            [ $# -ge 2 ] || die "--label needs a value"; LABEL="$2"; shift 2 ;;
      --include-archived) ARCHIVED="true"; shift ;;
      *) die "unknown arg: $1" ;;
    esac
  done
  [ -n "$QUERY" ] || die "--query <text> required"
  validate_count "--limit" "$LIMIT" 100
  case "$TEAM" in
    ''|*[!A-Za-z0-9]*) die "--team must be a team key like ENG (or all), got '$TEAM'" ;;
  esac
  # Search has no default state scope: closed/canceled issues must match so
  # duplicate hunting sees already-fixed work. `--team all` drops the team
  # filter for workspace-wide search.
  TEAM_SCOPE="$(printf '%s' "$TEAM" | tr 'A-Z' 'a-z')"
  if [ "$TEAM_SCOPE" = "all" ]; then
    TEAM="all"; FILTER='{}'
  else
    FILTER="$(jq -nc --arg team "$TEAM" '{team:{key:{eq:$team}}}')"
  fi
  if [ "${#STATE_NAMES[@]}" -gt 0 ]; then
    names="$(printf '%s\n' "${STATE_NAMES[@]}" | jq -R . | jq -sc .)"
    FILTER="$(jq -c --argjson names "$names" '. + {state:{name:{in:$names}}}' <<<"$FILTER")"
  fi
  if [ -n "$ASSIGNEE" ]; then
    lower="$(printf '%s' "$ASSIGNEE" | tr 'A-Z' 'a-z')"
    case "$lower" in
      me)   FILTER="$(jq -c '. + {assignee:{isMe:{eq:true}}}' <<<"$FILTER")" ;;
      none) FILTER="$(jq -c '. + {assignee:{null:true}}' <<<"$FILTER")" ;;
      ????????-????-????-????-????????????)
            FILTER="$(jq -c --arg a "$lower" '. + {assignee:{id:{eq:$a}}}' <<<"$FILTER")" ;;
      *@*)  FILTER="$(jq -c --arg a "$ASSIGNEE" '. + {assignee:{email:{eqIgnoreCase:$a}}}' <<<"$FILTER")" ;;
      *) die "--assignee must be me, none, a user UUID, or an email, got '$ASSIGNEE'" ;;
    esac
  fi
  if [ -n "$LABEL" ]; then
    FILTER="$(jq -c --arg l "$LABEL" '. + {labels:{some:{name:{eqIgnoreCase:$l}}}}' <<<"$FILTER")"
  fi
  resp="$(gql 'query CompactSearch($term: String!, $filter: IssueFilter, $limit: Int!, $archived: Boolean!){ searchIssues(term: $term, filter: $filter, first: $limit, includeArchived: $archived, includeComments: true){ nodes { identifier title priority updatedAt assignee { id } state { id name type } labels { nodes { name } } } pageInfo { hasNextPage } totalCount } }' \
              "$(jq -n --arg term "$QUERY" --argjson filter "$FILTER" --argjson limit "$LIMIT" --argjson archived "$ARCHIVED" '{term:$term, filter:$filter, limit:$limit, archived:$archived}')")" || exit $?
  jq -r --arg team "$TEAM" --arg query "$QUERY" --argjson archived "$ARCHIVED" '
    def compact: gsub("[\\r\\n\\t ]+"; " ") | sub("^ "; "") | sub(" $"; "");
    def clipped($n): compact | if length > $n then .[0:$n] + "…" else . end;
    def bit: if . then 1 else 0 end;
    def prio_name: {"0":"None","1":"Urgent","2":"High","3":"Medium","4":"Low"}[(.priority // 0) | tostring] // "None";
    .data.searchIssues as $conn
    # Keep the API relevance ranking: best match first, no workday re-sort.
    | $conn.nodes as $issues
    | "TEAM=\($team) RETURNED=\($issues | length) TOTAL=\($conn.totalCount // ($issues | length)) MORE=\($conn.pageInfo.hasNextPage | bit) ORDER=relevance ARCHIVED=\($archived | bit) QUERY=\($query | clipped(200))",
      ($issues[]
        | "ISSUE=\(.identifier) PRIORITY=\(.priority // 0) PRIORITY_NAME=\(prio_name) STATE_TYPE=\(.state.type) ASSIGNEE_ID=\(.assignee.id // "none") UPDATED=\(.updatedAt) STATUS=\(.state.name | clipped(80))",
          "ISSUE_TITLE=\(.identifier) TITLE=\(.title | clipped(200))",
          "ISSUE_LABELS=\(.identifier) LABELS=\(if (.labels.nodes | length) == 0 then "none" else [.labels.nodes[].name] | join(",") end)")
  ' <<<"$resp"
  ;;

history)
  ISSUE=""; LIMIT="25"
  while [ $# -gt 0 ]; do
    case "$1" in
      --issue) [ $# -ge 2 ] || die "--issue needs a value"; ISSUE="$2"; shift 2 ;;
      --limit) [ $# -ge 2 ] || die "--limit needs a value"; LIMIT="$2"; shift 2 ;;
      *) die "unknown arg: $1" ;;
    esac
  done
  [ -n "$ISSUE" ] || die "--issue <ENG-1234> required"
  validate_count "--limit" "$LIMIT" 100
  resp="$(gql 'query CompactHistory($id: String!, $limit: Int!){ issue(id: $id){ id identifier history(first: $limit, orderBy: createdAt){ nodes { createdAt actor { id name } fromState { name } toState { name } fromAssignee { id name } toAssignee { id name } fromPriority toPriority } pageInfo { hasNextPage } } } }' \
              "$(jq -n --arg id "$ISSUE" --argjson limit "$LIMIT" '{id:$id, limit:$limit}')")" || exit $?
  [ "$(jq -r '.data.issue.id // empty' <<<"$resp")" ] || die "issue '$ISSUE' not found"
  jq -r --argjson limit "$LIMIT" '
    def compact: gsub("[\\r\\n\\t ]+"; " ") | sub("^ "; "") | sub(" $"; "");
    def clipped($n): compact | if length > $n then .[0:$n] + "…" else . end;
    def bit: if . then 1 else 0 end;
    .data.issue as $i
    | ($i.history.nodes | sort_by(.createdAt)) as $nodes
    # One history node can carry several changes at once; emit one event per
    # tracked change kind and drop nodes with none (title/description edits).
    | [ $nodes[]
        | . as $n
        | ((if ($n.fromState != null or $n.toState != null)
            then [{kind:"state", from_id:"-", to_id:"-",
                   from:($n.fromState.name // "none"), to:($n.toState.name // "none")}] else [] end)
         + (if ($n.fromAssignee != null or $n.toAssignee != null)
            then [{kind:"assignee", from_id:($n.fromAssignee.id // "none"), to_id:($n.toAssignee.id // "none"),
                   from:($n.fromAssignee.name // "Unassigned"), to:($n.toAssignee.name // "Unassigned")}] else [] end)
         + (if ($n.fromPriority != null or $n.toPriority != null)
            then [{kind:"priority", from_id:"-", to_id:"-",
                   from:(($n.fromPriority // 0) | tostring), to:(($n.toPriority // 0) | tostring)}] else [] end))[]
        | . + {created:$n.createdAt, actor_id:($n.actor.id // "none"), actor:($n.actor.name // "System")}
      ] as $events
    | "ISSUE=\($i.identifier) LIMIT=\($limit) SCANNED=\($nodes | length) RETURNED=\($events | length) MORE=\($i.history.pageInfo.hasNextPage | bit) ORDER=oldest-first KINDS=state,assignee,priority",
      ($events | to_entries[]
        | "EVENT=\(.key) CREATED=\(.value.created) KIND=\(.value.kind) FROM_ID=\(.value.from_id) TO_ID=\(.value.to_id) ACTOR_ID=\(.value.actor_id) ACTOR=\(.value.actor | clipped(120))",
          "EVENT_CHANGE=\(.key) CHANGE=\(.value.from | clipped(80)) -> \(.value.to | clipped(80))")
  ' <<<"$resp"
  ;;

members)
  LIMIT="50"; QUERY=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --limit) [ $# -ge 2 ] || die "--limit needs a value"; LIMIT="$2"; shift 2 ;;
      --query) [ $# -ge 2 ] || die "--query needs a value"; QUERY="$2"; shift 2 ;;
      *) die "unknown arg: $1" ;;
    esac
  done
  validate_count "--limit" "$LIMIT" 100
  if [ -n "$QUERY" ]; then
    FILTER="$(jq -nc --arg q "$QUERY" '{active:{eq:true}, or:[{name:{containsIgnoreCase:$q}},{displayName:{containsIgnoreCase:$q}},{email:{containsIgnoreCase:$q}}]}')"
  else
    FILTER='{"active":{"eq":true}}'
  fi
  resp="$(gql 'query CompactMembers($filter: UserFilter, $limit: Int!){ users(filter: $filter, first: $limit){ nodes { id name displayName email } pageInfo { hasNextPage } } }' \
              "$(jq -n --argjson filter "$FILTER" --argjson limit "$LIMIT" '{filter:$filter, limit:$limit}')")" || exit $?
  jq -r --arg query "$QUERY" '
    def compact: gsub("[\\r\\n\\t ]+"; " ") | sub("^ "; "") | sub(" $"; "");
    def clipped($n): compact | if length > $n then .[0:$n] + "…" else . end;
    def bit: if . then 1 else 0 end;
    .data.users as $conn
    | ($conn.nodes | sort_by(.name)) as $users
    | "RETURNED=\($users | length) MORE=\($conn.pageInfo.hasNextPage | bit) ACTIVE_ONLY=1 QUERY=\(if $query == "" then "all" else $query end)",
      ($users[]
        | "USER=\(.id) EMAIL=\(.email // "none") DISPLAY=\(.displayName // "none") NAME=\((.name // "?") | clipped(160))")
  ' <<<"$resp"
  ;;

set-state)
  ISSUE=""; STATE_ID=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --issue)    [ $# -ge 2 ] || die "--issue needs a value"; ISSUE="$2"; shift 2 ;;
      --state-id) [ $# -ge 2 ] || die "--state-id needs a value"; STATE_ID="$2"; shift 2 ;;
      *) die "unknown arg: $1" ;;
    esac
  done
  [ -n "$ISSUE" ] || die "--issue <ENG-1234> required"
  normalize_state_id "$STATE_ID"
  resolve_issue "$ISSUE"
  resp="$(gql 'mutation($id: String!, $input: IssueUpdateInput!){ issueUpdate(id: $id, input: $input){ success issue { state { id name } } } }' \
              "$(jq -n --arg id "$ISSUE_UUID" --arg s "$STATE_ID" '{id:$id, input:{stateId:$s}}')")" || exit $?
  ok="$(jq -r '.data.issueUpdate.success // false' <<<"$resp")"
  [ "$ok" = "true" ] || reject "issueUpdate returned success=false"
  applied="$(jq -r '.data.issueUpdate.issue.state.id // "?"' <<<"$resp")"
  sname="$(jq -r '.data.issueUpdate.issue.state.name // "?"' <<<"$resp")"
  [ "$applied" = "$STATE_ID" ] || reject "state did not take (now '$sname' $applied)"
  echo "ISSUE=$ISSUE STATE_APPLIED=$applied STATE_NAME=$sname"
  ;;

comment)
  ISSUE=""; BODY=""; BODY_FILE=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --issue)     [ $# -ge 2 ] || die "--issue needs a value"; ISSUE="$2"; shift 2 ;;
      --body)      [ $# -ge 2 ] || die "--body needs a value"; BODY="$2"; shift 2 ;;
      --body-file) [ $# -ge 2 ] || die "--body-file needs a value"; BODY_FILE="$2"; shift 2 ;;
      *) die "unknown arg: $1" ;;
    esac
  done
  [ -n "$ISSUE" ] || die "--issue <ENG-1234> required"
  [ -z "$BODY" ] || [ -z "$BODY_FILE" ] || die "--body and --body-file are mutually exclusive"
  if [ -n "$BODY_FILE" ]; then
    [ -f "$BODY_FILE" ] && [ -s "$BODY_FILE" ] || die "--body-file <existing non-empty path> required"
    BODY="$(cat "$BODY_FILE")" || die "failed to read body file"
  fi
  validate_inline_text "--body" "$BODY" 20000
  resolve_issue "$ISSUE"
  create_comment "$BODY"
  echo "ISSUE=$ISSUE COMMENT_ID=$COMMENT_ID"
  ;;

update|start)
  ISSUE=""; TITLE=""; TITLE_FILE=""; DESCRIPTION=""; DESC_FILE=""; ASSIGN_ME=0; ASSIGN=""; PRIORITY=""; STATE_ID=""; COMMENT=""; COMMENT_FILE=""; ADD_LABEL=""; REMOVE_LABEL=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --issue)        [ $# -ge 2 ] || die "--issue needs a value"; ISSUE="$2"; shift 2 ;;
      --title)        [ $# -ge 2 ] || die "--title needs a value"; TITLE="$2"; shift 2 ;;
      --title-file)   [ $# -ge 2 ] || die "--title-file needs a value"; TITLE_FILE="$2"; shift 2 ;;
      --description)  [ $# -ge 2 ] || die "--description needs a value"; DESCRIPTION="$2"; shift 2 ;;
      --description-file) [ $# -ge 2 ] || die "--description-file needs a value"; DESC_FILE="$2"; shift 2 ;;
      --assign-me)    ASSIGN_ME=1; shift ;;
      --assign)       [ $# -ge 2 ] || die "--assign needs a value"; ASSIGN="$2"; shift 2 ;;
      --priority)     [ $# -ge 2 ] || die "--priority needs a value"; PRIORITY="$2"; shift 2 ;;
      --state-id)     [ $# -ge 2 ] || die "--state-id needs a value"; STATE_ID="$2"; shift 2 ;;
      --comment)      [ $# -ge 2 ] || die "--comment needs a value"; COMMENT="$2"; shift 2 ;;
      --comment-file) [ $# -ge 2 ] || die "--comment-file needs a value"; COMMENT_FILE="$2"; shift 2 ;;
      --add-label)    [ $# -ge 2 ] || die "--add-label needs a value"; ADD_LABEL="$2"; shift 2 ;;
      --remove-label) [ $# -ge 2 ] || die "--remove-label needs a value"; REMOVE_LABEL="$2"; shift 2 ;;
      *) die "unknown arg: $1" ;;
    esac
  done
  [ -n "$ISSUE" ] || die "--issue <ENG-1234> required"
  [ -z "$TITLE" ] || [ -z "$TITLE_FILE" ] || die "--title and --title-file are mutually exclusive"
  [ -z "$DESCRIPTION" ] || [ -z "$DESC_FILE" ] || die "--description and --description-file are mutually exclusive"
  [ -z "$COMMENT" ] || [ -z "$COMMENT_FILE" ] || die "--comment and --comment-file are mutually exclusive"
  [ "$ASSIGN_ME" = 0 ] || [ -z "$ASSIGN" ] || die "--assign-me and --assign are mutually exclusive"
  [ "$ASSIGN_ME" = 1 ] || [ -n "$ASSIGN" ] || [ -n "$PRIORITY" ] || [ -n "$STATE_ID" ] || [ -n "$COMMENT" ] || [ -n "$COMMENT_FILE" ] || [ -n "$ADD_LABEL" ] || [ -n "$REMOVE_LABEL" ] || [ -n "$TITLE" ] || [ -n "$TITLE_FILE" ] || [ -n "$DESCRIPTION" ] || [ -n "$DESC_FILE" ] || die "$cmd needs at least one lifecycle/update flag"
  if [ "$cmd" = "start" ]; then
    [ -z "$TITLE" ] && [ -z "$TITLE_FILE" ] && [ -z "$DESCRIPTION" ] && [ -z "$DESC_FILE" ] || die "start does not accept title or description changes"
    [ "$ASSIGN_ME" = 1 ] || die "start requires --assign-me"
    [ -n "$STATE_ID" ] || die "start requires --state-id <uuid>"
    [ -n "$COMMENT" ] || [ -n "$COMMENT_FILE" ] || die "start requires --comment <text> or --comment-file <path>"
  fi
  if [ -n "$ADD_LABEL" ] && [ -n "$REMOVE_LABEL" ] && \
     [ "$(printf '%s' "$ADD_LABEL" | tr 'A-Z' 'a-z')" = "$(printf '%s' "$REMOVE_LABEL" | tr 'A-Z' 'a-z')" ]; then
    die "cannot --add-label and --remove-label the same label"
  fi
  if [ -n "$PRIORITY" ]; then
    case "$PRIORITY" in 0|1|2|3|4) ;; *) die "--priority must be 0-4 (0=None 1=Urgent 2=High 3=Medium 4=Low)" ;; esac
  fi
  if [ -n "$STATE_ID" ]; then
    normalize_state_id "$STATE_ID"
  fi
  if [ -n "$COMMENT_FILE" ]; then
    [ -f "$COMMENT_FILE" ] || die "--comment-file <existing path> required"
    [ -s "$COMMENT_FILE" ] || die "--comment-file is empty: $COMMENT_FILE"
    COMMENT="$(cat "$COMMENT_FILE")" || die "failed to read comment file"
  fi
  TITLE_JSON='null'; DESCRIPTION_JSON='null'; TITLE_CHARS=""; DESCRIPTION_CHARS=""
  if [ -n "$TITLE_FILE" ]; then
    [ -f "$TITLE_FILE" ] || die "--title-file <existing path> required"
    [ -s "$TITLE_FILE" ] || die "--title-file is empty: $TITLE_FILE"
    TITLE_JSON="$(jq -Rs 'sub("\\r?\\n$"; "")' "$TITLE_FILE")" || die "failed to read title file"
    jq -e 'length > 0 and (test("[\\r\\n]") | not)' <<<"$TITLE_JSON" >/dev/null || die "--title-file must contain one non-empty line (an optional final newline is allowed)"
    TITLE_CHARS="$(jq -r 'length' <<<"$TITLE_JSON")"
  elif [ -n "$TITLE" ]; then
    validate_inline_text "--title" "$TITLE" 512
    TITLE_JSON="$(jq -n --arg value "$TITLE" '$value')" || die "failed to encode title"
    jq -e 'test("[\\r\\n]") | not' <<<"$TITLE_JSON" >/dev/null || die "--title must contain one non-empty line"
    TITLE_CHARS="$(jq -r 'length' <<<"$TITLE_JSON")"
  fi
  if [ -n "$DESC_FILE" ]; then
    [ -f "$DESC_FILE" ] || die "--description-file <existing path> required"
    [ -s "$DESC_FILE" ] || die "--description-file is empty: $DESC_FILE"
    DESCRIPTION_JSON="$(jq -Rs '.' "$DESC_FILE")" || die "failed to read description file"
    DESCRIPTION_CHARS="$(jq -r 'length' <<<"$DESCRIPTION_JSON")"
  elif [ -n "$DESCRIPTION" ]; then
    validate_inline_text "--description" "$DESCRIPTION" 100000
    DESCRIPTION_JSON="$(jq -n --arg value "$DESCRIPTION" '$value')" || die "failed to encode description"
    DESCRIPTION_CHARS="$(jq -r 'length' <<<"$DESCRIPTION_JSON")"
  fi
  if [ -n "$COMMENT" ]; then validate_inline_text "--comment" "$COMMENT" 20000; fi
  resolve_issue "$ISSUE"

  input='{}'
  out="ISSUE=$ISSUE"
  if [ -n "$TITLE_FILE" ] || [ -n "$TITLE" ]; then
    input="$(jq -c --argjson t "$TITLE_JSON" '. + {title:$t}' <<<"$input")"
  fi
  if [ -n "$DESC_FILE" ] || [ -n "$DESCRIPTION" ]; then
    input="$(jq -c --argjson d "$DESCRIPTION_JSON" '. + {description:$d}' <<<"$input")"
  fi
  if [ "$ASSIGN_ME" = 1 ]; then
    vresp="$(gql 'query{ viewer { id } }' '{}')" || exit $?
    vid="$(jq -r '.data.viewer.id // empty' <<<"$vresp")"
    [ -n "$vid" ] || die "viewer query returned no id"
    input="$(jq -c --arg a "$vid" '. + {assigneeId:$a}' <<<"$input")"
    out="$out ASSIGNEE=$vid"
  elif [ -n "$ASSIGN" ]; then
    resolve_assignee "$ASSIGN"
    input="$(jq -c --arg a "$ASSIGNEE_UUID" '. + {assigneeId:$a}' <<<"$input")"
    out="$out ASSIGNEE=$ASSIGNEE_UUID"
  fi
  if [ -n "$PRIORITY" ]; then
    input="$(jq -c --argjson p "$PRIORITY" '. + {priority:$p}' <<<"$input")"
    out="$out PRIORITY=$PRIORITY"
  fi
  if [ -n "$STATE_ID" ]; then
    input="$(jq -c --arg s "$STATE_ID" '. + {stateId:$s}' <<<"$input")"
  fi
  if [ -n "$ADD_LABEL" ] || [ -n "$REMOVE_LABEL" ]; then
    # labelIds REPLACES the set: start from the current ids, union the added
    # label, then subtract the removed one.
    labels="$ISSUE_LABEL_IDS"
    if [ -n "$ADD_LABEL" ]; then
      resolve_label_id "$ADD_LABEL"
      [ -n "$LABEL_ID" ] || die "label '$ADD_LABEL' not found for this issue's team or workspace (this script does not create labels)"
      labels="$(jq -c --arg l "$LABEL_ID" '(. + [$l]) | unique' <<<"$labels")"
    fi
    if [ -n "$REMOVE_LABEL" ]; then
      resolve_label_id "$REMOVE_LABEL"
      if [ -n "$LABEL_ID" ]; then
        labels="$(jq -c --arg l "$LABEL_ID" 'map(select(. != $l))' <<<"$labels")"
      fi
    fi
    input="$(jq -c --argjson ls "$labels" '. + {labelIds:$ls}' <<<"$input")"
  fi

  verify_error=""
  if [ "$input" != '{}' ]; then
    resp="$(gql 'mutation($id: String!, $input: IssueUpdateInput!){ issueUpdate(id: $id, input: $input){ success issue { identifier title description assignee { id } priority state { id name } labelIds labels { nodes { name } } parent { id identifier } } } }' \
                "$(jq -n --arg id "$ISSUE_UUID" --argjson i "$input" '{id:$id, input:$i}')")" || exit $?
    ok="$(jq -r '.data.issueUpdate.success // false' <<<"$resp")"
    [ "$ok" = "true" ] || reject "issueUpdate returned success=false"
    verify_resp="$resp"
    if [ -n "$TITLE_FILE" ] || [ -n "$TITLE" ] || [ -n "$DESC_FILE" ] || [ -n "$DESCRIPTION" ]; then
      # The mutation payload can be stale or Markdown-normalized. Verify
      # durable content with a separate read that escapes mutation request
      # data-loader state.
      verify_resp="$(gql 'query VerifyUpdatedIssue($id: String!){ issue(id: $id){ identifier title description state { id } labelIds } }' \
                    "$(jq -n --arg id "$ISSUE_UUID" '{id:$id}')")" || exit $?
    fi
    if [ -n "$STATE_ID" ]; then
      applied="$(jq -r '.data.issueUpdate.issue.state.id // "?"' <<<"$resp")"
      sname="$(jq -r '.data.issueUpdate.issue.state.name // "?"' <<<"$resp")"
      [ "$applied" = "$STATE_ID" ] || reject "state did not take (now '$sname' $applied)"
      out="$out STATE_APPLIED=$applied"
    fi
    if [ -n "$ASSIGN" ]; then
      applied_assignee="$(jq -r '.data.issueUpdate.issue.assignee.id // "none"' <<<"$resp")"
      [ "$applied_assignee" = "$ASSIGNEE_UUID" ] || reject "assignee did not take (now $applied_assignee)"
    fi
    if [ -n "$TITLE_FILE" ] || [ -n "$TITLE" ]; then
      if jq -e --argjson expected "$TITLE_JSON" '(.data.issue.title // .data.issueUpdate.issue.title) == $expected' <<<"$verify_resp" >/dev/null; then
        out="$out TITLE_APPLIED=1 TITLE_CHARS=$TITLE_CHARS"
      else
        out="$out TITLE_APPLIED=0 TITLE_CHARS=$TITLE_CHARS"; verify_error="title"
      fi
    fi
    if [ -n "$DESC_FILE" ] || [ -n "$DESCRIPTION" ]; then
      if jq -e --argjson expected "$DESCRIPTION_JSON" '
        def canonical_description:
          if . == null then null
          else gsub("\r\n"; "\n") | gsub("\r"; "\n") | sub("\n+$"; "")
          end;
        ((.data.issue.description // .data.issueUpdate.issue.description) | canonical_description)
          == ($expected | canonical_description)
      ' <<<"$verify_resp" >/dev/null; then
        out="$out DESCRIPTION_APPLIED=1 DESCRIPTION_CHARS=$DESCRIPTION_CHARS"
      else
        out="$out DESCRIPTION_APPLIED=0 DESCRIPTION_CHARS=$DESCRIPTION_CHARS"; verify_error="${verify_error:+$verify_error/}description"
      fi
    fi
    if [ -n "$TITLE_FILE" ] || [ -n "$TITLE" ] || [ -n "$DESC_FILE" ] || [ -n "$DESCRIPTION" ]; then
      if [ -z "$STATE_ID" ]; then
        applied_state="$(jq -r '.data.issue.state.id // .data.issueUpdate.issue.state.id // empty' <<<"$verify_resp")"
        if [ "$applied_state" = "$ISSUE_STATE_ID" ]; then
          out="$out STATE_PRESERVED=1"
        else
          out="$out STATE_PRESERVED=0"; verify_error="${verify_error:+$verify_error/}state"
        fi
      fi
      if [ -z "$ADD_LABEL" ] && [ -z "$REMOVE_LABEL" ]; then
        applied_labels="$(jq -c '(.data.issue.labelIds // .data.issueUpdate.issue.labelIds // []) | sort' <<<"$verify_resp")"
        expected_labels="$(jq -c 'sort' <<<"$ISSUE_LABEL_IDS")"
        if [ "$applied_labels" = "$expected_labels" ]; then
          out="$out LABELS_PRESERVED=1"
        else
          out="$out LABELS_PRESERVED=0"; verify_error="${verify_error:+$verify_error/}labels"
        fi
      fi
    fi
  fi
  if [ -n "$COMMENT" ]; then
    create_comment "$COMMENT"
    out="$out COMMENT_ID=$COMMENT_ID"
  fi
  if [ -n "$ADD_LABEL" ] || [ -n "$REMOVE_LABEL" ]; then
    names="$(jq -r '[.data.issueUpdate.issue.labels.nodes[].name] | join(",")' <<<"$resp")"
    out="$out LABELS=$names"
  elif [ -n "$STATE_ID" ]; then
    out="$out STATE_NAME=$sname"
  fi
  echo "$out"
  [ -z "$verify_error" ] || reject "update read-back mismatch for $verify_error"
  ;;

create)
  TITLE=""; DESCRIPTION=""; DESC_FILE=""; PARENT=""; TEAM_ID="65861c09-dd00-4ace-ab70-bda91f06f929"; STATE_ID="c6ddabe1-23a7-4ade-a65a-d52fccc31af6"; PRIORITY="3"; LABEL_IDS='[]'; LABEL_NAMES=()
  while [ $# -gt 0 ]; do
    case "$1" in
      --title)            [ $# -ge 2 ] || die "--title needs a value"; TITLE="$2"; shift 2 ;;
      --description)      [ $# -ge 2 ] || die "--description needs a value"; DESCRIPTION="$2"; shift 2 ;;
      --description-file) [ $# -ge 2 ] || die "--description-file needs a value"; DESC_FILE="$2"; shift 2 ;;
      --parent)           [ $# -ge 2 ] || die "--parent needs a value"; PARENT="$2"; shift 2 ;;
      --team-id)          [ $# -ge 2 ] || die "--team-id needs a value"; TEAM_ID="$2"; shift 2 ;;
      --state-id)         [ $# -ge 2 ] || die "--state-id needs a value"; STATE_ID="$2"; shift 2 ;;
      --priority)         [ $# -ge 2 ] || die "--priority needs a value"; PRIORITY="$2"; shift 2 ;;
      --label-id)         [ $# -ge 2 ] || die "--label-id needs a value"
                          lid="$(printf '%s' "$2" | tr 'A-Z' 'a-z')"
                          case "$lid" in
                            ????????-????-????-????-????????????) ;;
                            *) die "--label-id must be a label UUID, got '$2'" ;;
                          esac
                          LABEL_IDS="$(jq -c --arg l "$lid" '(. + [$l]) | unique' <<<"$LABEL_IDS")"; shift 2 ;;
      --label)            [ $# -ge 2 ] || die "--label needs a value"; LABEL_NAMES+=("$2"); shift 2 ;;
      *) die "unknown arg: $1" ;;
    esac
  done
  [ -n "$TITLE" ] || die "--title <text> required"
  validate_inline_text "--title" "$TITLE" 512
  case "$TITLE" in *$'\n'*|*$'\r'*) die "--title must contain one line" ;; esac
  [ -z "$DESCRIPTION" ] || [ -z "$DESC_FILE" ] || die "--description and --description-file are mutually exclusive"
  if [ -n "$DESC_FILE" ]; then
    [ -f "$DESC_FILE" ] && [ -s "$DESC_FILE" ] || die "--description-file <existing non-empty path> required"
    DESCRIPTION="$(cat "$DESC_FILE")" || die "failed to read description file"
  fi
  validate_inline_text "--description" "$DESCRIPTION" 100000
  case "$PRIORITY" in 0|1|2|3|4) ;; *) die "--priority must be 0-4 (0=None 1=Urgent 2=High 3=Medium 4=Low)" ;; esac
  normalize_state_id "$STATE_ID"
  TEAM_ID="$(printf '%s' "$TEAM_ID" | tr 'A-Z' 'a-z')"
  case "$TEAM_ID" in
    ????????-????-????-????-????????????) ;;
    *) die "--team-id must be a team UUID, got '$TEAM_ID'" ;;
  esac
  PARENT_UUID=""; PARENT_IDENTIFIER=""
  if [ -n "$PARENT" ]; then
    resolve_parent "$PARENT"
  fi
  # There is no issue yet, so point resolve_label_id's team scope at the
  # create target: team-scoped matches on the target team win, workspace
  # labels (team null) still match.
  ISSUE_TEAM_ID="$TEAM_ID"
  for lname in ${LABEL_NAMES[@]+"${LABEL_NAMES[@]}"}; do
    resolve_label_id "$lname"
    [ -n "$LABEL_ID" ] || die "label '$lname' not found for the target team or workspace (this script does not create labels)"
    LABEL_IDS="$(jq -c --arg l "$LABEL_ID" '(. + [$l]) | unique' <<<"$LABEL_IDS")"
  done
  input="$(jq -n --arg t "$TITLE" --arg d "$DESCRIPTION" --arg team "$TEAM_ID" --arg s "$STATE_ID" --argjson p "$PRIORITY" \
    '{title:$t, description:$d, teamId:$team, stateId:$s, priority:$p}')" || die "failed to encode issue input"
  if [ "$LABEL_IDS" != '[]' ]; then
    input="$(jq -c --argjson ls "$LABEL_IDS" '. + {labelIds:$ls}' <<<"$input")"
  fi
  if [ -n "$PARENT_UUID" ]; then
    input="$(jq -c --arg p "$PARENT_UUID" '. + {parentId:$p}' <<<"$input")"
  fi
  resp="$(gql 'mutation($input: IssueCreateInput!){ issueCreate(input: $input){ success issue { identifier url title description parent { id identifier } state { id name } labelIds labels { nodes { name } } } } }' \
              "$(jq -n --argjson i "$input" '{input:$i}')")" || exit $?
  ok="$(jq -r '.data.issueCreate.success // false' <<<"$resp")"
  key="$(jq -r '.data.issueCreate.issue.identifier // empty' <<<"$resp")"
  url="$(jq -r '.data.issueCreate.issue.url // empty' <<<"$resp")"
  [ "$ok" = "true" ] && [ -n "$key" ] || reject "issueCreate returned success=$ok"
  if ! verify_resp="$(gql 'query VerifyCreatedIssue($id: String!){ issue(id: $id){ identifier title description parent { id identifier } state { id name } labelIds labels { nodes { name } } } }' \
                    "$(jq -n --arg id "$key" '{id:$id}')")"; then
    echo "ISSUE=$key URL=$url"
    reject "created $key but fresh read-back failed"
  fi
  applied="$(jq -r '.data.issue.state.id // "?"' <<<"$verify_resp")"
  sname="$(jq -r '.data.issue.state.name // "?"' <<<"$verify_resp")"
  title_chars="$(jq -n --arg t "$TITLE" '$t | length')"
  description_chars="$(jq -r '.description | length' <<<"$input")"
  title_applied=0; description_applied=0; parent_applied=0; verify_error=""
  if jq -e --arg t "$TITLE" '.data.issue.title == $t' <<<"$verify_resp" >/dev/null; then title_applied=1; else verify_error="title"; fi
  expected_description="$(jq -c '.description' <<<"$input")"
  if jq -e --argjson expected "$expected_description" '
    def canonical_description:
      if . == null then null
      else gsub("\r\n"; "\n") | gsub("\r"; "\n") | sub("\n+$"; "")
      end;
    (.data.issue.description | canonical_description) == ($expected | canonical_description)
  ' <<<"$verify_resp" >/dev/null; then description_applied=1; else verify_error="${verify_error:+$verify_error/}description"; fi
  actual_parent_id="$(jq -r '.data.issue.parent.id // "none"' <<<"$verify_resp")"
  actual_parent_key="$(jq -r '.data.issue.parent.identifier // "none"' <<<"$verify_resp")"
  expected_parent_id="${PARENT_UUID:-none}"
  expected_parent_key="${PARENT_IDENTIFIER:-none}"
  if [ "$actual_parent_id" = "$expected_parent_id" ] && [ "$actual_parent_key" = "$expected_parent_key" ]; then parent_applied=1; else verify_error="${verify_error:+$verify_error/}parent"; fi
  out="ISSUE=$key URL=$url STATE_APPLIED=$applied PRIORITY=$PRIORITY TITLE_APPLIED=$title_applied TITLE_CHARS=$title_chars DESCRIPTION_APPLIED=$description_applied DESCRIPTION_CHARS=$description_chars PARENT_APPLIED=$parent_applied PARENT=$actual_parent_key PARENT_ID=$actual_parent_id"
  if [ "$LABEL_IDS" != '[]' ]; then
    expected_labels="$(jq -c 'sort' <<<"$LABEL_IDS")"
    actual_labels="$(jq -c '.data.issue.labelIds // [] | sort' <<<"$verify_resp")"
    if [ "$expected_labels" = "$actual_labels" ]; then labels_applied=1; else labels_applied=0; verify_error="${verify_error:+$verify_error/}labels"; fi
    names="$(jq -r '[.data.issue.labels.nodes[].name] | join(",")' <<<"$verify_resp")"
    out="$out LABELS_APPLIED=$labels_applied LABELS=$names"
  else
    out="$out STATE_NAME=$sname"
  fi
  # On a state mismatch the issue already exists — emit the parseable
  # ISSUE=/URL= line (with the actual applied state) before failing loudly,
  # so the caller never orphans the created issue.
  echo "$out"
  [ "$applied" = "$STATE_ID" ] || reject "created $key but requested state did not take (now '$sname' $applied)"
  [ -z "$verify_error" ] || reject "created $key but read-back mismatched $verify_error"
  ;;

upload)
  FILE=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --file) [ $# -ge 2 ] || die "--file needs a value"; FILE="$2"; shift 2 ;;
      *) die "unknown arg: $1" ;;
    esac
  done
  [ -n "$FILE" ] && [ -f "$FILE" ] || die "--file <existing path> required"
  [ -s "$FILE" ] || die "--file is empty: $FILE"
  size="$(wc -c < "$FILE" | tr -d '[:space:]')"
  fname="$(basename "$FILE")"
  case "$fname" in
    *.png)        ctype="image/png" ;;
    *.jpg|*.jpeg) ctype="image/jpeg" ;;
    *.gif)        ctype="image/gif" ;;
    *.webp)       ctype="image/webp" ;;
    *.mp4)        ctype="video/mp4" ;;
    *.webm)       ctype="video/webm" ;;
    *.mov)        ctype="video/quicktime" ;;
    *) ctype="$(file --brief --mime-type "$FILE" 2>/dev/null)" || ctype=""
       [ -n "$ctype" ] || ctype="application/octet-stream" ;;
  esac
  resp="$(gql 'mutation($ct: String!, $fn: String!, $sz: Int!){ fileUpload(contentType: $ct, filename: $fn, size: $sz){ success uploadFile { uploadUrl assetUrl headers { key value } } } }' \
              "$(jq -n --arg ct "$ctype" --arg fn "$fname" --argjson sz "$size" '{ct:$ct, fn:$fn, sz:$sz}')")" || exit $?
  ok="$(jq -r '.data.fileUpload.success // false' <<<"$resp")"
  upload_url="$(jq -r '.data.fileUpload.uploadFile.uploadUrl // empty' <<<"$resp")"
  asset_url="$(jq -r '.data.fileUpload.uploadFile.assetUrl // empty' <<<"$resp")"
  [ "$ok" = "true" ] && [ -n "$upload_url" ] && [ -n "$asset_url" ] || reject "fileUpload returned success=$ok"
  # The signed PUT must carry the mutation's returned headers plus the exact
  # contentType from the request; Linear's docs also require this Cache-Control.
  hdr_args=()
  while IFS=$'\t' read -r k v; do
    [ -n "$k" ] && hdr_args+=( -H "$k: $v" )
  done < <(jq -r '.data.fileUpload.uploadFile.headers[]? | [.key, .value] | @tsv' <<<"$resp")
  code="$(curl -sS --max-time 120 -o /dev/null -w '%{http_code}' -X PUT \
    -H "Content-Type: $ctype" \
    -H "Cache-Control: public, max-age=31536000" \
    ${hdr_args[@]+"${hdr_args[@]}"} \
    --data-binary @"$FILE" "$upload_url")" || die "PUT to signed upload URL failed (network?)"
  case "$code" in
    2??) ;;
    *) reject "signed upload PUT returned HTTP $code" ;;
  esac
  echo "SIZE=$size ASSET_URL=$asset_url FILE=$fname"
  ;;

download)
  URL=""; OUT=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --url) [ $# -ge 2 ] || die "--url needs a value"; URL="$2"; shift 2 ;;
      --out) [ $# -ge 2 ] || die "--out needs a value"; OUT="$2"; shift 2 ;;
      *) die "unknown arg: $1" ;;
    esac
  done
  [ -n "$URL" ] || die "--url <uploads.linear.app asset url> required"
  [ -n "$OUT" ] || die "--out <path> required"
  case "$URL" in
    https://uploads.linear.app/*) ;;
    *) die "--url must start with https://uploads.linear.app/ (the API key is sent only there)" ;;
  esac
  # -L follows Linear's redirect to the signed storage URL; curl drops the
  # Authorization header on the cross-host hop, which is what we want.
  code="$(curl -sSL --max-time 120 -o "$OUT" -w '%{http_code}' \
    -H "@$AUTH_HDR" "$URL")" || die "GET from uploads.linear.app failed (network?)"
  case "$code" in
    2??) ;;
    *) rm -f "$OUT"; reject "asset GET returned HTTP $code" ;;
  esac
  [ -s "$OUT" ] || { rm -f "$OUT"; reject "asset GET returned an empty body"; }
  size="$(wc -c < "$OUT" | tr -d '[:space:]')"
  ctype="$(file --brief --mime-type "$OUT" 2>/dev/null)" || ctype="unknown"
  echo "SIZE=$size CONTENT_TYPE=$ctype FILE=$OUT"
  ;;
esac
