package apierror

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/RyanCaudill04/ai-gateway/router/internal/upstream"
)

var t0 = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

// decode parses a body into the generic shape a client would see.
func decode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var env map[string]map[string]any
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("body is not an error envelope: %v\n%s", err, body)
	}
	if len(env) != 1 || env["error"] == nil {
		t.Fatalf(`body must be exactly {"error": {...}}: %s`, body)
	}
	return env["error"]
}

func TestEachTypeStatusHeadersAndBody(t *testing.T) {
	reset := t0.Add(42 * time.Second)
	tests := []struct {
		name       string
		err        *Error
		status     int
		retryAfter string
		wantType   string
		wantCode   string
		want       map[string]any // extra fields that must be present
		absent     []string       // fields that must be omitted
	}{
		{
			name: "rate_limited",
			err: RateLimited(Limit{Provider: "groq", Model: "llama-3.3-70b-versatile",
				LimitType: LimitTokens, Window: WindowMinute, ResetsAt: reset}),
			status: 429, retryAfter: "42", wantType: "rate_limited", wantCode: "rate_limit_exceeded",
			want: map[string]any{"provider": "groq", "model": "llama-3.3-70b-versatile",
				"limit_type": "tokens", "window": "minute", "resets_at": "2026-11-13T12:00:42Z"},
			absent: []string{"param", "attempts"},
		},
		{
			name:   "rate_limited, reset unknown: no Retry-After",
			err:    RateLimited(Limit{Provider: "gemini", Model: "gemini-2.5-flash"}),
			status: 429, wantType: "rate_limited", wantCode: "rate_limit_exceeded",
			want:   map[string]any{"provider": "gemini"},
			absent: []string{"limit_type", "window", "resets_at"},
		},
		{
			name: "rate_limited, service key cap",
			err: RateLimited(Limit{LimitType: LimitRequests, Window: WindowDay,
				ResetsAt: time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)}),
			status: 429, retryAfter: "43200", wantType: "rate_limited", wantCode: "rate_limit_exceeded",
			want:   map[string]any{"limit_type": "requests", "window": "day", "resets_at": "2026-11-14T00:00:00Z"},
			absent: []string{"provider", "model"},
		},
		{
			name:   "context_length_exceeded",
			err:    ContextLengthExceeded("groq", "llama", "prompt is too long"),
			status: 400, wantType: "context_length_exceeded", wantCode: "context_length_exceeded",
			want:   map[string]any{"provider": "groq", "model": "llama", "message": "prompt is too long"},
			absent: []string{"limit_type", "window", "resets_at", "attempts"},
		},
		{
			name:   "provider_error",
			err:    ProviderError("gemini", "gemini-2.5-flash", "HTTP 500"),
			status: 502, wantType: "provider_error", wantCode: "provider_error",
			want:   map[string]any{"provider": "gemini", "model": "gemini-2.5-flash"},
			absent: []string{"limit_type", "resets_at"},
		},
		{
			name:   "invalid_request",
			err:    InvalidRequest("messages is required", "messages"),
			status: 400, wantType: "invalid_request", wantCode: "invalid_request",
			want:   map[string]any{"param": "messages"},
			absent: []string{"provider", "model", "resets_at"},
		},
		{
			name:   "invalid_request, not found",
			err:    NotFound(),
			status: 404, wantType: "invalid_request", wantCode: "not_found",
			absent: []string{"param", "provider"},
		},
		{
			name:   "invalid_request, unknown model",
			err:    ModelNotFound(),
			status: 404, wantType: "invalid_request", wantCode: "model_not_found",
			want:   map[string]any{"param": "model"},
			absent: []string{"provider"},
		},
		{
			name:   "invalid_request, body too large",
			err:    RequestTooLarge(20 << 20),
			status: 413, wantType: "invalid_request", wantCode: "request_too_large",
			want: map[string]any{"message": "The request body is larger than the 20 MiB limit."},
		},
		{
			name:   "invalid_request, method not allowed",
			err:    MethodNotAllowed(),
			status: 405, wantType: "invalid_request", wantCode: "method_not_allowed",
		},
		{
			name:   "unauthorized",
			err:    Unauthorized("Sign-in required."),
			status: 401, wantType: "unauthorized", wantCode: "unauthorized",
			absent: []string{"param", "provider"},
		},
		{
			name:   "unauthorized, forbidden",
			err:    Forbidden("forbidden", "Not allowed."),
			status: 403, wantType: "unauthorized", wantCode: "forbidden",
		},
		{
			name:   "gateway unavailable",
			err:    Unavailable("auth_not_configured", "Not configured."),
			status: 503, wantType: "provider_error", wantCode: "auth_not_configured",
		},
		{
			name: "all_providers_exhausted, all rate limited",
			err: AllProvidersExhausted([]Attempt{
				{Provider: "groq", Model: "a", Type: TypeRateLimited, ResetsAt: t0.Add(90 * time.Second)},
				{Provider: "gemini", Model: "b", Type: TypeRateLimited, ResetsAt: reset},
			}),
			status: 429, retryAfter: "42", wantType: "all_providers_exhausted", wantCode: "all_providers_exhausted",
			want:   map[string]any{"resets_at": "2026-11-13T12:00:42Z"},
			absent: []string{"provider", "model"},
		},
		{
			name: "all_providers_exhausted, mixed",
			err: AllProvidersExhausted([]Attempt{
				{Provider: "groq", Model: "a", Type: TypeRateLimited},
				{Provider: "gemini", Model: "b", Type: TypeProviderError},
			}),
			status: 503, wantType: "all_providers_exhausted", wantCode: "all_providers_exhausted",
			absent: []string{"resets_at"},
		},
		{
			name:   "all_providers_exhausted, nothing to try",
			err:    AllProvidersExhausted(nil),
			status: 503, wantType: "all_providers_exhausted", wantCode: "all_providers_exhausted",
			absent: []string{"attempts", "resets_at"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			WriteAt(w, tt.err, t0)

			if w.Code != tt.status {
				t.Errorf("status = %d, want %d", w.Code, tt.status)
			}
			if got := w.Header().Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q", got)
			}
			if got := w.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q", got)
			}
			if got := w.Header().Get("Retry-After"); got != tt.retryAfter {
				t.Errorf("Retry-After = %q, want %q", got, tt.retryAfter)
			}

			e := decode(t, w.Body.Bytes())
			// The four fields the OpenAI SDKs read.
			if e["type"] != tt.wantType || e["code"] != tt.wantCode {
				t.Errorf("type/code = %v/%v, want %s/%s", e["type"], e["code"], tt.wantType, tt.wantCode)
			}
			if msg, _ := e["message"].(string); msg == "" {
				t.Error("message is empty")
			}
			for k, v := range tt.want {
				if e[k] != v {
					t.Errorf("%s = %v, want %v", k, e[k], v)
				}
			}
			for _, k := range tt.absent {
				if _, ok := e[k]; ok {
					t.Errorf("%s should be omitted, got %v", k, e[k])
				}
			}
		})
	}
}

