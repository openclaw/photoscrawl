package archive

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

type CompletionUsage struct {
	PromptTokens            int `json:"prompt_tokens"`
	CompletionTokens        int `json:"completion_tokens"`
	TotalTokens             int `json:"total_tokens"`
	CompletionTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details,omitempty"`
}

type CompletionDiagnostics struct {
	FinishReason string           `json:"finish_reason,omitempty"`
	Usage        *CompletionUsage `json:"usage,omitempty"`
}

func (d CompletionDiagnostics) wrap(err error) error {
	if err == nil || (d.FinishReason == "" && d.Usage == nil) {
		return err
	}
	text := ""
	if d.FinishReason != "" {
		text = " finish_reason=" + d.FinishReason
	}
	if u := d.Usage; u != nil {
		text += fmt.Sprintf(" prompt_tokens=%d completion_tokens=%d total_tokens=%d", u.PromptTokens, u.CompletionTokens, u.TotalTokens)
		if u.CompletionTokensDetails != nil {
			text += fmt.Sprintf(" reasoning_tokens=%d", u.CompletionTokensDetails.ReasoningTokens)
		}
	}
	return fmt.Errorf("%w;%s", err, text)
}

var sensitiveProviderText = regexp.MustCompile(`(?i)(data:[^\s"'<>]+|bearer\s+[^\s,"'<>]+|(?:sk-|sk_|gh[pousr]_|xox[baprs]-|AIza)[a-z0-9._~+/=-]+|["']?(?:api[_-]?key|token|secret|password|authorization|session(?:[_-]?id)?|cookie|image(?:_url)?|request)["']?\s*[=:]\s*(?:"[^"]*"|'[^']*'|[^\s,;"'<>]+)|[a-z0-9+/=_-]{32,})`)

var providerCookieHeader = regexp.MustCompile(`(?i)(?:set-cookie|cookie)\s*:\s*[^\r\n]*`)

type sanitizedTransportError struct {
	message string
	cause   error
}

func (e *sanitizedTransportError) Error() string { return e.message }
func (e *sanitizedTransportError) Unwrap() error { return e.cause }

func redactModelRequestEcho(message string, request any) string {
	var images []string
	switch typed := request.(type) {
	case openAIChatCompletionRequest:
		for _, message := range typed.Messages {
			for _, part := range message.Content {
				if part.ImageURL != nil {
					images = append(images, part.ImageURL.URL)
					if _, encoded, ok := strings.Cut(part.ImageURL.URL, ","); ok {
						images = append(images, encoded)
					}
				}
			}
		}
	case ollamaGenerateRequest:
		images = typed.Images
	}
	for _, image := range images {
		if image != "" {
			message = strings.ReplaceAll(message, image, "[image omitted]")
		}
	}
	return message
}

func sanitizeProviderMessage(message string) string {
	if start := strings.IndexAny(message, "{["); start >= 0 {
		message = message[:start] + "[echo omitted]"
	}
	message = providerCookieHeader.ReplaceAllString(message, "[redacted cookie]")
	message = sensitiveProviderText.ReplaceAllString(message, "[redacted]")
	message = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, message)
	if len(message) > 512 {
		message = string([]rune(message)[:min(128, len([]rune(message)))]) + " [truncated]"
	}
	return strings.TrimSpace(message)
}

func providerErrorMessage(body []byte) string {
	var envelope struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if json.Unmarshal(body, &envelope) == nil {
		var failure struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(envelope.Error, &failure) == nil && failure.Message != "" {
			return sanitizeProviderMessage(failure.Message)
		}
		var message string
		if json.Unmarshal(envelope.Error, &message) == nil {
			return sanitizeProviderMessage(message)
		}
		return sanitizeProviderMessage(envelope.Message)
	}
	text := strings.TrimSpace(string(body))
	if strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") {
		return "unreadable provider error body"
	}
	return sanitizeProviderMessage(text)
}
