package config

import (
	"os"
	"strings"
	"testing"
)

const valid = `
providers:
  groq:
    base_url: https://api.groq.com/openai/v1/
    models:
      - id: llama-3.3-70b-versatile
        context_length: 131072
  ollama:
    base_url: ${OLLAMA_BASE_URL}/v1
    local: true
    models:
      - id: llama3.2:3b
        context_length: 131072
routes:
  fast:
    - groq/llama-3.3-70b-versatile
    - ollama/llama3.2:3b
  private:
    - ollama/llama3.2:3b
`

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func keys(names ...string) HasKey {
	return func(p string) bool {
		for _, n := range names {
			if n == p {
				return true
			}
		}
		return false
	}
}

func TestParse(t *testing.T) {
	ollamaEnv := env(map[string]string{"OLLAMA_BASE_URL": "http://ollama:11434"})

	tests := []struct {
		name    string
		yaml    string
		env     func(string) string
		hasKey  HasKey
		wantErr []string // substrings that must all appear; empty = must succeed
	}{
		{name: "valid with key", yaml: valid, env: ollamaEnv, hasKey: keys("groq")},
		{name: "valid with no keys", yaml: valid, env: ollamaEnv, hasKey: keys()},
		{
			name: "unknown model in route",
			yaml: strings.Replace(valid, "- groq/llama-3.3-70b-versatile", "- groq/nope", 1),
			env:  ollamaEnv, hasKey: keys("groq"),
			wantErr: []string{"routes.fast[0]", `"groq/nope" is not a known provider/model`},
		},
		{
			name: "unset env reference",
			yaml: valid, env: env(nil), hasKey: keys("groq"),
			wantErr: []string{"providers.ollama.base_url", "OLLAMA_BASE_URL is not set"},
		},
		{
			name: "route name collides with model ID",
			yaml: valid + "  groq/llama-3.3-70b-versatile:\n    - ollama/llama3.2:3b\n",
			env:  ollamaEnv, hasKey: keys("groq"),
			wantErr: []string{"collides with the model ID"},
		},
		{
			name: "old api_key_env field is rejected",
			yaml: strings.Replace(valid, "    models:\n      - id: llama-3.3", "    api_key_env: GROQ_API_KEY\n    models:\n      - id: llama-3.3", 1),
			env:  ollamaEnv, hasKey: keys("groq"),
			wantErr: []string{"api_key_env"},
		},
		{
			name: "bad context length and empty route, both reported",
			yaml: strings.Replace(valid, "context_length: 131072\n  ollama", "context_length: 0\n  ollama", 1) + "  empty: []\n",
			env:  ollamaEnv, hasKey: keys("groq"),
			wantErr: []string{"context_length: must be a positive number", "routes.empty: route has no entries"},
		},
		{
			name:    "no providers",
			yaml:    "routes: {}\n",
			env:     ollamaEnv,
			wantErr: []string{"providers: at least one provider is required"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Parse([]byte(tt.yaml), tt.env, tt.hasKey)
			if len(tt.wantErr) == 0 {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if cfg == nil {
					t.Fatal("nil config")
				}
				return
			}
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}

func TestEnabledAndDisabled(t *testing.T) {
	env := env(map[string]string{"OLLAMA_BASE_URL": "http://ollama:11434"})

	cfg, err := Parse([]byte(valid), env, keys())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Providers["groq"].Enabled {
		t.Error("groq has no key and should be disabled")
	}
	if !cfg.Providers["ollama"].Enabled {
		t.Error("ollama is local and should be enabled without a key")
	}
	if got := cfg.DisabledProviders(); len(got) != 1 || got[0] != "groq" {
		t.Errorf("DisabledProviders = %v, want [groq]", got)
	}

	cfg, _ = Parse([]byte(valid), env, keys("groq"))
	if !cfg.Providers["groq"].Enabled {
		t.Error("groq has a key and should be enabled")
	}
}

func TestExpandAndNormalize(t *testing.T) {
	cfg, err := Parse([]byte(valid), env(map[string]string{"OLLAMA_BASE_URL": "http://ollama:11434"}), keys())
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Providers["ollama"].BaseURL; got != "http://ollama:11434/v1" {
		t.Errorf("ollama base_url = %q", got)
	}
	if got := cfg.Providers["groq"].BaseURL; got != "https://api.groq.com/openai/v1" {
		t.Errorf("trailing slash not trimmed: %q", got)
	}
	p, m, ok := cfg.Lookup("ollama/llama3.2:3b")
	if !ok || p.Name != "ollama" || m.ContextLength != 131072 {
		t.Errorf("Lookup failed: %v %v %v", p, m, ok)
	}
}

// The shipped example must always parse, so it can't silently rot.
func TestExampleConfigIsValid(t *testing.T) {
	data, err := os.ReadFile("../../../config/providers.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(data, env(map[string]string{"OLLAMA_BASE_URL": "http://ollama:11434"}), keys()); err != nil {
		t.Fatal(err)
	}
}
