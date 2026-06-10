// ABOUTME: Telegram sender: code-owned verdict footer, HTML mode, plain-text fallback, chunking.
// ABOUTME: The model can argue with the verdict in prose but can never suppress this block.

package telegram

import (
	"context"
	"fmt"
	"html"
	"regexp"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/maroffo/cadenza/internal/verdict"
)

type Sender struct {
	b      *bot.Bot
	chatID int64
}

func NewSender(b *bot.Bot, chatID int64) *Sender {
	return &Sender{b: b, chatID: chatID}
}

// SendWithVerdict appends the deterministic verdict block to the body and
// sends. Every coaching message goes through here so the verdict footer is
// structurally impossible to omit.
func (s *Sender) SendWithVerdict(ctx context.Context, body string, v verdict.Verdict) error {
	return s.Send(ctx, body+"\n\n"+verdict.RenderBlock(v))
}

// Send delivers an HTML-mode message, chunked under the 4096 limit, with a
// plain-text retry when Telegram rejects the entity parse: a mangled tag
// must degrade the formatting, never lose the message.
func (s *Sender) Send(ctx context.Context, text string) error {
	for _, chunk := range SplitMessage(text) {
		_, err := s.b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:    s.chatID,
			Text:      chunk,
			ParseMode: models.ParseModeHTML,
		})
		if err == nil {
			continue
		}
		if !isParseError(err) {
			return fmt.Errorf("telegram send: %w", err)
		}
		if _, err := s.b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: s.chatID,
			Text:   stripTags(chunk),
		}); err != nil {
			return fmt.Errorf("telegram plain fallback: %w", err)
		}
	}
	return nil
}

func isParseError(err error) bool {
	return strings.Contains(err.Error(), "can't parse entities")
}

var tagRe = regexp.MustCompile(`</?[a-zA-Z][^>]*>`)

func stripTags(s string) string {
	return html.UnescapeString(tagRe.ReplaceAllString(s, ""))
}
