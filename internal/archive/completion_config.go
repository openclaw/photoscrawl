package archive

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type CompletionConfig struct {
	MaxTokens          int    `json:"max_tokens"`
	ReasoningEffort    string `json:"reasoning_effort,omitempty"`
	ReasoningEnabled   *bool  `json:"reasoning_enabled,omitempty"`
	ReasoningMaxTokens int    `json:"reasoning_max_tokens,omitempty"`
}

type openRouterReasoning struct {
	Enabled   *bool `json:"enabled,omitempty"`
	MaxTokens int   `json:"max_tokens,omitempty"`
}

func ResolveCompletionConfig(overrides map[string]string) (CompletionConfig, error) {
	value := func(name string) string {
		if v, ok := overrides[name]; ok {
			return v
		}
		return os.Getenv("PHOTOSCRAWL_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_")))
	}
	config := CompletionConfig{MaxTokens: 2048, ReasoningEffort: value("local-model-reasoning-effort")}
	for _, item := range []struct {
		name   string
		target *int
	}{
		{"local-model-max-tokens", &config.MaxTokens},
		{"local-model-reasoning-max-tokens", &config.ReasoningMaxTokens},
	} {
		if raw := value(item.name); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n <= 0 {
				return CompletionConfig{}, fmt.Errorf("%s must be a positive integer", item.name)
			}
			*item.target = n
		} else if _, ok := overrides[item.name]; ok {
			return CompletionConfig{}, fmt.Errorf("%s must be a positive integer", item.name)
		}
	}
	if raw := value("local-model-reasoning-enabled"); raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return CompletionConfig{}, fmt.Errorf("local-model-reasoning-enabled must be true or false")
		}
		config.ReasoningEnabled = &enabled
	} else if _, ok := overrides["local-model-reasoning-enabled"]; ok {
		return CompletionConfig{}, fmt.Errorf("local-model-reasoning-enabled must be true or false")
	}
	return config.normalized()
}

func (c CompletionConfig) normalized() (CompletionConfig, error) {
	if c.MaxTokens == 0 {
		c.MaxTokens = 2048
	}
	if c.MaxTokens < 0 || c.ReasoningMaxTokens < 0 {
		return c, fmt.Errorf("completion and reasoning token budgets must be positive")
	}
	switch c.ReasoningEffort {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max":
	default:
		return c, fmt.Errorf("unsupported reasoning effort")
	}
	if c.ReasoningEffort != "" && (c.ReasoningEnabled != nil || c.ReasoningMaxTokens != 0) {
		return c, fmt.Errorf("reasoning effort conflicts with reasoning enabled or max tokens")
	}
	if c.ReasoningEnabled != nil && !*c.ReasoningEnabled && c.ReasoningMaxTokens != 0 {
		return c, fmt.Errorf("disabled reasoning conflicts with reasoning max tokens")
	}
	return c, nil
}