func TestRateLimitedMessages(t *testing.T) {
	tests := []struct {
		l    Limit
		want string
	}{
		{Limit{Provider: "groq", Model: "llama", LimitType: LimitRequests, Window: WindowDay}, "groq/llama hit its daily request limit."},
		{Limit{Provider: "groq", LimitType: LimitTokens, Window: WindowMinute}, "groq hit its per-minute token limit."},
		{Limit{Provider: "groq", Model: "llama"}, "groq/llama hit its rate limit."},
		{Limit{LimitType: LimitRequests, Window: WindowDay, ResetsAt: t0}, "This API key has reached its daily request limit. It resets at 2026-11-13T12:00:00Z."},
	}
	for _, tt := range tests {
		if got := RateLimited(tt.l).Message; got != tt.want {
			t.Errorf("message = %q, want %q", got, tt.want)
		}
	}
}

func TestResetsAtIsUTCSeconds(t *testing.T) {
	loc := time.FixedZone("X", 3600)
	e := RateLimited(Limit{ResetsAt: time.Date(2026, 11, 13, 13, 0, 0, 999, loc)})
	if got := decode(t, e.Body())["resets_at"]; got != "2026-11-13T12:00:00Z" {
		t.Errorf("resets_at = %v", got)
	}
}

func TestRetryAfterRoundsUp(t *testing.T) {
	w := httptest.NewRecorder()
	WriteAt(w, RateLimited(Limit{ResetsAt: t0.Add(1500 * time.Millisecond)}), t0)
	if got := w.Header().Get("Retry-After"); got != "2" {
		t.Errorf("Retry-After = %q, want 2", got)
	}
	w = httptest.NewRecorder()
	WriteAt(w, RateLimited(Limit{ResetsAt: t0.Add(-time.Second)}), t0)
	if got := w.Header().Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After for a past reset = %q, want 1", got)
	}
}

