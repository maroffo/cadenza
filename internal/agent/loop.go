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
	// Handler receives the tool_use id: side effects keyed on it stay
	// idempotent across loop retries (e.g. mutation proposals).
	Handler func(ctx context.Context, toolUseID string, input json.RawMessage) (string, error)
}

type Tools map[string]Tool

// Request is one model invocation; tier builders fill it.
// Cheap tier (Haiku): Model/System/UserText/MaxTokens only. Deep tier
// (Opus): Profile becomes the cached prefix block, History carries prior
// turns, Thinking/Effort tune reasoning. Haiku 4.5 rejects thinking and
// would never read a cache, so the zero values keep that tier clean.
type Request struct {
	Model     string
	System    string
	Profile   string // optional second system block; cache breakpoint lands here
	History   []anthropic.MessageParam
	UserText  string
	MaxTokens int
	Cache     bool
	Thinking  bool
	Effort    string // "", "low", "medium", "high", "xhigh", "max"
}

// Usage mirrors the API counters; CacheRead is what proves the prefix
// cache actually hits (decision 10: verify, never assume).
type Usage struct {
	InputTokens   int64
	OutputTokens  int64
	CacheRead     int64
	CacheCreation int64
}

// Result is what a loop run produces. Transcript carries the full exchange
// (user turns, assistant turns, tool results) so conversations persist and
// resume without a signature break.
type Result struct {
	Text       string
	StopReason string
	Transcript []anthropic.MessageParam
	Usage      Usage
}

// Run executes the tool loop.
func Run(ctx context.Context, c Client, req Request, tools Tools) (Result, error) {
	tp, err := toolParams(tools)
	if err != nil {
		return Result{}, err
	}
	system := []anthropic.TextBlockParam{{Text: req.System}}
	if req.Profile != "" {
		profileBlock := anthropic.TextBlockParam{Text: req.Profile}
		if req.Cache {
			// One breakpoint, on the last stable block: everything above it
			// (system + sorted tools + profile) becomes the cached prefix.
			profileBlock.CacheControl = anthropic.NewCacheControlEphemeralParam()
		}
		system = append(system, profileBlock)
	} else if req.Cache {
		system[0].CacheControl = anthropic.NewCacheControlEphemeralParam()
	}

	messages := make([]anthropic.MessageParam, 0, len(req.History)+1)
	messages = append(messages, req.History...)
	messages = append(messages, anthropic.NewUserMessage(anthropic.NewTextBlock(req.UserText)))

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(req.Model),
		MaxTokens: int64(req.MaxTokens),
		System:    system,
		Messages:  messages,
		Tools:     tp,
	}
	if req.Thinking {
		params.Thinking = anthropic.ThinkingConfigParamUnion{OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{}}
	}
	if req.Effort != "" {
		params.OutputConfig = anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffort(req.Effort)}
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
			usage := Usage{
				InputTokens:   resp.Usage.InputTokens,
				OutputTokens:  resp.Usage.OutputTokens,
				CacheRead:     resp.Usage.CacheReadInputTokens,
				CacheCreation: resp.Usage.CacheCreationInputTokens,
			}
			// The cache claim is verified here, in logs, not assumed (decision 10).
			slog.Info("agent: usage",
				"model", req.Model, "input", usage.InputTokens, "output", usage.OutputTokens,
				"cache_read", usage.CacheRead, "cache_creation", usage.CacheCreation)
			return Result{
				Text:       text,
				StopReason: string(resp.StopReason),
				Transcript: params.Messages,
				Usage:      usage,
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
		out, err := tool.Handler(ctx, tu.ID, tu.Input)
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
		var schema map[string]any
		if err := json.Unmarshal(t.Schema, &schema); err != nil {
			// Schemas are developer-authored constants: a malformed one must
			// fail loudly, not register a tool the model free-forms inputs for.
			return nil, fmt.Errorf("agent: tool %q schema: %w", name, err)
		}
		var required []string
		if rs, ok := schema["required"].([]any); ok {
			for _, r := range rs {
				if str, ok := r.(string); ok {
					required = append(required, str)
				}
			}
		}
		// Everything beyond properties/required ($defs, additionalProperties,
		// oneOf refs) rides through ExtraFields: dropping them would hand the
		// model an unconstrained schema with dangling $ref.
		extra := map[string]any{}
		for k, v := range schema {
			switch k {
			case "properties", "required", "type":
			default:
				extra[k] = v
			}
		}
		tp := anthropic.ToolParam{
			Name:        name,
			Description: anthropic.String(t.Description),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties:  schema["properties"],
				Required:    required,
				ExtraFields: extra,
			},
		}
		out = append(out, anthropic.ToolUnionParam{OfTool: &tp})
	}
	return out, nil
}
