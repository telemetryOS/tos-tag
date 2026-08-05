---
name: tool-curds
description: Reviewed Curds image generation capability for an explicit Slack request.
---

# Curds tool

Use `media.curds/generate` only while following the injected `curds` behavioral
skill and only when the requester explicitly asks to generate an image.

Call `tos_tag_tool` with `skill_names` containing `curds`, `tool_id` set to
`media.curds`, `operation_id` set to `generate`, and exactly three arguments:
the complete image prompt, one supported aspect ratio, and one of `auto`, `low`,
`medium`, or `high`.

The reviewed helper always uses OpenAI `gpt-image-2`, produces one WebP image,
and returns artifact metadata. The Go control plane retains the bytes outside
model context and publishes them only to the current authorized Slack
destination with the final response. Never request or expose an API token,
Slack URL, output path, provider override, arbitrary model, or shell command.
