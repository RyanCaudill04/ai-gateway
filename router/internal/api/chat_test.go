package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RyanCaudill04/ai-gateway/router/internal/config"
	"github.com/RyanCaudill04/ai-gateway/router/internal/upstream"
)

// Text that must never reach the observer or the request summary.
const (
	secretPrompt     = "SECRET-PROMPT-TEXT"
	secretCompletion = "SECRET-COMPLETION-TEXT"
)

// chatYAML is the test config. Every provider points at the same fake
// upstream, under its own path prefix, so a test can tell which was called.
const chatYAML = `
providers:
  groq:
    base_url: ${UP}/groq/v1
    models:
      - id: big
        context_length: 1000
      - id: openai/gpt-oss-20b
        context_length: 1000
  gemini:
    base_url: ${UP}/gemini/v1
    models:
      - id: flash
        context_length: 1000
  ollama:
    base_url: ${UP}/ollama/v1
    local: true
    models:
      - id: tiny
        context_length: 1000
routes:
  fast: [groq/big, ollama/tiny]
  long-context: [gemini/flash]
`

// upstreamCall is what the fake provider received.
type upstreamCall struct {
	provider string // from the path prefix
	auth     string
	body     map[string]json.RawMessage
}

type observation struct {
	provider, model string
	header          http.Header
	err             *upstream.Error
}

type recorder struct {
	mu  sync.Mutex
	obs []observation
}

func (r *recorder) ObserveUpstream(provider, model string, h http.Header, err *upstream.Error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.obs = append(r.obs, observation{provider, model, h, err})
}

func (r *recorder) all() []observation {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.obs)
}

type fixture struct {
	router *httptest.Server
	calls  chan upstreamCall
	obs    *recorder
	sums   chan RequestSummary
}

// newFixture starts a fake upstream running h (after recording the call) and
// a router in front of it. enabled lists the enabled providers; none means all.
func newFixture(t *testing.T, h http.HandlerFunc, enabled ...string) *fixture {
	t.Helper()
	f := &fixture{calls: make(chan upstreamCall, 8), obs: &recorder{}, sums: make(chan RequestSummary, 8)}

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := upstreamCall{auth: r.Header.Get("Authorization")}
		c.provider, _, _ = strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		json.NewDecoder(r.Body).Decode(&c.body)
		f.calls <- c
		h(w, r)
	}))
	t.Cleanup(up.Close)

	cfg, err := config.Parse([]byte(chatYAML), func(string) string { return up.URL }, nil)
	if err != nil {
		t.Fatal(err)
	}
	isEnabled := func(p string) bool { return len(enabled) == 0 || slices.Contains(enabled, p) }
	keys := map[string]string{"groq": "k-groq", "gemini": "k-gemini"}

	f.router = httptest.NewServer(Chat(ChatDeps{
		Config:    cfg,
		Enabled:   isEnabled,
		Client:    upstream.New(upstream.Options{}),
		Key:       func(p string) (string, bool) { k, ok := keys[p]; return k, ok },
		Observer:  f.obs,
		OnSummary: func(s RequestSummary) { f.sums <- s },
	}))
	t.Cleanup(f.router.Close)
	return f
}