func TestAllProvidersExhaustedListsAttempts(t *testing.T) {
	reset := t0.Add(time.Hour)
	e := AllProvidersExhausted([]Attempt{
		{Provider: "groq", Model: "llama-3.3-70b-versatile", Type: TypeRateLimited, Message: "slow down",
			LimitType: LimitRequests, Window: WindowDay, ResetsAt: reset},
		{Provider: "gemini", Model: "gemini-2.5-flash", Type: TypeProviderError, Message: "HTTP 500"},
	})
	got := decode(t, e.Body())["attempts"].([]any)
	if len(got) != 2 {
		t.Fatalf("attempts = %v", got)
	}
	first, second := got[0].(map[string]any), got[1].(map[string]any)
	want := map[string]any{"provider": "groq", "model": "llama-3.3-70b-versatile", "type": "rate_limited",
		"message": "slow down", "limit_type": "requests", "window": "day", "resets_at": "2026-11-13T13:00:00Z"}
	for k, v := range want {
		if first[k] != v {
			t.Errorf("attempt 0 %s = %v, want %v", k, first[k], v)
		}
	}
	for _, k := range []string{"limit_type", "window", "resets_at"} {
		if _, ok := second[k]; ok {
			t.Errorf("attempt 1 %s should be omitted", k)
		}
	}
	if second["type"] != "provider_error" {
		t.Errorf("attempt 1 type = %v", second["type"])
	}
}

func TestSSEIsTheSameObjectAsTheBody(t *testing.T) {
	e := RateLimited(Limit{Provider: "groq", Model: "llama", ResetsAt: t0.Add(time.Minute)})
	sse := string(e.SSE())
	if !strings.HasPrefix(sse, "data: {") || !strings.HasSuffix(sse, "}\n\n") {
		t.Fatalf("not an SSE data event: %q", sse)
	}
	payload := strings.TrimSuffix(strings.TrimPrefix(sse, "data: "), "\n\n")
	if payload != string(e.Body()) {
		t.Errorf("SSE payload differs from Body:\n%s\n%s", payload, e.Body())
	}
	if strings.Contains(payload, "\n") {
		t.Error("payload must be one line, or the event would be split")
	}
	if got := decode(t, []byte(payload)); got["type"] != "rate_limited" || got["provider"] != "groq" {
		t.Errorf("payload = %v", got)
	}
}

