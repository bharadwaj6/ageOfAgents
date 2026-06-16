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

type OpenAI struct {
	name      string
	Model     string
	BaseURL   string
	APIKeyEnv string
}

func NewOpenAI() *OpenAI {
	return &OpenAI{
		name:      "openai",
		Model:     "gpt-4o",
		BaseURL:   "https://api.openai.com/v1/chat/completions",
		APIKeyEnv: "OPENAI_API_KEY",
	}
}

func NewOpenAICompatible(name, model, baseURL, apiKeyEnv string) *OpenAI {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1/chat/completions"
	}
	if apiKeyEnv == "" {
		apiKeyEnv = "OPENAI_API_KEY"
	}
	if model == "" {
		model = "gpt-4o"
	}
	return &OpenAI{name: name, Model: model, BaseURL: baseURL, APIKeyEnv: apiKeyEnv}
}

func (o *OpenAI) Name() string { return o.name }

type openAIMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function functionCall `json:"function"`
}

type functionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIRequest struct {
	Model       string          `json:"model"`
	Messages    []openAIMessage `json:"messages"`
	Tools       []interface{}   `json:"tools,omitempty"`
	Temperature float64         `json:"temperature"`
}

type openAIResponse struct {
	Choices []struct {
		Message openAIMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

func (o *OpenAI) Run(ctx context.Context, task Task) (Result, error) {
	apiKey := os.Getenv(o.APIKeyEnv)
	if apiKey == "" {
		return Result{}, fmt.Errorf("%s is required", o.APIKeyEnv)
	}

	systemPrompt := "You are an autonomous AI coding agent. You can execute bash commands in the workspace using the 'bash' tool. Work iteratively to implement the task or output a decomposition. When finished, use the 'finish' tool."
	userPrompt := BuildPrompt(task)

	messages := []openAIMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	tools := []interface{}{
		map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        "bash",
				"description": "Execute a bash command in the worktree. You can use this to explore files, edit files (via sed/cat/echo/patch), run tests, and compile code.",
				"parameters": map[string]interface{}{
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
		},
		map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        "finish",
				"description": "Call this tool when you have finished the task. Provide a brief summary of what you did.",
				"parameters": map[string]interface{}{
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
		},
	}

	client := &http.Client{Timeout: 5 * time.Minute}
	totalTokens := 0
	var finalSummary string
	var trace strings.Builder

	// Run up to 15 iterations
	for i := 0; i < 15; i++ {
		reqBody := openAIRequest{
			Model:       o.Model,
			Messages:    messages,
			Tools:       tools,
			Temperature: 0.0,
		}

		b, err := json.Marshal(reqBody)
		if err != nil {
			return Result{}, err
		}

		req, err := http.NewRequestWithContext(ctx, "POST", o.BaseURL, bytes.NewReader(b))
		if err != nil {
			return Result{}, err
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Content-Type", "application/json")

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
			return Result{}, fmt.Errorf("openai api error: status %d: %s", resp.StatusCode, string(body))
		}

		var parsed openAIResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return Result{}, err
		}

		if len(parsed.Choices) == 0 {
			return Result{}, fmt.Errorf("openai api error: no choices returned")
		}

		totalTokens += parsed.Usage.TotalTokens
		assistantMsg := parsed.Choices[0].Message

		// Log assistant message
		if assistantMsg.Content != "" {
			trace.WriteString(assistantMsg.Content + "\n")
		}

		messages = append(messages, assistantMsg)

		if len(assistantMsg.ToolCalls) == 0 {
			// No tools called; force it to think or finish
			messages = append(messages, openAIMessage{
				Role:    "user",
				Content: "You did not call any tools. Please use the 'bash' tool to perform work, or the 'finish' tool if you are done.",
			})
			continue
		}

		finished := false

		// Process tool calls
		for _, tc := range assistantMsg.ToolCalls {
			if tc.Type != "function" {
				continue
			}

			var args map[string]interface{}
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				messages = append(messages, openAIMessage{
					Role:       "tool",
					ToolCallID: tc.ID,
					Name:       tc.Function.Name,
					Content:    fmt.Sprintf("Error parsing arguments: %v", err),
				})
				continue
			}

			if tc.Function.Name == "finish" {
				if sum, ok := args["summary"].(string); ok {
					finalSummary = sum
					trace.WriteString(fmt.Sprintf("\n[tool: finish] %s\n", sum))
				}
				finished = true

				// OpenAI requires a tool response for every tool call
				messages = append(messages, openAIMessage{
					Role:       "tool",
					ToolCallID: tc.ID,
					Name:       "finish",
					Content:    "Acknowledged.",
				})
				break
			} else if tc.Function.Name == "bash" {
				cmdStr, ok := args["command"].(string)
				if !ok {
					messages = append(messages, openAIMessage{
						Role:       "tool",
						ToolCallID: tc.ID,
						Name:       tc.Function.Name,
						Content:    "Error: missing or invalid 'command' argument",
					})
					continue
				}
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

				messages = append(messages, openAIMessage{
					Role:       "tool",
					ToolCallID: tc.ID,
					Name:       "bash",
					Content:    resultStr,
				})
			}
		}

		if finished {
			break
		}
	}

	traceStr := trace.String()

	return Result{
		Trace:    traceStr,
		Summary:  finalSummary,
		Subtasks: parseSubtasks(traceStr),
		Tokens:   totalTokens,
		Model:    o.Model,
	}, nil
}