func (f *fixture) post(t *testing.T, ctx context.Context, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, "POST", f.router.URL, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// summary waits for the request's summary and checks it holds no content.
func (f *fixture) summary(t *testing.T) RequestSummary {
	t.Helper()
	select {
	case s := <-f.sums:
		if dump := fmt.Sprintf("%#v", s); strings.Contains(dump, secretPrompt) || strings.Contains(dump, secretCompletion) {
			t.Errorf("summary holds request or response text: %s", dump)
		}
		return s
	case <-time.After(3 * time.Second):
		t.Fatal("no request summary")
		return RequestSummary{}
	}
}

func (f *fixture) call(t *testing.T) upstreamCall {
	t.Helper()
	select {
	case c := <-f.calls:
		return c
	case <-time.After(3 * time.Second):
		t.Fatal("upstream was not called")
		return upstreamCall{}
	}
}

func (f *fixture) noCall(t *testing.T) {
	t.Helper()
	select {
	case c := <-f.calls:
		t.Errorf("upstream called (%s) but should not have been", c.provider)
	default:
	}
}

func chatBody(model string, extra string) string {
	return `{"model":"` + model + `","messages":[{"role":"user","content":"` + secretPrompt + `"}],"x_custom":{"a":[1,null]}` + extra + `}`
}

func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// errorBody decodes an error envelope.
func errorBody(t *testing.T, s string) map[string]any {
	t.Helper()
	var env struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal([]byte(s), &env); err != nil || env.Error == nil {
		t.Fatalf("not an error envelope: %v: %s", err, s)
	}
	return env.Error
}

func wantRouterHeaders(t *testing.T, resp *http.Response, provider, model string) {
	t.Helper()
	if p, m := resp.Header.Get("X-Router-Provider"), resp.Header.Get("X-Router-Model"); p != provider || m != model {
		t.Errorf("x-router-provider/model = %q/%q, want %q/%q", p, m, provider, model)
	}
}

const completion = `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"` + secretCompletion + `"}}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`

func TestChatNonStreaming(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Ratelimit-Remaining-Requests", "99")
		io.WriteString(w, completion)
	})

	resp := f.post(t, context.Background(), chatBody("fast", ""))
	if got := readAll(t, resp.Body); resp.StatusCode != 200 || got != completion {
		t.Fatalf("= %d %s, want the provider's body as-is", resp.StatusCode, got)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	wantRouterHeaders(t, resp, "groq", "big")

	c := f.call(t)
	if c.provider != "groq" || c.auth != "Bearer k-groq" || string(c.body["model"]) != `"big"` ||
		string(c.body["stream"]) != "false" || string(c.body["x_custom"]) != `{"a":[1,null]}` {
		t.Errorf("upstream got provider=%s auth=%q body=%v", c.provider, c.auth, c.body)
	}

	s := f.summary(t)
	want := RequestSummary{RequestedModel: "fast", Provider: "groq", Model: "big", Status: 200,
		Usage: &Usage{PromptTokens: 11, CompletionTokens: 7}, Attempts: 1}
	s.Latency = 0
	if s.Usage == nil || *s.Usage != *want.Usage || s.RequestedModel != want.RequestedModel || s.Provider != want.Provider ||
		s.Model != want.Model || s.Status != want.Status || s.Attempts != 1 || s.Stream || s.ErrorType != "" || s.ClientGone {
		t.Errorf("summary = %+v (usage %+v), want %+v", s, s.Usage, want)
	}

	obs := f.obs.all()
	if len(obs) != 1 || obs[0].provider != "groq" || obs[0].model != "big" || obs[0].err != nil ||
		obs[0].header.Get("X-Ratelimit-Remaining-Requests") != "99" {
		t.Errorf("observations = %+v", obs)
	}
}

func TestChatResolution(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		enabled  []string
		provider string // provider called / named in headers ("" = none)
		upModel  string // model ID sent upstream and in x-router-model
		status   int
		code     string // error code, for errors
	}{
		{name: "route takes its first entry", model: "fast", provider: "groq", upModel: "big", status: 200},
		{name: "route skips disabled entries", model: "fast", enabled: []string{"ollama"}, provider: "ollama", upModel: "tiny", status: 200},
		{name: "pinned", model: "ollama/tiny", provider: "ollama", upModel: "tiny", status: 200},
		{name: "pinned model ID with slashes", model: "groq/openai/gpt-oss-20b", provider: "groq", upModel: "openai/gpt-oss-20b", status: 200},
		{name: "unknown model", model: "nope", status: 404, code: "model_not_found"},
		{name: "unknown model of a known provider", model: "groq/nope", status: 404, code: "model_not_found"},
		{name: "bare model ID without provider", model: "openai/gpt-oss-20b", status: 404, code: "model_not_found"},
		{name: "pinned, provider disabled", model: "groq/big", enabled: []string{"ollama"}, provider: "groq", upModel: "big", status: 503, code: "provider_not_enabled"},
		{name: "route, no entry enabled", model: "long-context", enabled: []string{"ollama"}, status: 503, code: "no_enabled_provider"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, completion)
			}, tt.enabled...)
			resp := f.post(t, context.Background(), chatBody(tt.model, ""))
			body := readAll(t, resp.Body)
			if resp.StatusCode != tt.status {
				t.Fatalf("status = %d, want %d: %s", resp.StatusCode, tt.status, body)
			}
			wantRouterHeaders(t, resp, tt.provider, tt.upModel)
			if tt.code != "" {
				e := errorBody(t, body)
				if e["code"] != tt.code {
					t.Errorf("code = %v, want %s", e["code"], tt.code)
				}
				if tt.status == 404 && e["param"] != "model" {
					t.Errorf("param = %v, want model", e["param"])
				}
				f.noCall(t)
				if s := f.summary(t); s.Attempts != 0 || s.Status != tt.status || s.ErrorType == "" {
					t.Errorf("summary = %+v", s)
				}
				return
			}
			c := f.call(t)
			if c.provider != tt.provider || string(c.body["model"]) != `"`+tt.upModel+`"` {
				t.Errorf("upstream got %s %s", c.provider, c.body["model"])
			}
			// Local providers get no key; the others get theirs.
			if wantAuth := map[string]string{"groq": "Bearer k-groq"}[tt.provider]; c.auth != wantAuth {
				t.Errorf("Authorization = %q, want %q", c.auth, wantAuth)
			}
		})
	}
}

