package models

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// fakeOllama serves /api/tags and /api/show for two models, as Ollama 0.31
// does.
func fakeOllama(t *testing.T) *httptest.Server {
	t.Helper()
	show := map[string]string{
		"qwen3.5:9b": `{"capabilities": ["completion", "vision", "tools", "thinking"],
			"model_info": {"general.architecture": "qwen35", "qwen35.context_length": 262144}}`,
		"gemma3:4b": `{"capabilities": ["completion", "vision"],
			"model_info": {"gemma3.context_length": 131072}}`,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"models": [{"name": "qwen3.5:9b"}, {"name": "gemma3:4b"}]}`))
	})
	mux.HandleFunc("POST /api/show", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Model string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || show[req.Model] == "" {
			http.Error(w, "model not found", http.StatusNotFound)
			return
		}
		w.Write([]byte(show[req.Model]))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestFromOllama(t *testing.T) {
	srv := fakeOllama(t)
	m, err := FromOllama(t.Context(), srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	want := Models{
		"ollama:qwen3.5:9b": {ContextTokens: 262_144, ToolCalling: true, Thinking: true},
		"ollama:gemma3:4b":  {ContextTokens: 131_072},
	}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("models:\n%v\nwant:\n%v", m, want)
	}

	srv.Close()
	if _, err := FromOllama(t.Context(), srv.URL); err == nil {
		t.Error("server down: no error")
	}
}
