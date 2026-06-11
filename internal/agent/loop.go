// ABOUTME: Hand-rolled Anthropic tool loop: iteration cap, every tool_use answered, no framework.
// ABOUTME: All safety-relevant numbers arrive pre-computed in the prompt; tools are read-only context.

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// maxIterations bounds the loop: a model that keeps calling tools past this
// is a bug or a runaway, and every iteration costs real money.
const maxIterations = 10

// Client wraps the SDK client so tests can point it at fakeanthropic.
type Client struct {
	api anthropic.Client
}

// NewClient builds a client; baseURL is overridable for tests and e2e
// (empty means the real API).
func NewClient(apiKey, baseURL string) Client {
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return Client{api: anthropic.NewClient(opts...)}
}

// Tool is a read-only capability exposed to the model. Handlers return the
// payload as a string (pre-trimmed: huge tool results poison the context).
type Tool struct {
	Description string
	Schema      json.RawMessage // full JSON schema of the input object
	Handler     func(ctx context.Context, input json.RawMessage) (string, error)
}

type Tools map[string]Tool

// Request is one model invocation; tier builders fill it.
type Request struct {
	Model     string
	System    string
	UserText  string
	MaxTokens int
}

// Run executes the tool loop and returns the final assistant text.
func Run(ctx context.Context, c Client, req Request, tools Tools) (string, error) {
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(req.Model),
		MaxTokens: int64(req.MaxTokens),
		System:    []anthropic.TextBlockParam{{Text: req.System}},
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(req.UserText))},
		Tools:     toolParams(tools),
	}

	for range maxIterations {
		resp, err := c.api.Messages.New(ctx, params)
		if err != nil {
			return "", fmt.Errorf("agent: messages: %w", err)
		}

		switch resp.StopReason {
		case anthropic.StopReasonToolUse:
			params.Messages = append(params.Messages, resp.ToParam())
			params.Messages = append(params.Messages, anthropic.NewUserMessage(toolResults(ctx, resp, tools)...))
		case anthropic.StopReasonPauseTurn:
			// Long-running turn paused server-side: re-send as-is.
			params.Messages = append(params.Messages, resp.ToParam())
		case anthropic.StopReasonRefusal:
			// Never auto-retry a refusal; the caller falls back deterministically.
			return "", fmt.Errorf("agent: model refused the request")
		default:
			// end_turn or max_tokens: take whatever text we have.
			text := collectText(resp)
			if text == "" {
				return "", fmt.Errorf("agent: empty response (stop_reason %s)", resp.StopReason)
			}
			return text, nil
		}
	}
	return "", fmt.Errorf("agent: iteration cap (%d) reached, aborting loop", maxIterations)
}

func collectText(resp *anthropic.Message) string {
	var b strings.Builder
	for _, block := range resp.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			b.WriteString(t.Text)
		}
	}
	return strings.TrimSpace(b.String())
}

// toolResults answers EVERY tool_use block: a missing tool_result for any id
// makes the API reject the next request, wedging the loop.
func toolResults(ctx context.Context, resp *anthropic.Message, tools Tools) []anthropic.ContentBlockParamUnion {
	var results []anthropic.ContentBlockParamUnion
	for _, block := range resp.Content {
		tu, ok := block.AsAny().(anthropic.ToolUseBlock)
		if !ok {
			continue
		}
		tool, registered := tools[tu.Name]
		if !registered {
			slog.Warn("agent: model called unregistered tool", "tool", tu.Name)
			results = append(results, anthropic.NewToolResultBlock(tu.ID, "unknown tool: "+tu.Name, true))
			continue
		}
		out, err := tool.Handler(ctx, tu.Input)
		if err != nil {
			slog.Warn("agent: tool failed", "tool", tu.Name, "err", err)
			results = append(results, anthropic.NewToolResultBlock(tu.ID, "tool error: "+err.Error(), true))
			continue
		}
		results = append(results, anthropic.NewToolResultBlock(tu.ID, out, false))
	}
	return results
}

func toolParams(tools Tools) []anthropic.ToolUnionParam {
	if len(tools) == 0 {
		return nil
	}
	out := make([]anthropic.ToolUnionParam, 0, len(tools))
	for name, t := range tools {
		var schema struct {
			Properties any      `json:"properties"`
			Required   []string `json:"required"`
		}
		_ = json.Unmarshal(t.Schema, &schema)
		tp := anthropic.ToolParam{
			Name:        name,
			Description: anthropic.String(t.Description),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: schema.Properties,
				Required:   schema.Required,
			},
		}
		out = append(out, anthropic.ToolUnionParam{OfTool: &tp})
	}
	return out
}