func TestChatMissingKeyIsAProviderError(t *testing.T) {
	cfg, err := config.Parse([]byte(chatYAML), func(string) string { return "http://127.0.0.1:1" }, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Enabled, but the key lookup says there is none (e.g. deleted since).
	h := Chat(ChatDeps{Config: cfg, Enabled: func(string) bool { return true }, Client: upstream.New(upstream.Options{})})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody("groq/big", ""))))
	if w.Code != http.StatusBadGateway || errorBody(t, w.Body.String())["type"] != "provider_error" {
		t.Errorf("= %d %s, want 502 provider_error", w.Code, w.Body)
	}
	if w.Header().Get("X-Router-Provider") != "groq" {
		t.Error("x-router-provider missing on error")
	}
}

func TestChatBadRequests(t *testing.T) {
	tests := []struct {
		name, body string
		status     int
		code       string
		param      string
	}{
		{name: "not JSON", body: `{"model":`, status: 400, code: "invalid_request"},
		{name: "array", body: `[{"model":"fast"}]`, status: 400, code: "invalid_request"},
		{name: "null", body: `null`, status: 400, code: "invalid_request", param: "model"},
		{name: "no model", body: `{"messages":[]}`, status: 400, code: "invalid_request", param: "model"},
		{name: "model not a string", body: `{"model":5}`, status: 400, code: "invalid_request", param: "model"},
		{name: "stream not a bool", body: `{"model":"fast","stream":"yes"}`, status: 400, code: "invalid_request", param: "stream"},
		{name: "too large", body: `{"model":"fast","pad":"` + strings.Repeat("a", MaxChatBody) + `"}`, status: 413, code: "request_too_large"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {})
			resp := f.post(t, context.Background(), tt.body)
			body := readAll(t, resp.Body)
			if resp.StatusCode != tt.status {
				t.Fatalf("status = %d, want %d: %s", resp.StatusCode, tt.status, body)
			}
			e := errorBody(t, body)
			if e["type"] != "invalid_request" || e["code"] != tt.code {
				t.Errorf("error = %v", e)
			}
			if p, _ := e["param"].(string); p != tt.param {
				t.Errorf("param = %q, want %q", p, tt.param)
			}
			f.noCall(t)
			f.summary(t)
		})
	}
}

// sse writes one event and flushes it.
func sse(w http.ResponseWriter, data string) {
	io.WriteString(w, "data: "+data+"\n\n")
	w.(http.Flusher).Flush()
}

const (
	chunk1    = `{"choices":[{"index":0,"delta":{"content":"` + secretCompletion + `"}}]}`
	chunk2    = `{"choices":[{"index":0,"delta":{"content":"!"},"finish_reason":"stop"}]}`
	usageOnly = `{"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`
)

// readEvent reads one SSE event ("data: ...\n\n") and returns its data.
func readEvent(br *bufio.Reader) (string, error) {
	line, err := br.ReadString('\n')
	if err != nil {
		return "", err
	}
	if blank, err := br.ReadString('\n'); err != nil || blank != "\n" {
		return "", fmt.Errorf("event not followed by a blank line: %q %v", blank, err)
	}
	return strings.TrimPrefix(strings.TrimSuffix(line, "\n"), "data: "), nil
}

