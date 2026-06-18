package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Anthropic drives Claude directly through the Messages API (raw net/http, no
// SDK — the binary stays dependency-light per ADR 006/keep-it-small, mirroring
// the native openai backend). It runs the same bash+finish agentic loop as the
// other native backends inside the task's worktree. Use it when you want
// API-key-native Anthropic access rather than the `claudecode` CLI subprocess.
type Anthropic struct {
	name      string
	Model     string
	BaseURL   string
	APIKeyEnv string
}

// NewAnthropic returns the backend with sensible defaults: the latest Opus
// model and the standard Messages endpoint, authenticated via ANTHROPIC_API_KEY.
func NewAnthropic() *Anthropic {
	return &Anthropic{
		name:      "anthropic",
		Model:     "claude-opus-4-8",
		BaseURL:   "https://api.anthropic.com/v1/messages",
		APIKeyEnv: "ANTHROPIC_API_KEY",
	}
}

// Name implements Backend.
func (a *Anthropic) Name() string { return a.name }

// anthropicMessage is one turn. Content is raw JSON so an assistant turn can be
// echoed back verbatim (preserving block order) and tool results can be built
// as a typed array.
type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []any              `json:"tools,omitempty"`
}

// anthropicBlock is a single content block from a response (text or tool_use).
type anthropicBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

type anthropicResponse struct {
	Content    json.RawMessage `json:"content"`
	StopReason string          `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// toolResultBlock is the user-turn reply to a tool_use block.
type toolResultBlock struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
}

// Run implements Backend: it drives the Messages API with bash + finish tools,
// executing bash in the worktree until the agent calls finish or stops.
func (a *Anthropic) Run(ctx context.Context, task Task) (Result, error) {
	apiKey := os.Getenv(a.APIKeyEnv)
	if apiKey == "" {
		return Result{}, fmt.Errorf("%s is required", a.APIKeyEnv)
	}

	system := "You are an autonomous AI coding agent. You can execute bash commands in the workspace using the 'bash' tool. Work iteratively to implement the task or output a decomposition. When finished, use the 'finish' tool."

	userPrompt, err := json.Marshal(BuildPrompt(task))
	if err != nil {
		return Result{}, err
	}
	messages := []anthropicMessage{{Role: "user", Content: userPrompt}}

	tools := []any{
		map[string]any{
			"name":        "bash",
			"description": "Execute a bash command in the worktree. You can use this to explore files, edit files (via sed/cat/echo/patch), run tests, and compile code.",
			"input_schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "The bash command to run.",
					},
				},
				"required": []string{"command"},
			},
		},
		map[string]any{
			"name":        "finish",
			"description": "Call this tool when you have finished the task. Provide a brief summary of what you did.",
			"input_schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"summary": map[string]any{
						"type":        "string",
						"description": "Summary of changes made, or decomposition JSON.",
					},
				},
				"required": []string{"summary"},
			},
		},
	}

	client := &http.Client{Timeout: 5 * time.Minute}
	totalTokens := 0
	var finalSummary string
	var trace strings.Builder

	// Run up to 15 iterations.
	for i := 0; i < 15; i++ {
		reqBody := anthropicRequest{
			Model:     a.Model,
			MaxTokens: 16384,
			System:    system,
			Messages:  messages,
			Tools:     tools,
		}
		b, err := json.Marshal(reqBody)
		if err != nil {
			return Result{}, err
		}

		req, err := http.NewRequestWithContext(ctx, "POST", a.BaseURL, bytes.NewReader(b))
		if err != nil {
			return Result{}, err
		}
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("content-type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return Result{}, err
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return Result{}, fmt.Errorf("read response body: %w", err)
		}
		if resp.StatusCode != 200 {
			return Result{}, fmt.Errorf("anthropic api error: status %d: %s", resp.StatusCode, string(body))
		}

		var parsed anthropicResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return Result{}, err
		}
		var blocks []anthropicBlock
		if err := json.Unmarshal(parsed.Content, &blocks); err != nil {
			return Result{}, err
		}

		totalTokens += parsed.Usage.InputTokens + parsed.Usage.OutputTokens

		// Echo the assistant turn back verbatim (preserves block order).
		messages = append(messages, anthropicMessage{Role: "assistant", Content: parsed.Content})

		var results []toolResultBlock
		finished := false
		for _, blk := range blocks {
			switch blk.Type {
			case "text":
				if blk.Text != "" {
					trace.WriteString(blk.Text + "\n")
				}
			case "tool_use":
				switch blk.Name {
				case "finish":
					var args struct {
						Summary string `json:"summary"`
					}
					if err := json.Unmarshal(blk.Input, &args); err == nil {
						finalSummary = args.Summary
						trace.WriteString(fmt.Sprintf("\n[tool: finish] %s\n", args.Summary))
					}
					finished = true
					results = append(results, toolResultBlock{Type: "tool_result", ToolUseID: blk.ID, Content: "Acknowledged."})
				case "bash":
					var args struct {
						Command string `json:"command"`
					}
					if err := json.Unmarshal(blk.Input, &args); err != nil || args.Command == "" {
						results = append(results, toolResultBlock{Type: "tool_result", ToolUseID: blk.ID, Content: "Error: missing or invalid 'command' argument"})
						continue
					}
					trace.WriteString(fmt.Sprintf("\n[tool: bash] %s\n", args.Command))

					cmd := exec.CommandContext(ctx, "bash", "-c", args.Command)
					cmd.Dir = task.Worktree
					out, runErr := cmd.CombinedOutput()
					resultStr := string(out)
					if runErr != nil {
						resultStr += fmt.Sprintf("\nError: %v", runErr)
					}
					if len(resultStr) > 4000 {
						resultStr = resultStr[:4000] + "... (truncated)"
					}
					if resultStr == "" {
						resultStr = "Command executed successfully with no output."
					}
					trace.WriteString(fmt.Sprintf("[result]\n%s\n", resultStr))
					results = append(results, toolResultBlock{Type: "tool_result", ToolUseID: blk.ID, Content: resultStr})
				}
			}
		}

		if finished {
			break
		}
		// No tool calls and the model is done: stop.
		if len(results) == 0 {
			break
		}
		// Feed tool results back as the next user turn.
		rb, err := json.Marshal(results)
		if err != nil {
			return Result{}, err
		}
		messages = append(messages, anthropicMessage{Role: "user", Content: rb})
	}

	traceStr := trace.String()
	return Result{
		Trace:    traceStr,
		Summary:  finalSummary,
		Subtasks: parseSubtasks(traceStr),
		Tokens:   totalTokens,
		Model:    a.Model,
	}, nil
}
