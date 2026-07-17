package archive

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestLocalModelEndpointRejectsAnyOffLoopbackResolution(t *testing.T) {
	t.Parallel()
	lookup := func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{
			{IP: net.ParseIP("127.0.0.1")},
			{IP: net.ParseIP("192.0.2.10")},
		}, nil
	}
	_, err := newLocalModelClassifierWithResolver(context.Background(), "fixture", "http://model.test/v1", localModelAPIOpenAI, lookup)
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("error = %v", err)
	}
}

func TestLocalModelEndpointRejectsOffLoopbackLiteral(t *testing.T) {
	t.Parallel()
	_, err := newLocalModelClassifier(context.Background(), "fixture", "http://192.0.2.10/v1", localModelAPIOpenAI)
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("error = %v", err)
	}
}

func TestLocalModelTransportRevalidatesResolutionBeforeDial(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(openAIResponseHandler))
	defer server.Close()
	imagePath := writeModelTestImage(t)

	var lookups atomic.Int32
	lookup := func(context.Context, string) ([]net.IPAddr, error) {
		if lookups.Add(1) == 1 {
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
		}
		return []net.IPAddr{{IP: net.ParseIP("192.0.2.10")}}, nil
	}
	port := server.Listener.Addr().(*net.TCPAddr).Port
	endpoint := "http://model.test:" + strconv.Itoa(port) + "/v1/chat/completions"
	classifier, err := newLocalModelClassifierWithResolver(context.Background(), "fixture", endpoint, localModelAPIOpenAI, lookup)
	if err != nil {
		t.Fatal(err)
	}
	result, err := classifier.classify(context.Background(), imagePath)
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("error = %v", err)
	}
	if result.HTTPRequests != 1 || result.HTTPResponses != 0 || len(result.ResponseEndpoints) != 0 {
		t.Fatalf("provenance = %#v", result)
	}
}

func TestLocalModelRedirectStoresActualLoopbackEndpoint(t *testing.T) {
	t.Parallel()
	destination := httptest.NewServer(http.HandlerFunc(openAIResponseHandler))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/v1/chat/completions", http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	classifier, err := newLocalModelClassifier(context.Background(), "fixture", redirect.URL+"/v1/chat/completions", localModelAPIOpenAI)
	if err != nil {
		t.Fatal(err)
	}
	result, err := classifier.classify(context.Background(), writeModelTestImage(t))
	if err != nil {
		t.Fatal(err)
	}
	if result.Endpoint != destination.URL+"/v1/chat/completions" {
		t.Fatalf("endpoint = %q", result.Endpoint)
	}
	if result.HTTPRequests != 2 || result.HTTPResponses != 2 || len(result.ResponseEndpoints) != 2 {
		t.Fatalf("provenance = %#v", result)
	}
}

func TestLocalModelRedirectRejectsOffLoopbackTarget(t *testing.T) {
	t.Parallel()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://192.0.2.10/v1/chat/completions", http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	classifier, err := newLocalModelClassifier(context.Background(), "fixture", redirect.URL+"/v1/chat/completions", localModelAPIOpenAI)
	if err != nil {
		t.Fatal(err)
	}
	result, err := classifier.classify(context.Background(), writeModelTestImage(t))
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("error = %v", err)
	}
	if result.HTTPRequests != 1 || result.HTTPResponses != 1 || len(result.ResponseEndpoints) != 1 || result.ResponseEndpoints[0] != redirect.URL+"/v1/chat/completions" {
		t.Fatalf("provenance = %#v", result)
	}
}

func TestLocalModelUnreadableImageRecordsNoHTTPRequest(t *testing.T) {
	t.Parallel()
	classifier, err := newLocalModelClassifier(context.Background(), "fixture", defaultOpenAIChatURL, localModelAPIOpenAI)
	if err != nil {
		t.Fatal(err)
	}
	result, err := classifier.classify(context.Background(), filepath.Join(t.TempDir(), "missing.jpeg"))
	if err == nil || !strings.Contains(err.Error(), "read local image") {
		t.Fatalf("error = %v", err)
	}
	if result.HTTPRequests != 0 || result.HTTPResponses != 0 || len(result.ResponseEndpoints) != 0 {
		t.Fatalf("provenance = %#v", result)
	}
}

func TestLocalModelMalformedResponsePreservesHTTPProvenance(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"not json"}}]}`))
	}))
	defer server.Close()
	classifier, err := newLocalModelClassifier(context.Background(), "fixture", server.URL, localModelAPIOpenAI)
	if err != nil {
		t.Fatal(err)
	}
	result, err := classifier.classify(context.Background(), writeModelTestImage(t))
	if err == nil || !strings.Contains(err.Error(), "did not return a JSON object") {
		t.Fatalf("error = %v", err)
	}
	expectedEndpoint := server.URL + "/v1/chat/completions"
	if result.HTTPRequests != 1 || result.HTTPResponses != 1 || result.Endpoint != expectedEndpoint || len(result.ResponseEndpoints) != 1 || result.ResponseEndpoints[0] != expectedEndpoint {
		t.Fatalf("provenance = %#v", result)
	}
}

func openAIResponseHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []any{map[string]any{
			"message": map[string]any{
				"content": `{"scene_summary":"A synthetic fixture."}`,
			},
		}},
	})
}

func writeModelTestImage(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.jpeg")
	if err := os.WriteFile(path, []byte("synthetic image bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