func TestChatStreamingFlushesEachEvent(t *testing.T) {
	release := make(chan struct{})
	sent2 := make(chan struct{})
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		sse(w, chunk1)
		// Chunk 2 is held back until the client has seen chunk 1. If the
		// router buffers, the client sees nothing, so give up waiting after
		// a while and send the rest; the check below then fails clearly
		// instead of the test hanging.
		select {
		case <-release:
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
			return
		}
		close(sent2)
		sse(w, chunk2)
		sse(w, usageOnly)
		sse(w, "[DONE]")
	})

	resp := f.post(t, context.Background(), chatBody("fast", `,"stream":true`))
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d: %s", resp.StatusCode, readAll(t, resp.Body))
	}
	for k, v := range map[string]string{"Content-Type": "text/event-stream", "X-Accel-Buffering": "no"} {
		if got := resp.Header.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	wantRouterHeaders(t, resp, "groq", "big")

	br := bufio.NewReader(resp.Body)
	if got, err := readEvent(br); err != nil || got != chunk1 {
		t.Fatalf("first event = %q %v", got, err)
	}
	select {
	case <-sent2:
		t.Fatal("chunk 1 only reached the client after chunk 2 was sent: the router is buffering")
	default:
	}
	close(release)

	// The usage-only chunk is hidden: the client didn't ask for usage.
	if rest := readAll(t, br); rest != "data: "+chunk2+"\n\ndata: [DONE]\n\n" {
		t.Errorf("rest of stream = %q", rest)
	}

	c := f.call(t)
	if string(c.body["stream"]) != "true" || string(c.body["stream_options"]) != `{"include_usage":true}` {
		t.Errorf("upstream body stream=%s stream_options=%s", c.body["stream"], c.body["stream_options"])
	}
	s := f.summary(t)
	if !s.Stream || s.Status != 200 || s.Usage == nil || *s.Usage != (Usage{5, 2}) || s.ErrorType != "" || s.ClientGone {
		t.Errorf("summary = %+v usage=%v", s, s.Usage)
	}
}

func TestChatStreamingUsageWhenAsked(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		sse(w, chunk1)
		sse(w, usageOnly)
		sse(w, "[DONE]")
	})
	resp := f.post(t, context.Background(), chatBody("fast", `,"stream":true,"stream_options":{"include_usage":true}`))
	want := "data: " + chunk1 + "\n\ndata: " + usageOnly + "\n\ndata: [DONE]\n\n"
	if got := readAll(t, resp.Body); got != want {
		t.Errorf("stream = %q, want the usage chunk passed through", got)
	}
	if s := f.summary(t); s.Usage == nil || *s.Usage != (Usage{5, 2}) {
		t.Errorf("summary usage = %v", s.Usage)
	}
}

func TestChatUpstreamErrorBeforeStream(t *testing.T) {
	tests := []struct {
		name     string
		upstream http.HandlerFunc
		status   int
		typ      string
	}{
		{
			name: "429 status",
			upstream: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "30")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(429)
				io.WriteString(w, `{"error":{"message":"Rate limit reached","type":"tokens"}}`)
			},
			status: 429, typ: "rate_limited",
		},
		{
			name: "500 status",
			upstream: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(500)
			},
			status: 502, typ: "provider_error",
		},
		{
			// OpenRouter style: 200, then the error as the first event. Nothing
			// was forwarded yet, so it is still a normal error response.
			name: "error as first event",
			upstream: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				sse(w, `{"error":{"code":429,"message":"Rate limit exceeded"}}`)
			},
			status: 429, typ: "rate_limited",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, tt.upstream)
			resp := f.post(t, context.Background(), chatBody("groq/big", `,"stream":true`))
			body := readAll(t, resp.Body)
			if resp.StatusCode != tt.status {
				t.Fatalf("status = %d, want %d: %s", resp.StatusCode, tt.status, body)
			}
			if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q", ct)
			}
			e := errorBody(t, body)
			if e["type"] != tt.typ || e["provider"] != "groq" || e["model"] != "big" {
				t.Errorf("error = %v", e)
			}
			wantRouterHeaders(t, resp, "groq", "big")
			if s := f.summary(t); s.Status != tt.status || string(s.ErrorType) != tt.typ || s.Attempts != 1 {
				t.Errorf("summary = %+v", s)
			}
			obs := f.obs.all()
			if len(obs) == 0 || obs[len(obs)-1].err == nil || obs[len(obs)-1].provider != "groq" {
				t.Errorf("observer did not see the failure: %+v", obs)
			}
		})
	}
}

