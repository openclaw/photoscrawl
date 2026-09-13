package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	repoPrompts "github.com/openclaw/photoscrawl/prompts"
)

const (
	localModelClassifierSource = "local_multimodal"
	localModelPromptVersion    = repoPrompts.LocalMultimodalObservationsV1Version
	defaultOllamaGenerateURL   = "http://127.0.0.1:11434/api/generate"
	defaultOpenAIChatURL       = "http://127.0.0.1:1234/v1/chat/completions"
	localModelAPIOllama        = "ollama"
	localModelAPIOpenAI        = "openai"
)

type localModelResult struct {
	Payload           map[string]any
	RawResponse       string
	Endpoint          string
	ImageBytes        int64
	ImageSHA256       string
	Observations      []contentObservation
	HTTPRequests      int
	HTTPResponses     int
	ResponseEndpoints []string
}

type localModelClassifier struct {
	modelID       string
	promptVersion string
	api           string
	endpointURL   string
	client        *http.Client
}

type ollamaGenerateRequest struct {
	Model   string         `json:"model"`
	Prompt  string         `json:"prompt"`
	Images  []string       `json:"images"`
	Stream  bool           `json:"stream"`
	Options map[string]any `json:"options,omitempty"`
}

type ollamaGenerateResponse struct {
	Response string `json:"response"`
	Done     bool   `json:"done"`
	Error    string `json:"error,omitempty"`
}

type localModelHTTPResponse struct {
	Content  string
	Endpoint string
}

func newLocalModelClassifier(ctx context.Context, modelID, endpointURL, api string) (localModelClassifier, error) {
	return newLocalModelClassifierWithResolver(ctx, modelID, endpointURL, api, net.DefaultResolver.LookupIPAddr)
}

func newLocalModelClassifierWithResolver(ctx context.Context, modelID, endpointURL, api string, lookup lookupIPAddrFunc) (localModelClassifier, error) {
	api = strings.ToLower(strings.TrimSpace(api))
	if api == "" {
		api = localModelAPIOllama
	}
	endpointURL = strings.TrimSpace(endpointURL)
	switch api {
	case localModelAPIOllama:
		if endpointURL == "" {
			endpointURL = defaultOllamaGenerateURL
		}
	case localModelAPIOpenAI:
		endpointURL = normalizeOpenAIChatURL(endpointURL)
	default:
		return localModelClassifier{}, fmt.Errorf("unsupported local model api %q", api)
	}
	validatedEndpoint, err := validateLoopbackEndpoint(ctx, endpointURL, lookup)
	if err != nil {
		return localModelClassifier{}, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = loopbackDialContext(lookup)
	return localModelClassifier{
		modelID:       strings.TrimSpace(modelID),
		promptVersion: localModelPromptVersion,
		api:           api,
		endpointURL:   validatedEndpoint,
		client: &http.Client{
			Timeout:   10 * time.Minute,
			Transport: recordingRoundTripper{base: transport},
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return errors.New("stopped after 10 redirects")
				}
				_, err := validateLoopbackEndpoint(req.Context(), req.URL.String(), lookup)
				return err
			},
		},
	}, nil
}

func (c localModelClassifier) classify(ctx context.Context, imagePath string) (localModelResult, error) {
	data, err := os.ReadFile(imagePath)
	if err != nil {
		return localModelResult{}, fmt.Errorf("read local image: %w", err)
	}
	recorder := &modelHTTPRecorder{}
	ctx = context.WithValue(ctx, modelHTTPRecorderKey{}, recorder)
	sum := sha256.Sum256(data)
	var response localModelHTTPResponse
	switch c.api {
	case localModelAPIOllama:
		response, err = c.classifyOllama(ctx, data)
	case localModelAPIOpenAI:
		response, err = c.classifyOpenAI(ctx, data)
	default:
		err = fmt.Errorf("unsupported local model api %q", c.api)
	}
	if err != nil {
		requests, responses, endpoints := recorder.snapshot()
		return localModelResult{HTTPRequests: requests, HTTPResponses: responses, ResponseEndpoints: endpoints}, err
	}
	payload, err := parseModelPayload(response.Content)
	if err != nil {
		requests, responses, endpoints := recorder.snapshot()
		return localModelResult{Endpoint: response.Endpoint, HTTPRequests: requests, HTTPResponses: responses, ResponseEndpoints: endpoints}, err
	}
	requests, responses, endpoints := recorder.snapshot()
	return localModelResult{
		Payload:           payload,
		RawResponse:       strings.TrimSpace(response.Content),
		Endpoint:          response.Endpoint,
		ImageBytes:        int64(len(data)),
		ImageSHA256:       hex.EncodeToString(sum[:]),
		Observations:      observationsFromPayload(payload),
		HTTPRequests:      requests,
		HTTPResponses:     responses,
		ResponseEndpoints: endpoints,
	}, nil
}

