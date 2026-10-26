// Command livecheck performs one explicit user-authorized upstream check. The
// key is read from stdin and is never accepted as a command-line argument.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"
	"relaylab/internal/domain"
	"relaylab/internal/upstream"
)

type report struct {
	Mode            string       `json:"mode"`
	Success         bool         `json:"success"`
	Model           string       `json:"model,omitempty"`
	Protocol        string       `json:"protocol,omitempty"`
	ReasoningEffort string       `json:"reasoning_effort,omitempty"`
	ReturnedModel   string       `json:"returned_model,omitempty"`
	HTTPStatus      int          `json:"http_status,omitempty"`
	DurationMS      int64        `json:"duration_ms"`
	FirstTextMS     *int64       `json:"first_text_ms,omitempty"`
	Usage           domain.Usage `json:"usage"`
	FinishReason    string       `json:"finish_reason,omitempty"`
	Text            string       `json:"text,omitempty"`
	ModelCount      int          `json:"model_count,omitempty"`
	Models          []string     `json:"models,omitempty"`
	GroupStatus     string       `json:"group_status,omitempty"`
	VerifiedGroup   string       `json:"verified_group,omitempty"`
	GroupMessage    string       `json:"group_message,omitempty"`
	Error           string       `json:"error,omitempty"`
	CheckedAt       time.Time    `json:"checked_at"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	base := flag.String("base-url", "", "user supplied API base address")
	model := flag.String("model", "gpt-6.1-sol", "explicit model")
	protocol := flag.String("protocol", "responses", "chat or responses")
	effort := flag.String("effort", "high", "explicit reasoning effort")
	mode := flag.String("mode", "probe", "discover, group or probe")
	out := flag.String("output", "", "optional redacted report file")
	timeout := flag.Int("timeout", 120, "request timeout seconds")
	flag.Parse()
	if *base == "" {
		return fmt.Errorf("base-url is required")
	}
	var key string
	var err error
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(os.Stderr, "API Key: ")
		var value []byte
		value, err = term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		key = string(value)
		clear(value)
	} else {
		value, e := bufio.NewReader(os.Stdin).ReadString('\n')
		if e != nil && len(value) == 0 {
			return fmt.Errorf("stdin key missing")
		}
		key = strings.TrimSuffix(strings.TrimSuffix(value, "\n"), "\r")
	}
	if err != nil {
		return err
	}
	if len(key) == 0 || len(key) > 4096 {
		return fmt.Errorf("invalid stdin key length")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeout)*time.Second)
	defer cancel()
	client := upstream.New()
	config := upstream.Config{BaseURL: *base, APIKey: key, Protocol: *protocol, DiscoveryProtocol: "openai", ChatTokenField: "max_completion_tokens"}
	r := report{Mode: *mode, Model: *model, Protocol: *protocol, ReasoningEffort: *effort, CheckedAt: time.Now().UTC()}
	started := time.Now()
	if *mode == "discover" {
		r.Models, err = client.ListModels(ctx, config)
		r.ModelCount = len(r.Models)
		r.DurationMS = time.Since(started).Milliseconds()
		r.Success = err == nil
	} else if *mode == "group" {
		var verification upstream.GroupVerification
		verification, err = client.VerifyGroup(ctx, config, "newapi", "", "")
		r.GroupStatus = verification.Status
		r.VerifiedGroup = verification.Group
		r.GroupMessage = verification.Message
		r.DurationMS = time.Since(started).Milliseconds()
		r.Success = err == nil
	} else if *mode == "probe" {
		var result upstream.Result
		result, err = client.Generate(ctx, upstream.Request{Config: config, Model: *model, Prompt: "Reply with exactly OK.", MaxTokens: 4096, Timeout: time.Duration(*timeout) * time.Second, ReasoningEffort: *effort}, nil)
		r.Success = err == nil && result.Complete
		r.ReturnedModel = result.ReturnedModel
		r.HTTPStatus = result.HTTPStatus
		r.DurationMS = result.DurationMS
		r.FirstTextMS = result.FirstTextMS
		r.Usage = result.Usage
		r.FinishReason = result.FinishReason
		r.Text = result.Text
		if len(r.Text) > 160 {
			r.Text = r.Text[:160]
		}
	} else {
		return fmt.Errorf("invalid mode")
	}
	if err != nil {
		r.Error = strings.ReplaceAll(err.Error(), key, "[redacted]")
	}
	encoded, e := json.MarshalIndent(r, "", "  ")
	if e != nil {
		return e
	}
	if *out != "" {
		if e = os.MkdirAll(filepath.Dir(*out), 0700); e != nil {
			return e
		}
		tmp := *out + ".tmp"
		if e = os.WriteFile(tmp, encoded, 0600); e != nil {
			return e
		}
		if e = os.Rename(tmp, *out); e != nil {
			return e
		}
	}
	fmt.Println(string(encoded))
	if !r.Success {
		return fmt.Errorf("live check failed; see redacted report")
	}
	return nil
}
