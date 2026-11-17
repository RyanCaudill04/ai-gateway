package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/RyanCaudill04/ai-gateway/router/internal/config"
)

const testYAML = `
providers:
  groq:
    base_url: https://groq.example/v1
    models:
      - id: big
        context_length: 131072
      - id: small
        context_length: 8192
  gemini:
    base_url: https://gemini.example/v1
    models:
      - id: flash
        context_length: 1048576
  ollama:
    base_url: http://ollama:11434/v1
    local: true
    models:
      - id: llama
        context_length: 4096
routes:
  fast: [groq/small, ollama/llama]
  long-context: [gemini/flash, groq/big]
  private: [ollama/llama]
`

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Parse([]byte(testYAML), func(string) string { return "" }, nil)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func enabledSet(names ...string) EnabledFunc {
	return func(p string) bool { return slices.Contains(names, p) }
}

func get(t *testing.T, cfg *config.Config, enabled EnabledFunc) []Model {
	t.Helper()
	w := httptest.NewRecorder()
	Models(cfg, enabled).ServeHTTP(w, httptest.NewRequest("GET", "/v1/models", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var body modelList
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Object != "list" {
		t.Errorf("object = %q, want list", body.Object)
	}
	return body.Data
}

func ids(ms []Model) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}

func TestModelsAllEnabled(t *testing.T) {
	ms := get(t, testConfig(t), enabledSet("groq", "gemini", "ollama"))

	want := []string{
		"fast", "long-context", "private", // routes first, sorted
		"gemini/flash", "groq/big", "groq/small", "ollama/llama",
	}
	if got := ids(ms); !slices.Equal(got, want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}

	byID := map[string]Model{}
	for _, m := range ms {
		byID[m.ID] = m
		if m.Object != "model" || m.OwnedBy == "" || m.ContextLength <= 0 {
			t.Errorf("%s: incomplete OpenAI fields: %+v", m.ID, m)
		}
	}

	cases := []struct {
		id       string
		ctx      int
		provider string
		local    bool
		virtual  bool
	}{
		{"groq/big", 131072, "groq", false, false},
		{"ollama/llama", 4096, "ollama", true, false},
		{"fast", 8192, "", false, true}, // largest entry is groq/small
		{"long-context", 1048576, "", false, true},
		{"private", 4096, "", true, true},
	}
	for _, c := range cases {
		m := byID[c.id]
		if m.ContextLength != c.ctx || m.Provider != c.provider || m.Local != c.local || m.Virtual != c.virtual {
			t.Errorf("%s = %+v, want ctx=%d provider=%q local=%v virtual=%v", c.id, m, c.ctx, c.provider, c.local, c.virtual)
		}
	}
}

func TestModelsDisabledProviders(t *testing.T) {
	tests := []struct {
		name    string
		enabled EnabledFunc
		want    []string
		ctx     map[string]int // expected route context lengths
	}{
		{
			name:    "disabled provider's models left out, routes shrink to usable entries",
			enabled: enabledSet("groq", "ollama"),
			want:    []string{"fast", "long-context", "private", "groq/big", "groq/small", "ollama/llama"},
			ctx:     map[string]int{"fast": 8192, "long-context": 131072, "private": 4096},
		},
		{
			name:    "route with no usable entries is not listed",
			enabled: enabledSet("gemini"),
			want:    []string{"long-context", "gemini/flash"},
			ctx:     map[string]int{"long-context": 1048576},
		},
		{
			name:    "nothing enabled gives an empty list",
			enabled: enabledSet(),
			want:    []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := get(t, testConfig(t), tt.enabled)
			if got := ids(ms); !slices.Equal(got, tt.want) {
				t.Fatalf("ids = %v, want %v", got, tt.want)
			}
			for _, m := range ms {
				if want, ok := tt.ctx[m.ID]; ok && m.ContextLength != want {
					t.Errorf("%s context_length = %d, want %d", m.ID, m.ContextLength, want)
				}
			}
		})
	}
}

func TestRouteLocalOnlyWhenEveryUsableEntryIsLocal(t *testing.T) {
	// fast = groq (remote) + ollama (local): local only once groq is gone.
	cfg := testConfig(t)
	for _, tc := range []struct {
		enabled EnabledFunc
		local   bool
	}{
		{enabledSet("groq", "ollama"), false},
		{enabledSet("ollama"), true},
	} {
		for _, m := range get(t, cfg, tc.enabled) {
			if m.ID == "fast" && m.Local != tc.local {
				t.Errorf("fast.local = %v, want %v", m.Local, tc.local)
			}
		}
	}
}

func TestEmptyListEncodesAsArray(t *testing.T) {
	w := httptest.NewRecorder()
	Models(testConfig(t), enabledSet()).ServeHTTP(w, httptest.NewRequest("GET", "/v1/models", nil))
	if got := w.Body.String(); got != `{"object":"list","data":[]}`+"\n" {
		t.Errorf("body = %q", got)
	}
}

func TestEnabledIsReadPerRequest(t *testing.T) {
	keys := map[string]bool{}
	h := Models(testConfig(t), func(p string) bool { return keys[p] })

	count := func() int {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/models", nil))
		var body modelList
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return len(body.Data)
	}

	if n := count(); n != 0 {
		t.Fatalf("before key: %d models, want 0", n)
	}
	keys["gemini"] = true // a key registered while the router runs
	if n := count(); n != 2 {
		t.Fatalf("after key: %d models, want 2 (long-context + gemini/flash)", n)
	}
	delete(keys, "gemini")
	if n := count(); n != 0 {
		t.Fatalf("after removal: %d models, want 0", n)
	}
}
