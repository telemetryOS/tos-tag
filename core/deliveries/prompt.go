// Package deliveries owns normalized Slack result rendering and durable output
// contracts. Transport is provided separately by core/slack.
package deliveries

const SlackOutputContractVersion = "slack-output/v1"

const SlackOutputPrompt = `You are writing a message that will be delivered to Slack.
Return ordered typed segments: mrkdwn_text, table, or artifact.

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

Do not choose or alter the Slack channel, thread, recipients, or mentions.`

func WithSlackOutputContract(systemInstructions string) string {
	if systemInstructions == "" {
		return SlackOutputPrompt
	}
	return systemInstructions + "\n\n" + SlackOutputPrompt
}
