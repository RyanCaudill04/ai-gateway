package admin

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/RyanCaudill04/ai-gateway/router/internal/config"
	"github.com/RyanCaudill04/ai-gateway/router/internal/providerkeys"
	"github.com/RyanCaudill04/ai-gateway/router/internal/store"
)

const goodKey = "gsk_GOOD_key_abcdefgh_1234"

func TestListenRefusesNonLoopback(t *testing.T) {
	for _, addr := range []string{":0", "0.0.0.0:0", "[::]:0", "192.168.1.10:0", "example.com:0", "nonsense"} {
		if ln, err := Listen(addr); err == nil {
			ln.Close()
			t.Errorf("Listen(%q) succeeded, want refusal", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:0", "localhost:0"} {
		ln, err := Listen(addr)
		if err != nil {
			t.Errorf("Listen(%q) = %v", addr, err)
			continue
		}
		ln.Close()
	}
}

// recorder keeps every response body the admin API sends, so a test can
// check that none of them ever contained a key.
type recorder struct {
	mu  sync.Mutex
	all bytes.Buffer
}

type teeWriter struct {
	http.ResponseWriter
	rec *recorder
}

func (w teeWriter) Write(b []byte) (int, error) {
	w.rec.mu.Lock()
	w.rec.all.Write(b)
	w.rec.mu.Unlock()
	return w.ResponseWriter.Write(b)
}

type fixture struct {
	keys   *providerkeys.Keys
	db     *store.Store
	client *Client
	bodies *recorder
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	// A fake provider for --test: accepts only goodKey.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+goodKey {
			http.Error(w, "invalid api key "+r.Header.Get("Authorization"), http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(upstream.Close)

	cfg, err := config.Parse([]byte(`
providers:
  groq:
    base_url: `+upstream.URL+`/v1
    models: [{id: llama, context_length: 8192}]
  gemini:
    base_url: `+upstream.URL+`/v1beta/openai
    models: [{id: flash, context_length: 8192}]
  ollama:
    base_url: http://ollama:11434/v1
    local: true
    models: [{id: tiny, context_length: 4096}]
`), os.Getenv, nil)
	if err != nil {
		t.Fatal(err)
	}

	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	c, _ := providerkeys.NewCipher(bytes.Repeat([]byte{9}, 32))
	keys, err := providerkeys.Load(context.Background(), db, c)
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	RegisterProviders(mux, Providers{
		Config: cfg,
		Keys:   keys,
		Enabled: func(name string) bool {
			p := cfg.Providers[name]
			return p != nil && (p.Local || keys.Has(name))
		},
	})
	rec := &recorder{}
	h := Guard(mux)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(teeWriter{w, rec}, r)
	}))
	t.Cleanup(srv.Close)
	return &fixture{keys: keys, db: db, client: NewClient(strings.TrimPrefix(srv.URL, "http://")), bodies: rec}
}

func (f *fixture) rows(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.db.Reader().QueryRow(`SELECT COUNT(*) FROM provider_keys`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestProviderKeyEndpoints(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	badKey := "gsk_BAD_key_zzzzzzzz_0000"

	var apiErr *Error
	wantErr := func(err error, status int, contains string) {
		t.Helper()
		if !errors.As(err, &apiErr) || apiErr.Status != status || !strings.Contains(apiErr.Message, contains) {
			t.Fatalf("err = %v, want %d containing %q", err, status, contains)
		}
	}

	// Unknown provider: rejected without repeating the name, which might be a key.
	err := f.client.Do(ctx, "PUT", "/admin/providers/"+badKey+"/key", SetKeyRequest{Key: goodKey}, nil)
	wantErr(err, http.StatusNotFound, "providers.yaml has: gemini, groq, ollama")
	// Local providers take no key.
	err = f.client.Do(ctx, "PUT", "/admin/providers/ollama/key", SetKeyRequest{Key: goodKey}, nil)
	wantErr(err, http.StatusBadRequest, "local provider")
	// Malformed keys and bodies.
	err = f.client.Do(ctx, "PUT", "/admin/providers/groq/key", SetKeyRequest{Key: "x"}, nil)
	wantErr(err, http.StatusBadRequest, "shorter")
	err = f.client.Do(ctx, "PUT", "/admin/providers/groq/key", map[string]string{"key": goodKey, "extra": "x"}, nil)
	wantErr(err, http.StatusBadRequest, "expected JSON")

	// A failed --test saves nothing and says why.
	err = f.client.Do(ctx, "PUT", "/admin/providers/groq/key", SetKeyRequest{Key: badKey, Test: true}, nil)
	wantErr(err, http.StatusUnprocessableEntity, "401")
	if f.keys.Has("groq") || f.rows(t) != 0 {
		t.Fatal("a key that failed its test was saved")
	}

	// A passing --test saves, and the in-memory lookup has it at once.
	var st ProviderStatus
	if err := f.client.Do(ctx, "PUT", "/admin/providers/groq/key", SetKeyRequest{Key: goodKey + "\n", Test: true}, &st); err != nil {
		t.Fatal(err)
	}
	if !st.Enabled || !st.HasKey || st.KeyLast4 != "1234" {
		t.Fatalf("status = %+v", st)
	}
	if got, ok := f.keys.Get("groq"); !ok || got != goodKey {
		t.Fatalf("Get = %q, %v", got, ok)
	}
	// Without --test, any well-formed key is saved.
	if err := f.client.Do(ctx, "PUT", "/admin/providers/gemini/key", SetKeyRequest{Key: badKey}, nil); err != nil {
		t.Fatal(err)
	}

	var list []ProviderStatus
	if err := f.client.Do(ctx, "GET", "/admin/providers", nil, &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || list[0].Provider != "gemini" || list[0].KeyLast4 != "0000" || !list[2].Local || !list[2].Enabled {
		t.Fatalf("list = %+v", list)
	}

	if err := f.client.Do(ctx, "DELETE", "/admin/providers/groq/key", nil, &st); err != nil {
		t.Fatal(err)
	}
	if st.Enabled || f.keys.Has("groq") {
		t.Fatalf("after remove: %+v", st)
	}
	err = f.client.Do(ctx, "DELETE", "/admin/providers/groq/key", nil, nil)
	wantErr(err, http.StatusNotFound, "no key registered")
	err = f.client.Do(ctx, "DELETE", "/admin/providers/nope/key", nil, nil)
	wantErr(err, http.StatusNotFound, "unknown provider")

	// Nothing the admin API sent back, on any path, contained a key.
	all := f.bodies.all.String()
	for _, k := range []string{goodKey, badKey} {
		if strings.Contains(all, k) {
			t.Fatalf("an admin response contained a key: %s", all)
		}
	}
}

func TestGuardRejectsBrowsersAndForeignHosts(t *testing.T) {
	h := Guard(http.NewServeMux())
	for name, mod := range map[string]func(*http.Request){
		"DNS rebinding host": func(r *http.Request) { r.Host = "evil.example:8081" },
		"browser origin":     func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
		"browser fetch":      func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
	} {
		r := httptest.NewRequest("GET", "/admin/providers", nil)
		r.Host = "127.0.0.1:8081"
		mod(r)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", name, w.Code)
		}
	}
}

func TestClientReportsRouterNotRunning(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listens here now

	err = NewClient(addr).Do(context.Background(), "GET", "/admin/providers", nil, nil)
	if !errors.Is(err, ErrNotRunning) || !strings.Contains(err.Error(), "docker compose up -d") {
		t.Fatalf("err = %v, want ErrNotRunning with a hint", err)
	}
}
