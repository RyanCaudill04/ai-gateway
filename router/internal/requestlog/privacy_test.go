package requestlog

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/RyanCaudill04/ai-gateway/router/internal/api"
	"github.com/RyanCaudill04/ai-gateway/router/internal/apikeys"
	"github.com/RyanCaudill04/ai-gateway/router/internal/config"
	"github.com/RyanCaudill04/ai-gateway/router/internal/store"
	"github.com/RyanCaudill04/ai-gateway/router/internal/upstream"
)

// TestEntryHasOnlyAllowedFields fails when someone adds a field to Entry.
// That is the point: a new field has to be added to this list on purpose, so
// the person doing it has to stop and ask "could this carry prompt or
// completion text?" The answer must be no. Free-form types (string slices,
// maps, interfaces, pointers, byte slices) are rejected even if listed.
func TestEntryHasOnlyAllowedFields(t *testing.T) {
	allowed := map[string]reflect.Type{
		"Time":             reflect.TypeOf(time.Time{}),
		"Service":          reflect.TypeOf(""),
		"RequestedModel":   reflect.TypeOf(""), // a model name the client chose, not content
		"Provider":         reflect.TypeOf(""),
		"Model":            reflect.TypeOf(""),
		"FallbackAttempts": reflect.TypeOf(0),
		"PromptTokens":     reflect.TypeOf(0), // a count
		"CompletionTokens": reflect.TypeOf(0),
		"Latency":          reflect.TypeOf(time.Duration(0)),
		"Status":           reflect.TypeOf(0),
		"ErrorType":        reflect.TypeOf(""), // the category, never an error message
	}
	typ := reflect.TypeOf(Entry{})
	seen := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		seen[f.Name] = true
		want, ok := allowed[f.Name]
		if !ok {
			t.Errorf("Entry has a new field %q (%s). The request log must never hold prompt or completion text: "+
				"if this field cannot carry content, add it to the allowed list in this test.", f.Name, f.Type)
			continue
		}
		if f.Type != want {
			t.Errorf("Entry.%s changed type to %s (allowed: %s)", f.Name, f.Type, want)
		}
		switch f.Type.Kind() {
		case reflect.Interface, reflect.Map, reflect.Slice, reflect.Array, reflect.Pointer, reflect.Func, reflect.Chan:
			t.Errorf("Entry.%s is a free-form %s type; it could carry content", f.Name, f.Type.Kind())
		}
	}
	for name := range allowed {
		if !seen[name] {
			t.Errorf("allowed field %q no longer exists; remove it from the list", name)
		}
	}
}

// TestRequestTextNeverReachesTheDatabase sends a request with a recognisable
// marker in the prompt (and one in the upstream's answer) through the real
// chat handler into a real database, then searches every database file for
// the markers. The service name is searched too, as a control: it proves the
// search can find a row that was written.
func TestRequestTextNeverReachesTheDatabase(t *testing.T) {
	const (
		promptMarker     = "PRIVACY-MARKER-PROMPT-7f3a9c"
		completionMarker = "PRIVACY-MARKER-ANSWER-2b81de"
		serviceName      = "svc-privacy-control"
	)
	dir := t.TempDir()
	db, err := store.Open(context.Background(), filepath.Join(dir, "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	logger := Open(db, Options{})

	// A provider error message that quotes the prompt, as real ones can.
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := readAll(r)
		if strings.Contains(body, "fail-please") {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "bad input: " + promptMarker}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": completionMarker}}},
			"usage":   map[string]any{"prompt_tokens": 5, "completion_tokens": 7},
		})
	}))
	defer up.Close()
	h := chatHandler(t, up.URL, logger.OnSummary)

	for _, content := range []string{promptMarker, promptMarker + " fail-please"} {
		body := `{"model":"fast","messages":[{"role":"user","content":"` + content + `"}]}`
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
		req = req.WithContext(apikeys.ContextWithName(req.Context(), serviceName))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := logger.Close(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	db.Reader().QueryRow(`SELECT COUNT(*) FROM request_log`).Scan(&n)
	if n != 2 {
		t.Fatalf("rows = %d, want 2", n)
	}

	grep := func(stage string) {
		files, _ := filepath.Glob(filepath.Join(dir, "gateway.db*"))
		found := false
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range []string{promptMarker, completionMarker, "PRIVACY-MARKER"} {
				if bytes.Contains(data, []byte(m)) {
					t.Errorf("%s: %s contains %q", stage, filepath.Base(f), m)
				}
			}
			found = found || bytes.Contains(data, []byte(serviceName))
		}
		if !found {
			t.Errorf("%s: control failed: the service name is in none of %v, so the search proves nothing", stage, files)
		}
	}
	grep("while open (database and -wal file)")
	db.Close() // checkpoints the WAL into the main file
	grep("after close")
}

func readAll(r *http.Request) (string, error) {
	var b bytes.Buffer
	_, err := b.ReadFrom(r.Body)
	return b.String(), err
}

// chatHandler is the real chat handler in front of a fake provider at upURL,
// with one route, "fast", to a local (keyless) model.
func chatHandler(t testing.TB, upURL string, on api.SummaryFunc) http.Handler {
	t.Helper()
	const yaml = `
providers:
  ollama:
    base_url: ${UP}/v1
    local: true
    models:
      - id: tiny
        context_length: 1000
routes:
  fast: [ollama/tiny]
`
	cfg, err := config.Parse([]byte(yaml), func(string) string { return upURL }, nil)
	if err != nil {
		t.Fatal(err)
	}
	return api.Chat(api.ChatDeps{
		Config:    cfg,
		Enabled:   func(string) bool { return true },
		Client:    upstream.New(upstream.Options{}),
		OnSummary: on,
	})
}
