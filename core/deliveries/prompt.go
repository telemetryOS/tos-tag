// Package deliveries owns normalized Slack result rendering and durable output
// contracts. Transport is provided separately by core/slack.
package deliveries

const SlackOutputContractVersion = "slack-output/v1"

const SlackOutputPrompt = `You are writing a message that will be delivered to Slack.
Return only one JSON object with this exact top-level shape:
{"segments":[{"kind":"mrkdwn_text","text":"message"}]}

Every segment must use the field "kind" with one of: mrkdwn_text, table, or
artifact. Do not use "type" for the segment kind and do not wrap the JSON in a
Markdown code fence.

For mrkdwn_text:
- Use Slack links: <https://example.com|descriptive label>.
- Use *bold*, _italic_, and ~strikethrough~ when they improve scanning.
- Put variable names, ENV names, literal values, commands, flags, paths, model
  names, codes, issue keys, UUIDs, job IDs, and identifiers in single backticks.
- Use triple-backtick blocks for multiline code, commands, logs, or JSON.
- Keep explanatory prose outside code blocks and use short paragraphs and lists.
- Never use GitHub links [label](url), double-asterisk bold, HTML tables, or
  unaligned pipe tables in mrkdwn text.

For comparisons with multiple rows or repeated fields, return a complete table
segment with columns and typed rows. Do not hide the table in prose. The Slack
renderer will create a native Block Kit table. Use a fenced aligned table only
when terminal-style literal formatting is itself meaningful.

Use this exact structure for a table segment:
{"kind":"table","table":{"columns":[{"header":"Check"},{"header":"Result"}],"rows":[[{"type":"raw_text","text":"Build"},{"type":"raw_text","text":"Passed"}]]}}
Valid cell types are raw_text, raw_number, and rich_text. A table segment must
not include a text or artifact field.

Do not choose or alter the Slack channel, thread, recipients, or mentions.`

func WithSlackOutputContract(systemInstructions string) string {
	if systemInstructions == "" {
		return SlackOutputPrompt
	}
	return systemInstructions + "\n\n" + SlackOutputPrompt
}
