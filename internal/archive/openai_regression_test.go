package archive

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestOpenAICompletionRegression(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"length", `{"choices":[{"finish_reason":"length","message":{"content":"{}"}}],"usage":{"prompt_tokens":10,"completion_tokens":2048,"total_tokens":2058,"completion_tokens_details":{"reasoning_tokens":2048}}}`, "truncated"},
		{"empty", `{"choices":[{"finish_reason":"stop","message":{"content":""}}],"usage":{"completion_tokens":2048,"completion_tokens_details":{"reasoning_tokens":2048}}}`, "reasoning_tokens"},
		{"no_choices", `{"choices":[],"usage":{"completion_tokens":42}}`, "completion_tokens"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := localModelClassifier{modelID: "fixture", api: "openai", endpointURL: "http://127.0.0.1/v1/chat/completions", client: &http.Client{Transport: syntheticRoundTripper(func(r *http.Request) (*http.Response, error) {
				var request map[string]any
				_ = json.NewDecoder(r.Body).Decode(&request)
				if request["max_tokens"] != float64(2048) {
					t.Errorf("budget = %v, want 2048", request["max_tokens"])
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.body)), Request: r}, nil
			})}}
			var err error
			_, err = c.classifyOpenAI(context.Background(), []byte("synthetic"))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want %s", err, tc.want)
			}
		})
	}
}

type syntheticRoundTripper func(*http.Request) (*http.Response, error)

func (f syntheticRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
