package upstream

import (
	"strings"

	"relaylab/internal/domain"
)

func reasoningValue(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "default" {
		return ""
	}
	return v
}

// ResolveReasoning applies model overrides without inferring capabilities from
// a model name. The explicit value "default" disables a channel default.
func ResolveReasoning(m domain.Model, c domain.Channel) (effort, mode string, budget int) {
	effort, mode, budget = c.ReasoningEffort, c.ThinkingMode, c.ThinkingBudget
	if strings.TrimSpace(m.ReasoningEffort) != "" {
		effort = m.ReasoningEffort
	}
	if strings.TrimSpace(m.ThinkingMode) != "" {
		mode = m.ThinkingMode
	}
	if m.ThinkingBudget != 0 {
		budget = m.ThinkingBudget
	}
	effort, mode = reasoningValue(effort), reasoningValue(mode)
	if reasoningValue(m.ThinkingMode) == "" && strings.TrimSpace(m.ThinkingMode) != "" {
		budget = 0
	} else if mode != "enabled" && strings.TrimSpace(m.ThinkingMode) != "" && m.ThinkingBudget == 0 {
		budget = 0
	}
	return
}

// ValidateReasoning rejects parameters that the selected wire protocol cannot
// represent before an upstream request is dispatched.
func ValidateReasoning(p, effort, thinkingMode string, budget, maxTokens int) error {
	p, err := protocol(p)
	if err != nil {
		return err
	}
	effort, thinkingMode = reasoningValue(effort), reasoningValue(thinkingMode)
	invalid := func(message string) error { return &Error{Code: "invalid_reasoning", Message: message} }
	if budget < 0 {
		return invalid("Thinking budget must not be negative")
	}
	if p == "chat" || p == "responses" {
		switch effort {
		case "", "none", "minimal", "low", "medium", "high", "xhigh", "max":
		default:
			return invalid("Unsupported reasoning effort for the selected OpenAI protocol")
		}
		if thinkingMode != "" || budget != 0 {
			return invalid("Thinking mode and budget require the Anthropic protocol")
		}
		return nil
	}
	switch effort {
	case "", "low", "medium", "high", "xhigh", "max":
	default:
		return invalid("Unsupported reasoning effort for the Anthropic protocol")
	}
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	switch thinkingMode {
	case "", "adaptive", "disabled", "between_tools":
		if budget != 0 {
			return invalid("Thinking budget is only valid with enabled thinking")
		}
	case "enabled":
		if budget < 1024 || budget >= maxTokens {
			return invalid("Enabled thinking requires a budget of at least 1024 and less than max tokens")
		}
	default:
		return invalid("Thinking mode must be default, adaptive, enabled, disabled or between_tools")
	}
	return nil
}
