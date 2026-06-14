package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	xaiAPIURL        = "https://api.x.ai/v1/chat/completions"
	defaultGrokModel = "grok-3"
	// grokMaxBytes caps total file content sent; grok-3 has 131K-token context.
	// At ~4 chars/token that leaves ~450K for files after prompt overhead.
	grokMaxBytes = 400_000
)

// Grok drives xAI's Grok model via the OpenAI-compatible xAI API.
// It picks the most relevant Python files from the worktree, asks Grok for
// the fix, and writes the changed files back in place. The orchestrator then
// diffs, commits, and gates the result exactly as it does for claudecode.
//
// Requires XAI_API_KEY in the environment.
type Grok struct {
	Model    string
	APIKey   string
	APIURL   string
	MaxBytes int
	// http is injectable for tests; nil uses http.DefaultClient.
	http interface {
		Do(*http.Request) (*http.Response, error)
	}
}

// NewGrok returns a Grok backend wired to the xAI API.
func NewGrok() *Grok {
	return &Grok{
		Model:    defaultGrokModel,
		APIKey:   os.Getenv("XAI_API_KEY"),
		APIURL:   xaiAPIURL,
		MaxBytes: grokMaxBytes,
	}
}

// Name implements Backend.
func (*Grok) Name() string { return "grok" }

// Run implements Backend: gather relevant files, call Grok, apply changes.
func (g *Grok) Run(ctx context.Context, task Task) (Result, error) {
	if g.APIKey == "" {
		return Result{}, fmt.Errorf("grok: XAI_API_KEY is not set")
	}

	files, err := gatherRelevantFiles(task.Worktree, task.Goal+" "+task.Title, g.MaxBytes)
	if err != nil {
		return Result{}, fmt.Errorf("grok: gather files: %w", err)
	}
	if len(files) == 0 {
		return Result{}, fmt.Errorf("grok: no Python source files found in worktree")
	}

	msgs := buildGrokMessages(task, files)
	reply, tokens, err := g.callXAI(ctx, msgs)
	if err != nil {
		return Result{}, fmt.Errorf("grok: API call: %w", err)
	}

	if err := applyGrokChanges(task.Worktree, reply); err != nil {
		return Result{}, fmt.Errorf("grok: apply changes: %w", err)
	}

	return Result{
		Trace:   reply,
		Summary: task.Title,
		Tokens:  tokens,
	}, nil
}

// ── file gathering ────────────────────────────────────────────────────────────

