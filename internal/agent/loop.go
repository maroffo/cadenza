// ABOUTME: Hand-rolled Anthropic tool loop: iteration cap, every tool_use answered, no framework.
// ABOUTME: All safety-relevant numbers arrive pre-computed in the prompt; tools are read-only context.

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"slices"
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

// Result is what a loop run produces. Transcript carries the full exchange
// (user turns, assistant turns, tool results) so M5 can persist and resume
// conversations without a signature break.
type Result struct {
	Text       string
	StopReason string
	Transcript []anthropic.MessageParam
}

// Run executes the tool loop.
func Run(ctx context.Context, c Client, req Request, tools Tools) (Result, error) {
	tp, err := toolParams(tools)
	if err != nil {
		return Result{}, err
	}
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(req.Model),
		MaxTokens: int64(req.MaxTokens),
		System:    []anthropic.TextBlockParam{{Text: req.System}},
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(req.UserText))},
		Tools:     tp,
	}

	for range maxIterations {
		resp, err := c.api.Messages.New(ctx, params)
		if err != nil {
			return Result{}, fmt.Errorf("agent: messages: %w", err)
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
			return Result{}, fmt.Errorf("agent: model refused the request")
		default:
			// end_turn or max_tokens: take whatever text we have.
			if resp.StopReason == anthropic.StopReasonMaxTokens {
				// Truncation must be visible: the athlete would otherwise get
				// a mid-sentence message with no signal anywhere.
				slog.Warn("agent: response truncated at max_tokens", "model", req.Model)
			}
			text := collectText(resp)
			if text == "" {
				return Result{}, fmt.Errorf("agent: empty response (stop_reason %s)", resp.StopReason)
			}
			params.Messages = append(params.Messages, resp.ToParam())
			return Result{
				Text:       text,
				StopReason: string(resp.StopReason),
				Transcript: params.Messages,
			}, nil
		}
	}
	return Result{}, fmt.Errorf("agent: iteration cap (%d) reached, aborting loop", maxIterations)
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

// toolParams serializes the registry in SORTED name order: tools sit at
// position zero of the future cache prefix, and a map-ordered list would
// silently invalidate the M5 cache on every call.
func toolParams(tools Tools) ([]anthropic.ToolUnionParam, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	out := make([]anthropic.ToolUnionParam, 0, len(tools))
	for _, name := range slices.Sorted(maps.Keys(tools)) {
		t := tools[name]
		var schema struct {
			Properties any      `json:"properties"`
			Required   []string `json:"required"`
		}
		if err := json.Unmarshal(t.Schema, &schema); err != nil {
			// Schemas are developer-authored constants: a malformed one must
			// fail loudly, not register a tool the model free-forms inputs for.
			return nil, fmt.Errorf("agent: tool %q schema: %w", name, err)
		}
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
	return out, nil
}
