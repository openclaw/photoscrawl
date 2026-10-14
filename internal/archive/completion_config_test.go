package archive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompletionConfigEnvironmentAndOverrides(t *testing.T) {
	for _, name := range []string{"MAX_TOKENS", "REASONING_EFFORT", "REASONING_ENABLED", "REASONING_MAX_TOKENS"} {
		t.Setenv("PHOTOSCRAWL_LOCAL_MODEL_"+name, "")
	}
	c, err := ResolveCompletionConfig(nil)
	if err != nil || c.MaxTokens != 2048 {
		t.Fatalf("default = %+v, %v", c, err)
	}
	t.Setenv("PHOTOSCRAWL_LOCAL_MODEL_MAX_TOKENS", "4096")
	c, err = ResolveCompletionConfig(nil)
	if err != nil || c.MaxTokens != 4096 {
		t.Fatalf("env = %+v, %v", c, err)
	}
	for _, raw := range []string{"0", "-1", "oops", "999999999999999999999"} {
		t.Setenv("PHOTOSCRAWL_LOCAL_MODEL_MAX_TOKENS", raw)
		if _, err := ResolveCompletionConfig(nil); err == nil {
			t.Fatalf("accepted env %q", raw)
		}
		c, err = ResolveCompletionConfig(map[string]string{"local-model-max-tokens": "3072"})
		if err != nil || c.MaxTokens != 3072 {
			t.Fatalf("override = %+v, %v", c, err)
		}
	}
	for _, raw := range []string{"", "0", "-1", "oops"} {
		if _, err := ResolveCompletionConfig(map[string]string{"local-model-max-tokens": raw}); err == nil {
			t.Fatalf("accepted flag %q", raw)
		}
	}
	t.Setenv("PHOTOSCRAWL_LOCAL_MODEL_MAX_TOKENS", "2048")
	for _, name := range []string{"reasoning-effort", "reasoning-enabled", "reasoning-max-tokens"} {
		t.Setenv("PHOTOSCRAWL_LOCAL_MODEL_"+strings.ToUpper(strings.ReplaceAll(name, "-", "_")), "invalid")
		if _, err := ResolveCompletionConfig(nil); err == nil {
			t.Fatalf("accepted invalid %s", name)
		}
		t.Setenv("PHOTOSCRAWL_LOCAL_MODEL_"+strings.ToUpper(strings.ReplaceAll(name, "-", "_")), "")
	}
	for _, effort := range []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"} {
		if _, err := ResolveCompletionConfig(map[string]string{"local-model-reasoning-effort": effort}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCompletionConfigConflicts(t *testing.T) {
	enabled, disabled := true, false
	for _, c := range []CompletionConfig{
		{MaxTokens: -1}, {ReasoningMaxTokens: -1}, {ReasoningEffort: "unknown"},
		{ReasoningEffort: "low", ReasoningEnabled: &enabled},
		{ReasoningEffort: "none", ReasoningEnabled: &disabled},
		{ReasoningEffort: "low", ReasoningMaxTokens: 128},
		{ReasoningEnabled: &disabled, ReasoningMaxTokens: 128},
	} {
		if _, err := c.normalized(); err == nil {
			t.Fatalf("accepted conflict %+v", c)
		}
	}
}

func TestOpenAIRequestControls(t *testing.T) {
	enabled, disabled := true, false
	for _, tc := range []struct {
		name    string
		config  CompletionConfig
		effort  string
		enabled *bool
		cap     int
	}{
		{name: "default"},
		{name: "budget", config: CompletionConfig{MaxTokens: 4096}},
		{name: "effort", config: CompletionConfig{ReasoningEffort: "low"}, effort: "low"},
		{name: "disable", config: CompletionConfig{ReasoningEnabled: &disabled}, enabled: &disabled},
		{name: "enable", config: CompletionConfig{ReasoningEnabled: &enabled}, enabled: &enabled},
		{name: "cap", config: CompletionConfig{ReasoningMaxTokens: 128}, cap: 128},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := localModelClassifier{api: "openai", endpointURL: "http://127.0.0.1/v1/chat/completions", completion: tc.config, client: &http.Client{Transport: syntheticRoundTripper(func(r *http.Request) (*http.Response, error) {
				var req map[string]any
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Fatal(err)
				}
				want := 2048
				if tc.config.MaxTokens > 0 {
					want = tc.config.MaxTokens
				}
				if req["max_tokens"] != float64(want) {
					t.Errorf("budget = %v", req["max_tokens"])
				}
				if tc.effort == "" {
					if _, ok := req["reasoning_effort"]; ok {
						t.Error("unexpected effort")
					}
				} else if req["reasoning_effort"] != tc.effort {
					t.Error("missing effort")
				}
				reasoning, exists := req["reasoning"].(map[string]any)
				if tc.enabled == nil && tc.cap == 0 {
					if exists {
						t.Error("default reasoning changed")
					}
				} else {
					if !exists {
						t.Fatal("missing reasoning")
					}
					if tc.enabled != nil && reasoning["enabled"] != *tc.enabled {
						t.Errorf("enabled = %v", reasoning["enabled"])
					}
					if tc.cap > 0 && reasoning["max_tokens"] != float64(tc.cap) {
						t.Error("missing reasoning cap")
					}
					if _, ok := reasoning["exclude"]; ok {
						t.Error("unexpected exclude")
					}
				}
				return syntheticHTTPResponse(r, 200, `{"choices":[{"finish_reason":"stop","message":{"content":"{\"scene_summary\":\"Synthetic plate.\"}"}}],"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}}`), nil
			})}}
			path := filepath.Join(t.TempDir(), "synthetic.jpeg")
			if err := os.WriteFile(path, []byte("synthetic"), 0600); err != nil {
				t.Fatal(err)
			}
			result, err := c.classify(context.Background(), path)
			if err != nil || result.Payload["scene_summary"] != "Synthetic plate." || result.Diagnostics.Usage.TotalTokens != 30 {
				t.Fatalf("result=%+v error=%v", result, err)
			}
		})
	}
}