// gatherRelevantFiles walks the worktree and returns a map of relative path →
// content for the most relevant Python files. Files whose module path appears
// in the hint text are boosted; the total is capped at maxBytes.
func gatherRelevantFiles(root, hint string, maxBytes int) (map[string]string, error) {
	wantDirs := moduleHintDirs(hint)

	type entry struct {
		rel   string
		score int
		size  int64
	}
	var all []entry

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			switch name {
			case ".git", "__pycache__", ".tox", ".mypy_cache", "build", "dist", "node_modules":
				return filepath.SkipDir
			}
			if strings.HasSuffix(name, ".egg-info") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".py") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		info, err := d.Info()
		if err != nil {
			return nil
		}

		score := scoreFile(rel, wantDirs, hint)
		all = append(all, entry{rel: rel, score: score, size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(all, func(i, j int) bool {
		if all[i].score != all[j].score {
			return all[i].score > all[j].score
		}
		return all[i].size < all[j].size
	})

	files := make(map[string]string, len(all))
	var total int
	for _, e := range all {
		if total >= maxBytes {
			break
		}
		content, err := os.ReadFile(filepath.Join(root, e.rel))
		if err != nil {
			continue
		}
		if total+len(content) > maxBytes {
			if e.score >= 10 {
				// High-priority file: include truncated rather than skip entirely.
				files[e.rel] = string(content[:maxBytes-total]) + "\n# ... (truncated)"
				total = maxBytes
			}
			continue
		}
		files[e.rel] = string(content)
		total += len(content)
	}
	return files, nil
}

// moduleHintDirs converts dotted module references in hint to directory paths.
// "astropy.modeling.separable" → "astropy/modeling".
func moduleHintDirs(hint string) map[string]bool {
	re := regexp.MustCompile(`\b([a-zA-Z_]\w+(?:\.[a-zA-Z_]\w+)+)\b`)
	dirs := map[string]bool{}
	for _, m := range re.FindAllString(hint, -1) {
		parts := strings.Split(m, ".")
		// directory containing the module
		if len(parts) > 1 {
			dirs[strings.Join(parts[:len(parts)-1], "/")] = true
		}
		// also the exact file path
		dirs[strings.ReplaceAll(m, ".", "/")+".py"] = true
	}
	return dirs
}

func scoreFile(rel string, wantDirs map[string]bool, hint string) int {
	score := 0
	relLower := strings.ToLower(rel)
	// Exact file match
	if wantDirs[rel] {
		score += 15
	}
	// Directory match
	for d := range wantDirs {
		if strings.HasPrefix(relLower, strings.ToLower(d)) {
			score += 8
			break
		}
	}
	// Filename appears in hint (e.g. "test_separable.py" in issue text)
	base := strings.ToLower(filepath.Base(rel))
	if strings.Contains(strings.ToLower(hint), base) || strings.Contains(strings.ToLower(hint), strings.TrimSuffix(base, ".py")) {
		score += 5
	}
	// Penalty for test files unless explicitly referenced
	if strings.Contains(relLower, "/test") && score == 0 {
		score -= 2
	}
	return score
}

// ── prompt construction ───────────────────────────────────────────────────────

type xaiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func buildGrokMessages(task Task, files map[string]string) []xaiMessage {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var user strings.Builder
	if task.Conventions != "" {
		user.WriteString("Project conventions (follow exactly):\n")
		user.WriteString(task.Conventions)
		user.WriteString("\n\n")
	}
	if task.Goal != "" {
		user.WriteString("Issue to fix:\n")
		user.WriteString(task.Goal)
		user.WriteString("\n\n")
	}
	fmt.Fprintf(&user, "Task: %s\n\n", task.Title)
	user.WriteString("Source files:\n\n")
	for _, p := range paths {
		fmt.Fprintf(&user, "=== %s ===\n%s\n\n", p, files[p])
	}
	user.WriteString(`Return each file you need to change using this exact format (complete new file contents — no truncation):

<edit>
<path>relative/path/to/file.py</path>
<content>
complete new file contents here
</content>
</edit>

Multiple files: use multiple <edit> blocks.
Make only the minimal targeted changes needed to fix the issue.
Do not add unrelated refactors or style changes.`)

	return []xaiMessage{
		{
			Role: "system",
			Content: "You are an expert software engineer. You fix bugs by making minimal, " +
				"targeted code changes. Return only changed files in the requested XML format — " +
				"nothing else outside the <edit> blocks.",
		},
		{Role: "user", Content: user.String()},
	}
}

// ── xAI API call ──────────────────────────────────────────────────────────────

type xaiRequest struct {
	Model    string       `json:"model"`
	Messages []xaiMessage `json:"messages"`
}

type xaiResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (g *Grok) callXAI(ctx context.Context, msgs []xaiMessage) (string, int, error) {
	body, err := json.Marshal(xaiRequest{Model: g.Model, Messages: msgs})
	if err != nil {
		return "", 0, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.APIURL, bytes.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+g.APIKey)

	client := g.http
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, fmt.Errorf("read response: %w", err)
	}

	var result xaiResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", 0, fmt.Errorf("decode response: %w", err)
	}
	if result.Error != nil {
		return "", 0, fmt.Errorf("xAI API error: %s", result.Error.Message)
	}
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("xAI API status %d: %s", resp.StatusCode, string(raw))
	}
	if len(result.Choices) == 0 {
		return "", 0, fmt.Errorf("xAI API returned no choices")
	}

	return result.Choices[0].Message.Content, result.Usage.TotalTokens, nil
}

// ── response parsing + file application ───────────────────────────────────────

var editBlockRe = regexp.MustCompile(`(?s)<edit>\s*<path>(.*?)</path>\s*<content>\n?(.*?)\n?</content>\s*</edit>`)

// applyGrokChanges parses <edit> blocks from the model reply and writes each
// file back into the worktree. Unknown or path-traversal paths are skipped.
func applyGrokChanges(root, reply string) error {
	matches := editBlockRe.FindAllStringSubmatch(reply, -1)
	for _, m := range matches {
		relPath := strings.TrimSpace(m[1])
		content := m[2]

		// Guard against path traversal.
		if strings.Contains(relPath, "..") || filepath.IsAbs(relPath) {
			continue
		}
		full := filepath.Join(root, relPath)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", relPath, err)
		}
	}
	return nil
}