func TestChatErrorMidStream(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		sse(w, chunk1)
		sse(w, `{"error":{"code":429,"message":"Rate limit exceeded"}}`)
	})
	resp := f.post(t, context.Background(), chatBody("fast", `,"stream":true`))
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	br := bufio.NewReader(resp.Body)
	if got, err := readEvent(br); err != nil || got != chunk1 {
		t.Fatalf("first event = %q %v", got, err)
	}
	data, err := readEvent(br)
	if err != nil {
		t.Fatal(err)
	}
	if e := errorBody(t, data); e["type"] != "rate_limited" || e["provider"] != "groq" {
		t.Errorf("error event = %v", e)
	}
	// No [DONE] after an error: the stream just ends.
	if rest := readAll(t, br); rest != "" {
		t.Errorf("after the error event: %q", rest)
	}
	if s := f.summary(t); s.Status != 200 || s.ErrorType != "rate_limited" {
		t.Errorf("summary = %+v", s)
	}
	obs := f.obs.all()
	if len(obs) != 2 || obs[0].err != nil || obs[1].err == nil || obs[1].err.Kind != upstream.KindRateLimited {
		t.Errorf("observations = %+v, want headers then the in-stream error", obs)
	}
}

func TestChatClientDisconnectStopsUpstream(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			stopped := make(chan struct{})
			f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					sse(w, chunk1)
				}
				<-r.Context().Done()
				close(stopped)
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			extra := ""
			if stream {
				extra = `,"stream":true`
			} else {
				time.AfterFunc(100*time.Millisecond, cancel)
			}
			req, _ := http.NewRequestWithContext(ctx, "POST", f.router.URL, strings.NewReader(chatBody("fast", extra)))
			resp, err := http.DefaultClient.Do(req)
			if stream {
				if err != nil {
					t.Fatal(err)
				}
				if got, err := readEvent(bufio.NewReader(resp.Body)); err != nil || got != chunk1 {
					t.Fatalf("first event = %q %v", got, err)
				}
				cancel()
				resp.Body.Close()
			} else if err == nil {
				resp.Body.Close()
				t.Fatal("request succeeded, want it cancelled")
			}

			select {
			case <-stopped:
			case <-time.After(3 * time.Second):
				t.Fatal("upstream request still running after the client left")
			}
			s := f.summary(t)
			if !s.ClientGone || s.ErrorType != "" {
				t.Errorf("summary = %+v", s)
			}
			if !stream && s.Status != StatusClientClosed {
				t.Errorf("status = %d, want %d", s.Status, StatusClientClosed)
			}
			for _, o := range f.obs.all() {
				if o.err != nil {
					t.Errorf("client leaving was reported to the observer as a provider failure: %v", o.err)
				}
			}
		})
	}
}

func TestParseChunkUsage(t *testing.T) {
	tests := []struct {
		data      string
		usage     *Usage
		usageOnly bool
	}{
		{chunk1, nil, false},
		{usageOnly, &Usage{5, 2}, true},
		{`{"choices":[{"delta":{}}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`, &Usage{1, 2}, false},
		{`{"choices":[{"delta":{"content":"x"}}],"usage":null}`, nil, false},
		{completion, &Usage{11, 7}, false},
		{`{"choices":[],"usage":"weird"}`, nil, false},
	}
	for _, tt := range tests {
		u, only := parseChunkUsage([]byte(tt.data))
		if only != tt.usageOnly || (u == nil) != (tt.usage == nil) || (u != nil && *u != *tt.usage) {
			t.Errorf("parseChunkUsage(%s) = %v %v, want %v %v", tt.data, u, only, tt.usage, tt.usageOnly)
		}
	}
}