func syntheticHTTPResponse(r *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)), Body: io.NopCloser(strings.NewReader(body)), Request: r}
}

func TestOpenAIProviderErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		status           int
	}{
		{"422", `{"error":{"message":"reasoning budget unsupported","secret":"private-value"},"usage":{"completion_tokens":12}}`, "reasoning budget unsupported", 422},
		{"plain", "unsupported parameter", "unsupported parameter", 400},
		{"quoted_credential", `invalid token="private-value with spaces"`, "[redacted]", 422},
		{"raw_image_echo", `invalid image c3ludGhldGlj`, "omitted", 422},
		{"oversize", strings.Repeat("x", 10000), "exceeded 8192", 502},
		{"echo", `{"error":{"message":"invalid token=token-private-value Bearer bearer-private-value data:image/jpeg;base64,c3ludGhldGlj sk-private-value"},"request":{"image":"private-value"}}`, "[redacted]", 422},
		{"json_echo", `{"error":{"message":"unsupported request: {\"token\":\"private-value\",\"image\":\"private-value\"}"}}`, "unsupported", 422},
		{"malformed_envelope", `{"private":"private-value"`, "unreadable", 422},
		{"provider_200", `{"error":{"message":"unsupported reasoning"},"usage":{"completion_tokens":12}}`, "completion_tokens", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := localModelClassifier{endpointURL: "http://127.0.0.1/v1/chat/completions", client: &http.Client{Transport: syntheticRoundTripper(func(r *http.Request) (*http.Response, error) {
				return syntheticHTTPResponse(r, tc.status, tc.body), nil
			})}}
			response, err := c.classifyOpenAI(context.Background(), []byte("synthetic"))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v", err)
			}
			if strings.Contains(err.Error(), "private-value") {
				t.Fatalf("sensitive echo: %v", err)
			}
			if tc.status != 200 && !strings.Contains(err.Error(), "HTTP") {
				t.Fatalf("missing status: %v", err)
			}
			if tc.name == "422" && (response.Diagnostics.Usage == nil || response.Diagnostics.Usage.CompletionTokens != 12) {
				t.Fatal("lost usage")
			}
		})
	}
}

func TestOpenAIMalformedOutputDiagnostics(t *testing.T) {
	c := localModelClassifier{api: "openai", endpointURL: "http://127.0.0.1/v1/chat/completions", client: &http.Client{Transport: syntheticRoundTripper(func(r *http.Request) (*http.Response, error) {
		return syntheticHTTPResponse(r, 200, `{"choices":[{"finish_reason":"stop","message":{"content":"{private-value}"}}],"usage":{"completion_tokens":10}}`), nil
	})}}
	path := filepath.Join(t.TempDir(), "synthetic.jpeg")
	if err := os.WriteFile(path, []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := c.classify(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "completion_tokens") || strings.Contains(err.Error(), "private-value") || result.Diagnostics.Usage.CompletionTokens != 10 {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	c.client.Transport = syntheticRoundTripper(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("transport failure token=private-value")
	})
	result, err = c.classify(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "transport failure") || strings.Contains(err.Error(), "private-value") || result.HTTPResponses != 0 {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestOpenAITransportErrorPreservesCause(t *testing.T) {
	sensitive := errors.New("sensitiveError Cookie: sessionid=short-secret session=abcd")
	for _, cause := range []error{context.Canceled, sensitive, fmt.Errorf("loopback guard: %w", sensitive)} {
		c := localModelClassifier{endpointURL: "http://127.0.0.1/v1/chat/completions", client: &http.Client{Transport: syntheticRoundTripper(func(*http.Request) (*http.Response, error) { return nil, cause })}}
		_, err := c.classifyOpenAI(context.Background(), []byte("synthetic"))
		if !errors.Is(err, cause) {
			t.Errorf("lost transport cause: %v", err)
		}
		if cause != context.Canceled && !errors.Is(err, sensitive) {
			t.Errorf("lost nested sentinel: %v", err)
		}
		if err == nil || !strings.Contains(err.Error(), "call local model:") || strings.Contains(err.Error(), sensitive.Error()) || strings.Contains(err.Error(), "short-secret") || strings.Contains(err.Error(), "abcd") {
			t.Errorf("unsafe or missing displayed context: %v", err)
		}
	}
}

func TestProviderCookieSessionRedaction(t *testing.T) {
	for _, message := range []string{"invalid Cookie: sessionid=short-secret; theme=short-secret", "invalid session=abcd", `invalid sessionid="short-secret"`, "invalid Set-Cookie: other=short-secret; Path=/"} {
		body, _ := json.Marshal(map[string]any{"error": map[string]any{"message": message}})
		got := providerErrorMessage(body)
		if !strings.Contains(got, "invalid") || strings.Contains(got, "short-secret") || strings.Contains(got, "abcd") {
			t.Errorf("unsafe cookie/session error: %s", got)
		}
	}
}
