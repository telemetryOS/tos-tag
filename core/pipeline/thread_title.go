package pipeline

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/RobertWHurst/blackbox"

	"github.com/telemetryos/tos-tag/core/sessions"
	"github.com/telemetryos/tos-tag/types"
)

const (
	defaultSlackThreadTitle  = "Tag request"
	maxSlackThreadTitleRunes = 80
)

var (
	slackThreadTitleMarkupPattern = regexp.MustCompile(`<[^>\r\n]*>`)
	slackThreadTitleURLPattern    = regexp.MustCompile(`(?i)\bhttps?://\S+`)
	slackThreadTitleCodePattern   = regexp.MustCompile("(?s)`[^`]*`")
	slackThreadTitleSecretPattern = regexp.MustCompile(`(?i)\b(?:api[_-]?key|access[_-]?token|auth(?:orization)?|bearer|password|passwd|secret|token)\b\s*[:=]?\s*\S+`)
	slackThreadTitleOpaquePattern = regexp.MustCompile(`\b(?:[A-Za-z0-9_+/=-]{25,}|[0-9]{12,})\b`)
)

func (p *Pipeline) setNewDirectMessageThreadTitle(ctx context.Context, envelope types.SlackEnvelope, session sessions.Session, created bool) {
	if !created || (envelope.ChannelKind != types.SlackChannelKindDirectMessage && !strings.HasPrefix(envelope.ChannelID, "D")) {
		return
	}
	titleCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err := p.deps.Transport.SetThreadTitle(titleCtx, types.SlackThreadTitleRequest{
		TeamID: envelope.TeamID, ChannelID: envelope.ChannelID, ThreadTS: envelope.RootThreadTS(),
		SessionID: session.ID, Title: safeSlackThreadTitle(envelope.Text),
	})
	logContext := blackbox.Ctx{"session_id": session.ID, "channel_id": envelope.ChannelID, "thread_ts": envelope.RootThreadTS()}
	if err != nil {
		logContext["error_type"] = fmt.Sprintf("%T", err)
		p.deps.Logger.WithCtx(logContext).Warn("Slack agent thread title update failed; continuing")
		return
	}
	p.deps.Logger.WithCtx(logContext).Info("Slack agent thread title updated for new direct-message session")
}

func safeSlackThreadTitle(value string) string {
	value = slackThreadTitleCodePattern.ReplaceAllString(value, " ")
	value = slackThreadTitleMarkupPattern.ReplaceAllString(value, " ")
	value = slackThreadTitleURLPattern.ReplaceAllString(value, " ")
	if slackThreadTitleSecretPattern.MatchString(value) || slackThreadTitleOpaquePattern.MatchString(value) {
		return defaultSlackThreadTitle
	}
	value = strings.Map(func(value rune) rune {
		if unicode.IsControl(value) {
			return ' '
		}
		if value == '<' || value == '>' || value == '`' {
			return -1
		}
		return value
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	value = strings.Trim(value, " \t\r\n.,;:!?-—–_\"'")
	if value == "" {
		return defaultSlackThreadTitle
	}
	runes := []rune(value)
	runes[0] = unicode.ToUpper(runes[0])
	if len(runes) <= maxSlackThreadTitleRunes {
		return string(runes)
	}
	short := strings.TrimSpace(string(runes[:maxSlackThreadTitleRunes-1]))
	if boundary := strings.LastIndex(short, " "); boundary >= maxSlackThreadTitleRunes/2 {
		short = short[:boundary]
	}
	short = strings.TrimRight(short, " .,;:!?-—–_")
	if short == "" {
		return defaultSlackThreadTitle
	}
	return short + "…"
}
