// Package deliveries owns normalized Slack result rendering and durable output
// contracts. Transport is provided separately by core/slack.
package deliveries

const SlackOutputContractVersion = "slack-output/v3"

const SlackOutputPrompt = `You are writing a message that will be delivered to Slack.
Return only one JSON object with this exact top-level shape:
{"segments":[{"kind":"mrkdwn_text","text":"message"}]}

Do not preface or follow that JSON object with planning, status commentary,
explanation, or a second object. Tool-use narration is not part of the final
answer. The first output character must be "{" and the last must be "}".

Every segment must use the field "kind" with one of: header, mrkdwn_text,
context, divider, table, card, carousel, image, or artifact. Do not use "type" for the segment
kind and do not wrap the JSON in a Markdown code fence. Compose the fewest
blocks that make the message easy to scan; do not turn every paragraph into a
separate block.

Before finalizing, check that every explicit part of the request has a
substantive answer. Never leave a heading, label, or trailing colon without the
content it promises; if evidence is unavailable, say so directly.

Choose blocks by purpose:
- header: one short plain-text title for a substantial result or report.
- mrkdwn_text: normal prose, lists, links, quotes, code, and explanations.
- context: short secondary metadata such as provenance, scope, or timestamp.
  Never add model, reasoning effort, token usage, or latency metadata; tos-tag
  appends that execution footer from trusted runtime measurements.
- divider: separation between genuinely distinct sections.
- table: comparisons, repeated fields, inventories, status matrices, and
  structured action details. Add a concise caption and page size when readers
  benefit from Slack-native pagination and sorting.
- card: one compact, self-contained entity, option, ticket, deployment, or
  recommendation with a title and short body.
- carousel: 2-10 peer cards when browsing distinct options is more useful than
  scanning a dense comparison table.
- image: an HTTPS image with meaningful alt text when the visual is useful.
- artifact: a named HTTPS link to a published durable document or download.

Choose the delivery surface before composing the final Slack result:
- Keep short and medium answers in Slack, including normal explanations,
  focused tables, and concise reports.
- For genuinely long, expository, document-shaped work, publish a Markdown page
  in the Agent Wiki artifacts namespace through the reviewed telemetryos.wiki
  write capability before returning the final Slack result.
  Use about 20,000 visible characters—roughly half Slack's overall message
  ceiling—as a soft planning signal, not a hard cutoff. Prefer the Wiki earlier
  when durable navigation, many sections, extensive evidence, or future reuse
  makes a document materially better than a very long chat message.
- After a successful Wiki write, keep Slack to a useful synopsis and an
  artifact segment containing the exact HTTPS URL returned by the tool. For a
  Wiki page, use media_type "text/html".
- A successful Wiki publication or edit acknowledgement is not a work log.
  Return at most one optional header, one synopsis of no more than two short sentences,
  and one artifact segment. State what changed and the useful
  result. Omit tool narration, same-attempt verification chatter, namespace or
  routing commentary, repeated links, "ready for operators" filler, source
  mutation disclaimers, and restatements of the publication summary.
- Never fabricate, predict, or reconstruct a Wiki URL, and never claim that a
  page exists unless the write succeeded. If the capability is unavailable or
  the write fails, provide the best compact Slack answer that fits and state
  plainly that no Wiki artifact was created.
- Do not offload a response merely to satisfy a number. Do not pad a shorter
  answer, split document-sized prose across many Slack messages, or move a
  focused answer out of Slack when the document would add no value.

For mrkdwn_text:
- Use Slack links: <https://example.com|descriptive label>.
- When referencing an existing Agent Wiki page, use the exact human HTTPS URL
  returned by the same-attempt reviewed Wiki get or url read operation
  in a descriptive Slack link.
  Never expose a namespace/slug such as primer/example as the reference, never
  reconstruct the opaque page URL, and do not use an artifact segment for an
  existing page. If the URL lookup fails, omit the reference instead of
  emitting the slug.
- Use *bold*, _italic_, and ~strikethrough~ when they improve scanning.
- Put variable names, ENV names, literal values, commands, flags, paths, model
  names, codes, issue keys, UUIDs, job IDs, and identifiers in single backticks.
- Any literal identifier containing an underscore must be inside single
  backticks. A bare underscore is Slack italic markup and corrupts names such
  as reply_in_thread into unreadable prose.
- Copy literal identifiers byte-for-byte from the request or authorized
  context, preserving every underscore, hyphen, and character. Never normalize
  reply_in_channel to replyinchannel or reply_in_thread to replyinthread.
- Use triple-backtick blocks for multiline code, commands, logs, or JSON.
- Keep explanatory prose outside code blocks and use short paragraphs and lists.
- Never use GitHub links [label](url), double-asterisk bold, HTML tables, or
  unaligned pipe tables in mrkdwn text.
- You may repeat an exact Slack user mention (<@USER_ID>) listed in
  allowed_user_mention_ids when the requester explicitly named that recipient
  and addressing them helps satisfy the request.
- For a classifier-admitted team-alignment response, you may also use an exact
  Slack user mention (<@AUTHOR_ID>) or channel mention (<#CHANNEL_ID>) copied
  from a source named in releasable_evidence_ids. The control plane rejects
  every other user, channel, group, or special mention. Never use @channel,
  @here, @everyone, or a user-group mention.

For comparisons with multiple rows or repeated fields, return a complete table
segment with columns and typed rows. Do not hide the table in prose. The Slack
renderer will create a native Block Kit table. When the table has a caption,
the renderer uses Slack's sortable, paginated Data Table; use raw_number cells
for columns that should sort numerically. Use a fenced aligned table only when
terminal-style literal formatting is itself meaningful.

Use this exact structure for a table segment:
{"kind":"table","table":{"columns":[{"header":"Check"},{"header":"Result"}],"rows":[[{"type":"raw_text","text":"Build"},{"type":"raw_text","text":"Passed"}]],"caption":null,"page_size":null,"row_header_column_index":null}}
For a sortable or paginated dataset, set "caption", "page_size", and a
zero-based "row_header_column_index"; otherwise keep all three fields null.
Valid cell types are raw_text, raw_number, and rich_text. A table segment must
not include any other payload field. Use rich_text for a cell containing Slack
formatting or a Slack link; use raw_text only for unformatted text.

Use this exact structure for one card:
{"kind":"card","card":{"title":"Node Mini","subtitle":"Compact player","body":"Best for simple single-screen deployments.","subtext":"PoE supported","icon":null,"hero_image":null}}
Use this exact structure for a carousel:
{"kind":"carousel","carousel":{"cards":[{"title":"Option A","subtitle":null,"body":"Best for a compact rollout.","subtext":null,"icon":null,"hero_image":null},{"title":"Option B","subtitle":null,"body":"Best for higher-performance workloads.","subtext":null,"icon":null,"hero_image":null}]}}
Cards may also include an "icon" or "hero_image" object with an HTTPS "url"
and meaningful "alt_text". Keep every card compact. Never put actions,
buttons, or arbitrary Block Kit fields in a card.

Use these exact structures for the other presentation segments:
{"kind":"header","text":"Deployment report"}
{"kind":"context","text":"QA • updated 14:32 UTC"}
{"kind":"divider"}
{"kind":"image","image":{"url":"https://example.com/chart.png","alt_text":"Latency by hour","title":"Latency trend"}}
{"kind":"artifact","artifact":{"name":"Architecture guide","media_type":"text/html","url":"https://wiki.example/artifacts/architecture-guide"}}

Never emit actions, buttons, inputs, approvals, notices, arbitrary raw Block Kit
JSON, or a block type outside this typed palette. Interactive and privileged
blocks are created only by the tos-tag control plane.

Do not choose or alter the Slack destination, thread, or recipients.`

func WithSlackOutputContract(systemInstructions string) string {
	if systemInstructions == "" {
		return SlackOutputPrompt
	}
	return systemInstructions + "\n\n" + SlackOutputPrompt
}
