package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/RyanCaudill04/ai-gateway/router/internal/config"
	"github.com/RyanCaudill04/ai-gateway/router/internal/upstream"
)

// chatHandler builds the handler against a fake provider named "fake".
func chatHandler(t *testing.T, provider http.HandlerFunc) http.Handler {
	t.Helper()
	srv := httptest.NewServer(provider)
	t.Cleanup(srv.Close)
	yaml := "providers:\n  fake:\n    base_url: " + srv.URL + "\n    local: true\n    models:\n      - id: m\n        context_length: 1000\nroutes:\n  private: [fake/m]\n"
	cfg, err := config.Parse([]byte(yaml), func(string) string { return "" }, nil)
	if err != nil {
		t.Fatal(err)
	}
	return ChatCompletions(cfg, upstream.New(upstream.Options{}), enabledSet("fake"), func(string) string { return "secret" })
}

func post(h http.Handler, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	return w
}

func TestChatForwardsToProvider(t *testing.T) {
	var gotAuth, gotBody string
	h := chatHandler(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotAuth, gotBody = r.Header.Get("Authorization"), string(b)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}]}`))
	})

	w := post(h, `{"model":"fake/m","messages":[{"role":"user","content":"yo"}]}`)

	if w.Code != 200 || !strings.Contains(w.Body.String(), `"hi"`) {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
	if gotAuth != "Bearer secret" {
		t.Errorf("provider got Authorization %q", gotAuth)
	}
	// The provider sees its own model id, and the rest of the body is kept.
	if !strings.Contains(gotBody, `"model":"m"`) || !strings.Contains(gotBody, `"yo"`) {
		t.Errorf("provider got body %s", gotBody)
	}
}

func TestChatRejectsBadRequests(t *testing.T) {
	h := chatHandler(t, func(w http.ResponseWriter, r *http.Request) { t.Error("provider should not be called") })
	for name, body := range map[string]string{
		"bad json":      `{`,
		"unknown model": `{"model":"nope/x"}`,
		"streaming":     `{"model":"fake/m","stream":true}`,
	} {
		if w := post(h, body); w.Code != 400 {
			t.Errorf("%s: got %d, want 400", name, w.Code)
		}
	}
}

func TestChatMapsProviderRateLimit(t *testing.T) {
	h := chatHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"slow down"}}`))
	})
	if w := post(h, `{"model":"fake/m"}`); w.Code != 429 {
		t.Errorf("got %d, want 429: %s", w.Code, w.Body)
	}
}