func (c localModelClassifier) classifyOllama(ctx context.Context, data []byte) (localModelHTTPResponse, error) {
	request := ollamaGenerateRequest{
		Model:  c.modelID,
		Prompt: repoPrompts.LocalMultimodalObservationsV1,
		Images: []string{base64.StdEncoding.EncodeToString(data)},
		Stream: false,
		Options: map[string]any{
			"temperature": 0.1,
		},
	}
	var generated ollamaGenerateResponse
	endpoint, err := c.postJSON(ctx, request, &generated)
	if err != nil {
		return localModelHTTPResponse{}, err
	}
	if strings.TrimSpace(generated.Error) != "" {
		return localModelHTTPResponse{}, errors.New(generated.Error)
	}
	return localModelHTTPResponse{Content: generated.Response, Endpoint: endpoint}, nil
}

type openAIChatCompletionRequest struct {
	Model       string              `json:"model"`
	Messages    []openAIChatMessage `json:"messages"`
	Temperature float64             `json:"temperature"`
	MaxTokens   int                 `json:"max_tokens,omitempty"`
}

type openAIChatMessage struct {
	Role    string                  `json:"role"`
	Content []openAIChatMessagePart `json:"content"`
}

type openAIChatMessagePart struct {
	Type     string                  `json:"type"`
	Text     string                  `json:"text,omitempty"`
	ImageURL *openAIChatImageURLPart `json:"image_url,omitempty"`
}

type openAIChatImageURLPart struct {
	URL string `json:"url"`
}

type openAIChatCompletionResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (c localModelClassifier) classifyOpenAI(ctx context.Context, data []byte) (localModelHTTPResponse, error) {
	mediaType := http.DetectContentType(data)
	request := openAIChatCompletionRequest{
		Model: c.modelID,
		Messages: []openAIChatMessage{{
			Role: "user",
			Content: []openAIChatMessagePart{
				{Type: "text", Text: repoPrompts.LocalMultimodalObservationsV1},
				{Type: "image_url", ImageURL: &openAIChatImageURLPart{URL: "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data)}},
			},
		}},
		Temperature: 0.1,
		MaxTokens:   800,
	}
	var completion openAIChatCompletionResponse
	endpoint, err := c.postJSON(ctx, request, &completion)
	if err != nil {
		return localModelHTTPResponse{}, err
	}
	if completion.Error != nil && strings.TrimSpace(completion.Error.Message) != "" {
		return localModelHTTPResponse{}, errors.New(completion.Error.Message)
	}
	if len(completion.Choices) == 0 {
		return localModelHTTPResponse{}, errors.New("local model returned no choices")
	}
	return localModelHTTPResponse{Content: completion.Choices[0].Message.Content, Endpoint: endpoint}, nil
}

func normalizeOpenAIChatURL(value string) string {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	if value == "" {
		return defaultOpenAIChatURL
	}
	if strings.HasSuffix(value, "/v1/chat/completions") || strings.HasSuffix(value, "/chat/completions") {
		return value
	}
	if strings.HasSuffix(value, "/v1") {
		return value + "/chat/completions"
	}
	return value + "/v1/chat/completions"
}

func (c localModelClassifier) postJSON(ctx context.Context, request, response any) (string, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpointURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("call local model: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("local model returned %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(response); err != nil {
		return "", fmt.Errorf("decode local model response: %w", err)
	}
	return responseEndpoint(resp), nil
}