func TestFromUpstream(t *testing.T) {
	hint := LimitHint{LimitType: LimitRequests, Window: WindowDay}
	tests := []struct {
		name     string
		ue       *upstream.Error
		hint     LimitHint
		status   int
		wantType Type
		want     map[string]any
		absent   []string
	}{
		{
			name:   "rate limited with reset and hint",
			ue:     &upstream.Error{Kind: upstream.KindRateLimited, Provider: "groq", Model: "llama", Status: 429, Message: "x", RetryAfter: 30 * time.Second},
			hint:   hint,
			status: 429, wantType: TypeRateLimited,
			want: map[string]any{"provider": "groq", "model": "llama", "limit_type": "requests", "window": "day", "resets_at": "2026-11-13T12:00:30Z"},
		},
		{
			name:   "rate limited, nothing known",
			ue:     &upstream.Error{Kind: upstream.KindRateLimited, Provider: "gemini", Model: "flash", Status: 429},
			status: 429, wantType: TypeRateLimited,
			want:   map[string]any{"provider": "gemini"},
			absent: []string{"limit_type", "window", "resets_at"},
		},
		{
			name:   "context too long",
			ue:     &upstream.Error{Kind: upstream.KindContextTooLong, Provider: "groq", Model: "llama", Status: 413, Message: "reduce the length"},
			status: 400, wantType: TypeContextLengthExceeded,
			want:   map[string]any{"provider": "groq", "message": "reduce the length"},
			absent: []string{"resets_at"},
		},
		{
			name:   "bad request",
			ue:     &upstream.Error{Kind: upstream.KindBadRequest, Provider: "groq", Model: "llama", Status: 400, Message: "bad role"},
			status: 400, wantType: TypeInvalidRequest,
			want: map[string]any{"provider": "groq", "message": "bad role"},
		},
		{
			name:   "provider error",
			ue:     &upstream.Error{Kind: upstream.KindProvider, Provider: "groq", Model: "llama", Status: 500, Message: "boom"},
			status: 502, wantType: TypeProviderError,
			want: map[string]any{"provider": "groq", "message": "boom"},
		},
		{
			name:   "network failure (status 0) is a provider error",
			ue:     &upstream.Error{Kind: upstream.KindProvider, Provider: "ollama", Model: "m", Message: "timeout"},
			status: 502, wantType: TypeProviderError,
		},
		{
			name:   "error reported inside a 200 stream",
			ue:     &upstream.Error{Kind: upstream.KindProvider, Provider: "groq", Model: "llama", Status: 200, InStream: true, Message: "overloaded"},
			status: 502, wantType: TypeProviderError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := fromUpstream(tt.ue, tt.hint, t0)
			if e.Type != tt.wantType || e.Status != tt.status {
				t.Errorf("type/status = %s/%d, want %s/%d", e.Type, e.Status, tt.wantType, tt.status)
			}
			got := decode(t, e.Body())
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("%s = %v, want %v", k, got[k], v)
				}
			}
			for _, k := range tt.absent {
				if _, ok := got[k]; ok {
					t.Errorf("%s should be omitted", k)
				}
			}
		})
	}
}

func TestAttemptFrom(t *testing.T) {
	ue := &upstream.Error{Kind: upstream.KindRateLimited, Provider: "groq", Model: "llama", Message: "slow", RetryAfter: time.Minute}
	a := attemptFrom(ue, LimitHint{LimitType: LimitTokens, Window: WindowMinute}, t0)
	want := Attempt{Provider: "groq", Model: "llama", Type: TypeRateLimited, Message: "slow",
		LimitType: LimitTokens, Window: WindowMinute, ResetsAt: t0.Add(time.Minute)}
	if a != want {
		t.Errorf("attempt = %+v, want %+v", a, want)
	}
	// The hint only describes rate limits.
	ue = &upstream.Error{Kind: upstream.KindProvider, Provider: "groq", Model: "llama"}
	if a := attemptFrom(ue, LimitHint{LimitType: LimitTokens}, t0); a.LimitType != "" || a.Type != TypeProviderError {
		t.Errorf("attempt = %+v", a)
	}
}

func TestRoutes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	h := Routes(mux)

	tests := []struct {
		method, path string
		status       int
		code         string
		allow        string
	}{
		{"GET", "/v1/models", 200, "", ""},
		{"GET", "/nope", 404, "not_found", ""},
		{"POST", "/v1/models", 405, "method_not_allowed", "GET, HEAD"},
	}
	for _, tt := range tests {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tt.method, tt.path, nil))
		if w.Code != tt.status {
			t.Errorf("%s %s = %d, want %d", tt.method, tt.path, w.Code, tt.status)
		}
		if tt.code == "" {
			continue
		}
		if got := decode(t, w.Body.Bytes())["code"]; got != tt.code {
			t.Errorf("%s %s code = %v, want %s", tt.method, tt.path, got, tt.code)
		}
		if got := w.Header().Get("Allow"); got != tt.allow {
			t.Errorf("%s %s Allow = %q, want %q", tt.method, tt.path, got, tt.allow)
		}
	}
}
