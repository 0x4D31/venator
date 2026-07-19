package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	llmconfig "github.com/0x4D31/venator/internal/llm/config"
	llmmodel "github.com/0x4D31/venator/internal/llm/model"
)

func TestNewRejectsInsecureRemoteServerURL(t *testing.T) {
	_, err := New(llmconfig.Config{
		APIKey: "test-only", Model: "test-model", ServerURL: "http://models.example.test/v1",
	})
	if err == nil || !strings.Contains(err.Error(), "must use HTTPS") {
		t.Fatalf("error = %v", err)
	}
}

func TestNewAllowsLoopbackHTTPForLocalCompatibleServer(t *testing.T) {
	if _, err := New(llmconfig.Config{
		APIKey: "test-only", Model: "test-model", ServerURL: "http://127.0.0.1:8080/v1",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCallSendsExplicitZeroTemperature(t *testing.T) {
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

	client, err := New(llmconfig.Config{
		APIKey: "test-only", Model: "test-model", ServerURL: server.URL, Temperature: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Call(context.Background(), llmmodel.Request{System: "system", User: "user"}); err != nil {
		t.Fatal(err)
	}
	temperature, ok := request["temperature"]
	if !ok {
		t.Fatal("request omitted explicitly configured zero temperature")
	}
	if temperature != json.Number("0") {
		t.Fatalf("temperature = %#v, want 0", temperature)
	}
}
