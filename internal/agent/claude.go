package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
)

type claudeBackend struct{}

func (claudeBackend) Name() string     { return "claude" }
func (claudeBackend) Label() string    { return "Claude Code" }
func (claudeBackend) CLI() string      { return "claude" }
func (claudeBackend) CoAuthor() string { return "Claude <noreply@anthropic.com>" }

func (b claudeBackend) Run(ctx context.Context, opts RunOptions) (Usage, error) {
	var env []string
	if opts.UseAgentTeams {
		env = append(os.Environ(), "CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1")
	}

	if opts.MaxTokens > 0 {
		return b.runCapped(ctx, opts, env)
	}

	stdout, stderr, err := runCLICapture(ctx, b.CLI(), claudeArgs(opts), env, opts)

	if usage, result, ok := ParseClaudeJSON(stdout); ok {
		writeRunLog(ctx, opts, result+stderr)
		return usage, err
	}
	writeRunLog(ctx, opts, stdout+stderr)
	return ParseUsage(stdout + "\n" + stderr), err
}

func claudeArgs(opts RunOptions) []string {
	return []string{
		"--dangerously-skip-permissions",
		"--print",
		"--output-format", "json",
		"-p", opts.Prompt,
	}
}

func claudeStreamArgs(opts RunOptions) []string {
	return []string{
		"--dangerously-skip-permissions",
		"--print",
		"--output-format", "stream-json",
		"--verbose",
		"-p", opts.Prompt,
	}
}

type claudeStreamEvent struct {
	Type         string      `json:"type"`
	Subtype      string      `json:"subtype"`
	Result       string      `json:"result"`
	TotalCostUSD float64     `json:"total_cost_usd"`
	Model        string      `json:"model"`
	Usage        claudeUsage `json:"usage"`
	Message      struct {
		Model string      `json:"model"`
		Usage claudeUsage `json:"usage"`
	} `json:"message"`
}

type claudeUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

func (u claudeUsage) total() int64 {
	return u.InputTokens + u.OutputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
}

func (u *claudeUsage) add(other claudeUsage) {
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
	u.CacheCreationInputTokens += other.CacheCreationInputTokens
	u.CacheReadInputTokens += other.CacheReadInputTokens
}

func (u claudeUsage) toUsage(model string) Usage {
	billedInput := u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
	return Usage{
		InputTokens:  billedInput,
		OutputTokens: u.OutputTokens,
		TotalTokens:  u.total(),
		CostUSD: PricesForModel(model).Estimate(
			u.InputTokens, u.OutputTokens, u.CacheCreationInputTokens, u.CacheReadInputTokens),
	}
}

type streamTail struct {
	lines [][]byte
	size  int
}

const streamTailMax = 16 << 10

func (t *streamTail) add(line []byte) {
	l := append([]byte(nil), line...)
	t.lines = append(t.lines, l)
	t.size += len(l)
	for t.size > streamTailMax && len(t.lines) > 1 {
		t.size -= len(t.lines[0])
		t.lines = t.lines[1:]
	}
}

func (t *streamTail) String() string {
	return string(bytes.Join(t.lines, []byte("\n")))
}

func tailWorthy(ev claudeStreamEvent) bool {
	switch ev.Type {
	case "assistant", "user":
		return false
	case "system":
		return ev.Subtype != "init"
	case "result":
		return ev.Result == ""
	}
	return true
}

func (b claudeBackend) runCapped(ctx context.Context, opts RunOptions, env []string) (Usage, error) {
	runCtx, cancel := context.WithCancel(ctx)
	if opts.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
	}
	defer cancel()

	cmd := exec.CommandContext(runCtx, b.CLI(), claudeStreamArgs(opts)...)
	cmd.Dir = opts.Workdir
	if env := childEnv(env, opts); env != nil {
		cmd.Env = env
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return Usage{}, fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return Usage{}, fmt.Errorf("start claude: %w", err)
	}

	var (
		final     Usage
		result    string
		cumUsage  claudeUsage
		cumTokens int64
		model     string
		aborted   bool
		tail      streamTail
	)
	reader := bufio.NewReader(pipe)
	for {
		line, rerr := reader.ReadBytes('\n')
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			var ev claudeStreamEvent
			if json.Unmarshal(trimmed, &ev) != nil {
				tail.add(trimmed)
			} else {
				switch ev.Type {
				case "assistant":
					if ev.Message.Model != "" {
						model = ev.Message.Model
					}
					cumUsage.add(ev.Message.Usage)
					cumTokens = cumUsage.total()
					if !aborted && opts.MaxTokens > 0 && cumTokens >= opts.MaxTokens {
						aborted = true
						cancel()
					}
				case "result":
					in := ev.Usage.InputTokens + ev.Usage.CacheCreationInputTokens + ev.Usage.CacheReadInputTokens
					final = Usage{
						InputTokens:  in,
						OutputTokens: ev.Usage.OutputTokens,
						TotalTokens:  in + ev.Usage.OutputTokens,
						CostUSD:      ev.TotalCostUSD,
					}
					result = ev.Result
				}
				if tailWorthy(ev) {
					tail.add(trimmed)
				}
			}
		}
		if rerr != nil {
			break
		}
	}
	waitErr := cmd.Wait()

	body := result
	if tail.size > 0 {
		body += "\n\n[noctra] non-result stream events:\n" + tail.String()
	}
	if aborted {
		body += fmt.Sprintf("\n\n[noctra] run aborted: per-run token ceiling %d reached (cumulative ~%d)", opts.MaxTokens, cumTokens)
	}
	writeRunLog(ctx, opts, body+stderr.String())

	if final.TotalTokens == 0 && cumTokens > 0 {
		final = cumUsage.toUsage(model)
	} else if final.CostUSD == 0 && cumTokens > 0 {
		final.CostUSD = cumUsage.toUsage(model).CostUSD
	}

	if aborted {
		return final, fmt.Errorf("%w: cumulative ~%d tokens (ceiling %d)", ErrTokenCapExceeded, cumTokens, opts.MaxTokens)
	}
	if waitErr != nil {
		if runCtx.Err() == context.DeadlineExceeded {
			return final, fmt.Errorf("%w: %w", ErrTimedOut, waitErr)
		}
		return final, waitErr
	}
	return final, nil
}

var claudeRateLimitRe = regexp.MustCompile(`(?i)rate.limit|usage.limit|exceeded.*limit|too many requests`)

func (claudeBackend) HasRateLimit(output string) bool {
	return claudeRateLimitRe.MatchString(output)
}
