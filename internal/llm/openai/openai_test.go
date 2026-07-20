package openai

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/0x4D31/venator/internal/llm/provider"
)

func TestNewRejectsInsecureRemoteServerURL(t *testing.T) {
	_, err := New(provider.Config{
		APIKey: "test-only", Model: "test-model", ServerURL: "http://models.example.test/v1",
	})
	if err == nil || !strings.Contains(err.Error(), "must use HTTPS") {
		t.Fatalf("error = %v", err)
	}
}

func TestNewAllowsLoopbackHTTPForLocalCompatibleServer(t *testing.T) {
	if _, err := New(provider.Config{
		APIKey: "test-only", Model: "test-model", ServerURL: "http://127.0.0.1:8080/v1",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestNewRejectsWhitespaceOnlyCredentialsAndModel(t *testing.T) {
	tests := []provider.Config{
		{APIKey: " \n\t", Model: "test-model"},
		{APIKey: "test-only", Model: " \n\t"},
	}
	for _, cfg := range tests {
		if _, err := New(cfg); err == nil {
			t.Fatalf("accepted invalid config: %#v", cfg)
		}
	}
}

func TestNewRejectsServerURLQueryAndFragment(t *testing.T) {
	for _, serverURL := range []string{
		"https://models.example.test/v1?tenant=one",
		"https://models.example.test/v1?",
		"https://models.example.test/v1#responses",
	} {
		_, err := New(provider.Config{APIKey: "test-only", Model: "test-model", ServerURL: serverURL})
		if err == nil || !strings.Contains(err.Error(), "server URL is invalid") || strings.Contains(err.Error(), serverURL) {
			t.Fatalf("serverURL=%q error=%v", serverURL, err)
		}
	}
}

func TestCallSendsExplicitZeroTemperature(t *testing.T) {
	configured := 0.0
	request := callRequest(t, provider.Config{APIKey: "test-only", Model: "test-model", Temperature: &configured})
	temperature, ok := request["temperature"]
	if !ok {
		t.Fatal("request omitted explicitly configured zero temperature")
	}
	if temperature != json.Number("0") {
		t.Fatalf("temperature = %#v, want 0", temperature)
	}
}

func TestCallOmitsUnconfiguredTemperature(t *testing.T) {
	request := callRequest(t, provider.Config{APIKey: "test-only", Model: "test-model"})
	if _, ok := request["temperature"]; ok {
		t.Fatalf("request included unconfigured temperature: %#v", request["temperature"])
	}
}

func TestNewRejectsOutOfRangeTemperature(t *testing.T) {
	temperature := 2.1
	_, err := New(provider.Config{APIKey: "test-only", Model: "test-model", Temperature: &temperature})
	if err == nil || !strings.Contains(err.Error(), "between 0 and 2") {
		t.Fatalf("error = %v", err)
	}
}

func TestNewRejectsNonFiniteTemperature(t *testing.T) {
	for name, temperature := range map[string]float64{
		"NaN":               math.NaN(),
		"positive infinity": math.Inf(1),
		"negative infinity": math.Inf(-1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := New(provider.Config{APIKey: "test-only", Model: "test-model", Temperature: &temperature})
			if err == nil || !strings.Contains(err.Error(), "between 0 and 2") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func callRequest(t *testing.T, cfg provider.Config) map[string]any {
	t.Helper()
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		decoder := json.NewDecoder(r.Body)
		decoder.UseNumber()
		if err := decoder.Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"{\"decisions\":[]}"}]}]}`))
	}))
	defer server.Close()
	cfg.ServerURL = server.URL
	client, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Call(context.Background(), provider.Request{System: "system", User: "user"}); err != nil {
		t.Fatal(err)
	}
	return request
}
