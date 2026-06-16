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

type Anthropic struct {
	Model string
}

func NewAnthropic() *Anthropic {
	return &Anthropic{Model: "claude-3-7-sonnet-20250219"}
}

func (a *Anthropic) Name() string { return "anthropic" }

type message struct {
	Role    string        `json:"role"`
	Content []interface{} `json:"content"`
}

type textContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolUseContent struct {
	Type  string                 `json:"type"`
	ID    string                 `json:"id"`
	Name  string                 `json:"name"`
	Input map[string]interface{} `json:"input"`
}

type toolResultContent struct {
	Type     string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
	Content  string `json:"content"`
}

type anthropicRequest struct {
	Model       string        `json:"model"`
	MaxTokens   int           `json:"max_tokens"`
	Messages    []message     `json:"messages"`
	System      string        `json:"system,omitempty"`
	Tools       []interface{} `json:"tools,omitempty"`
}

type anthropicResponse struct {
	Content []struct {
		Type  string                 `json:"type"`
		Text  string                 `json:"text,omitempty"`
		ID    string                 `json:"id,omitempty"`
		Name  string                 `json:"name,omitempty"`
		Input map[string]interface{} `json:"input,omitempty"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func (a *Anthropic) Run(ctx context.Context, task Task) (Result, error) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return Result{}, fmt.Errorf("ANTHROPIC_API_KEY is required")
	}

	system := "You are an autonomous AI coding agent. You can execute bash commands in the workspace using the 'bash' tool. Work iteratively to implement the task or output a decomposition. When finished, use the 'finish' tool."
	prompt := BuildPrompt(task)

	messages := []message{
		{
			Role: "user",
			Content: []interface{}{
				textContent{Type: "text", Text: prompt},
			},
		},
	}

	tools := []interface{}{
		map[string]interface{}{
			"name":        "bash",
			"description": "Execute a bash command in the worktree. You can use this to explore files, edit files (via sed/cat/echo/patch), run tests, and compile code.",
			"input_schema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"command": map[string]interface{}{
						"type":        "string",
						"description": "The bash command to run.",
					},
				},
				"required": []string{"command"},
			},
		},
		map[string]interface{}{
			"name":        "finish",
			"description": "Call this tool when you have finished the task. Provide a brief summary of what you did.",
			"input_schema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"summary": map[string]interface{}{
						"type":        "string",
						"description": "Summary of changes made, or decomposition JSON.",
					},
				},
				"required": []string{"summary"},
			},
		},
	}

	client := &http.Client{Timeout: 5 * time.Minute}
	totalInputTokens := 0
	totalOutputTokens := 0
	var finalSummary string
	var trace strings.Builder

	// Run up to 15 iterations
	for i := 0; i < 15; i++ {
		reqBody := anthropicRequest{
			Model:     a.Model,
			MaxTokens: 4096,
			Messages:  messages,
			System:    system,
			Tools:     tools,
		}

		b, err := json.Marshal(reqBody)
		if err != nil {
			return Result{}, err
		}

		req, err := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(b))
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

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != 200 {
			return Result{}, fmt.Errorf("anthropic api error: status %d: %s", resp.StatusCode, string(body))
		}

		var parsed anthropicResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return Result{}, err
		}

		totalInputTokens += parsed.Usage.InputTokens
		totalOutputTokens += parsed.Usage.OutputTokens

		var assistantContent []interface{}
		var toolResults []interface{}
		finished := false

		for _, c := range parsed.Content {
			if c.Type == "text" {
				assistantContent = append(assistantContent, textContent{Type: "text", Text: c.Text})
				trace.WriteString(c.Text + "\n")
			} else if c.Type == "tool_use" {
				assistantContent = append(assistantContent, toolUseContent{
					Type:  "tool_use",
					ID:    c.ID,
					Name:  c.Name,
					Input: c.Input,
				})

				if c.Name == "finish" {
					if sum, ok := c.Input["summary"].(string); ok {
						finalSummary = sum
						trace.WriteString(fmt.Sprintf("\n[tool: finish] %s\n", sum))
					}
					finished = true
					break
				} else if c.Name == "bash" {
					cmdStr, _ := c.Input["command"].(string)
					trace.WriteString(fmt.Sprintf("\n[tool: bash] %s\n", cmdStr))

					cmd := exec.CommandContext(ctx, "bash", "-c", cmdStr)
					cmd.Dir = task.Worktree
					out, err := cmd.CombinedOutput()
					
					resultStr := string(out)
					if err != nil {
						resultStr += fmt.Sprintf("\nError: %v", err)
					}
					if len(resultStr) > 4000 {
						resultStr = resultStr[:4000] + "... (truncated)"
					}
					if resultStr == "" {
						resultStr = "Command executed successfully with no output."
					}
					trace.WriteString(fmt.Sprintf("[result]\n%s\n", resultStr))

					toolResults = append(toolResults, toolResultContent{
						Type:      "tool_result",
						ToolUseID: c.ID,
						Content:   resultStr,
					})
				}
			}
		}

		if len(assistantContent) > 0 {
			messages = append(messages, message{Role: "assistant", Content: assistantContent})
		}

		if finished {
			break
		}

		if len(toolResults) > 0 {
			messages = append(messages, message{Role: "user", Content: toolResults})
		} else {
			// No tools called and not finished; force it to think or finish
			messages = append(messages, message{
				Role: "user",
				Content: []interface{}{
					textContent{Type: "text", Text: "You did not call any tools. Please use the 'bash' tool to perform work, or the 'finish' tool if you are done."},
				},
			})
		}
	}

	traceStr := trace.String()

	return Result{
		Trace:    traceStr,
		Summary:  finalSummary,
		Subtasks: parseSubtasks(traceStr),
		Tokens:   totalInputTokens + totalOutputTokens,
		Model:    a.Model,
	}, nil
}
