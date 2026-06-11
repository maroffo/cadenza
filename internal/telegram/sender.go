// ABOUTME: Telegram sender: code-owned verdict footer, HTML mode, plain-text fallback, chunking.
// ABOUTME: The model can argue with the verdict in prose but can never suppress this block.

package telegram

import (
	"context"
	"errors"
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

// AnswerCallback acknowledges a callback query. Mandatory after every tap or
// the client shows a stuck spinner for up to a minute.
func (s *Sender) AnswerCallback(ctx context.Context, callbackID string) error {
	_, err := s.b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: callbackID,
	})
	if err != nil {
		return fmt.Errorf("telegram answer callback: %w", err)
	}
	return nil
}

// SendWithButton sends an HTML message with a single inline button.
// callbackData must stay within Telegram's 64-byte limit.
func (s *Sender) SendWithButton(ctx context.Context, text, buttonLabel, callbackData string) error {
	_, err := s.b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    s.chatID,
		Text:      text,
		ParseMode: models.ParseModeHTML,
		ReplyMarkup: models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{
				{{Text: buttonLabel, CallbackData: callbackData}},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("telegram send with button: %w", err)
	}
	return nil
}

// isParseError matches HTML entity-parse rejections: errors.Is gates on the
// library's typed 400 sentinel, the description match narrows to parse
// failures (other 400s, like a wrong chat id, must NOT trigger a fallback).
func isParseError(err error) bool {
	return errors.Is(err, bot.ErrorBadRequest) &&
		strings.Contains(err.Error(), "can't parse entities")
}

var tagRe = regexp.MustCompile(`</?[a-zA-Z][^>]*>`)

func stripTags(s string) string {
	return html.UnescapeString(tagRe.ReplaceAllString(s, ""))
}

var allowedTagRe = regexp.MustCompile(`^</?(b|i)>$`)

// SanitizeNarrative enforces the model's markup contract in code: only
// <b> and <i> survive; every other tag is stripped. The prompt asks for at
// most one bold, but prompts are wishes and this is the guarantee.
func SanitizeNarrative(s string) string {
	return tagRe.ReplaceAllStringFunc(s, func(tag string) string {
		if allowedTagRe.MatchString(tag) {
			return tag
		}
		return ""
	})
}
