package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testKey = "gsk_test0123456789abcdefSECRET"

// clientBody is a request as a client might send it, with a field this
// package knows nothing about.
const clientBody = `{"model":"fast","messages":[{"role":"user","content":"<hi> & bye"}],"temperature":0.2,"x_custom":{"nested":[1,2,{"a":null}]}}`

func newReq(url, key string) Request {
	return Request{Provider: "groq", BaseURL: url, Model: "llama-3.3-70b-versatile", APIKey: key, Body: []byte(clientBody)}
}

func fakeUpstream(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// asError fails the test unless err is an *Error, and checks the key never
// leaked into it.
func asError(t *testing.T, err error) *Error {
	t.Helper()
	var ue *Error
	if !errors.As(err, &ue) {
		t.Fatalf("want *Error, got %T: %v", err, err)
	}
	if strings.Contains(err.Error(), testKey) || strings.Contains(ue.Message, testKey) {
		t.Fatalf("API key leaked into error: %v", err)
	}
	if ue.Provider != "groq" || ue.Model != "llama-3.3-70b-versatile" {
		t.Errorf("provider/model = %q/%q", ue.Provider, ue.Model)
	}
	return ue
}

func TestErrorClassification(t *testing.T) {
	future := strconv.FormatInt(time.Now().Add(30*time.Second).UnixMilli(), 10)
	tests := []struct {
		name    string
		status  int
		headers map[string]string
		body    string
		kind    Kind
		retry   time.Duration // exact, unless approx is set
		approx  bool
		msgHas  string
	}{
		// Rate limits.
		{name: "429 retry-after seconds", status: 429, headers: map[string]string{"Retry-After": "7"},
			body: `{"error":{"message":"Rate limit reached for model on tokens per minute (TPM). Please try again in 7s.","type":"tokens","code":"rate_limit_exceeded"}}`,
			kind: KindRateLimited, retry: 7 * time.Second},
		{name: "429 groq reset headers", status: 429,
			headers: map[string]string{"X-Ratelimit-Remaining-Requests": "10", "X-Ratelimit-Reset-Requests": "2m59.56s", "X-Ratelimit-Remaining-Tokens": "0", "X-Ratelimit-Reset-Tokens": "7.66s"},
			body:    `{"error":{"message":"Rate limit reached","type":"tokens","code":"rate_limit_exceeded"}}`,
			kind:    KindRateLimited, retry: 7660 * time.Millisecond},
		{name: "429 openrouter reset ms", status: 429, headers: map[string]string{"X-RateLimit-Limit": "20", "X-RateLimit-Remaining": "0", "X-RateLimit-Reset": future},
			body: `{"error":{"code":429,"message":"Rate limit exceeded: free-models-per-min."}}`,
			kind: KindRateLimited, retry: 30 * time.Second, approx: true},
		{name: "429 gemini retry info", status: 429,
			body: `[{"error":{"code":429,"message":"You exceeded your current quota","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"37s"}]}}]`,
			kind: KindRateLimited, retry: 37 * time.Second},
		{name: "429 workers ai no retry info", status: 429,
			body: `{"success":false,"errors":[{"code":3040,"message":"Capacity temporarily exceeded, please try again."}]}`,
			kind: KindRateLimited, retry: 0, msgHas: "Capacity temporarily exceeded"},

		// Context too long, in each provider's shape.
		{name: "context openai-style code", status: 400,
			body: `{"error":{"message":"This model's maximum context length is 8192 tokens.","type":"invalid_request_error","param":"messages","code":"context_length_exceeded"}}`,
			kind: KindContextTooLong},
		{name: "context groq", status: 400,
			body: `{"error":{"message":"Please reduce the length of the messages or completion.","type":"invalid_request_error","param":"messages","code":"context_length_exceeded"}}`,
			kind: KindContextTooLong},
		{name: "context groq 413 tpm", status: 413,
			body: `{"error":{"message":"Request too large for model on tokens per minute (TPM): Limit 6000, Requested 9000","type":"tokens","code":"rate_limit_exceeded"}}`,
			kind: KindContextTooLong},
		{name: "context openrouter metadata", status: 400,
			body: `{"error":{"code":400,"message":"Input exceeds the model's limit","metadata":{"error_type":"context_length_exceeded"}}}`,
			kind: KindContextTooLong},
		{name: "context gemini array", status: 400,
			body: `[{"error":{"code":400,"message":"The input token count (1048577) exceeds the maximum number of tokens allowed (1048576).","status":"INVALID_ARGUMENT"}}]`,
			kind: KindContextTooLong},
		// Workers AI doesn't publish this error: the Cloudflare envelope is
		// assumed and the wording is a guess, matched by "context window".
		{name: "context workers ai (assumed shape)", status: 400,
			body: `{"success":false,"errors":[{"code":5021,"message":"AiError: The estimated number of input and maximum output tokens (9000) exceeded this model context window limit (8192)."}]}`,
			kind: KindContextTooLong},
		{name: "context ollama", status: 400,
			body: `{"error":{"message":"the input length exceeds the context length","type":"invalid_request_error"}}`,
			kind: KindContextTooLong},

		// Bad requests.
		{name: "400 bad request", status: 400,
			body: `{"error":{"message":"'messages' must be an array","type":"invalid_request_error","param":"messages"}}`,
			kind: KindBadRequest, msgHas: "'messages' must be an array"},
		{name: "422 unprocessable", status: 422, body: `{"error":{"message":"bad tool schema"}}`, kind: KindBadRequest},

		// Provider errors, including a rejected provider key.
		{name: "500", status: 500, body: `{"error":{"message":"internal error","type":"server_error"}}`, kind: KindProvider},
		{name: "502 html", status: 502, body: `<html><body>Bad Gateway</body></html>`, kind: KindProvider, msgHas: "Bad Gateway"},
		{name: "503 plain text", status: 503, body: `upstream connect error`, kind: KindProvider, msgHas: "upstream connect error"},
		{name: "401 echoes key", status: 401,
			body: `{"error":{"message":"Invalid API Key: ` + testKey + `","type":"invalid_request_error","code":"invalid_api_key"}}`,
			kind: KindProvider, msgHas: "provider rejected the API key"},
		{name: "403", status: 403, body: `{"error":{"code":403,"message":"Forbidden"}}`, kind: KindProvider, msgHas: "denied access"},
		{name: "gemini bad key is 400", status: 400,
			body: `[{"error":{"code":400,"message":"API key not valid. Please pass a valid API key.","status":"INVALID_ARGUMENT"}}]`,
			kind: KindProvider, msgHas: "provider rejected the API key"},
		{name: "404 model not found", status: 404, body: `{"error":{"message":"model not found","code":"model_not_found"}}`, kind: KindProvider},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tt.headers {
					w.Header().Set(k, v)
				}
				w.Header().Set("X-Test", "kept")
				w.WriteHeader(tt.status)
				io.WriteString(w, tt.body)
			})
			c := New(Options{})
			// Both entry points share the classification; check both.
			for _, mode := range []string{"complete", "stream"} {
				var err error
				if mode == "complete" {
					_, err = c.Complete(context.Background(), newReq(srv.URL, testKey))
				} else {
					_, err = c.Stream(context.Background(), newReq(srv.URL, testKey))
				}
				ue := asError(t, err)
				if ue.Kind != tt.kind {
					t.Errorf("%s: kind = %s, want %s (%v)", mode, ue.Kind, tt.kind, err)
				}
				if ue.Status != tt.status {
					t.Errorf("%s: status = %d, want %d", mode, ue.Status, tt.status)
				}
				if ue.InStream {
					t.Errorf("%s: InStream set for an HTTP error", mode)
				}
				if ue.Header.Get("X-Test") != "kept" {
					t.Errorf("%s: response headers not kept on error", mode)
				}
				if tt.approx {
					if ue.RetryAfter <= 0 || ue.RetryAfter > tt.retry {
						t.Errorf("%s: RetryAfter = %v, want (0, %v]", mode, ue.RetryAfter, tt.retry)
					}
				} else if ue.RetryAfter != tt.retry {
					t.Errorf("%s: RetryAfter = %v, want %v", mode, ue.RetryAfter, tt.retry)
				}
				if tt.msgHas != "" && !strings.Contains(ue.Message, tt.msgHas) {
					t.Errorf("%s: message %q does not contain %q", mode, ue.Message, tt.msgHas)
				}
			}
		})
	}
}

func TestComplete(t *testing.T) {
	var got struct {
		path, auth string
		body       map[string]json.RawMessage
	}
	srv := fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		got.path = r.URL.Path
		got.auth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&got.body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Ratelimit-Remaining-Requests", "999")
		io.WriteString(w, `{"id":"1","choices":[{"message":{"content":"hello"}}],"usage":{"prompt_tokens":3}}`)
	})

	req := newReq(srv.URL+"/openai/v1", testKey)
	req.Body = []byte(`{"model":"groq/llama-3.3-70b-versatile","stream":true,"stream_options":{"include_usage":true},"messages":[],"x_custom":{"nested":[1,2,{"a":null}]},"note":"<b>"}`)
	resp, err := New(Options{}).Complete(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/openai/v1/chat/completions" {
		t.Errorf("path = %q", got.path)
	}
	if got.auth != "Bearer "+testKey {
		t.Errorf("Authorization = %q", got.auth)
	}
	want := map[string]string{
		"model":    `"llama-3.3-70b-versatile"`,
		"stream":   `false`,
		"messages": `[]`,
		"x_custom": `{"nested":[1,2,{"a":null}]}`,
		"note":     `"<b>"`,
	}
	for k, v := range want {
		if string(got.body[k]) != v {
			t.Errorf("body[%s] = %s, want %s", k, got.body[k], v)
		}
	}
	if _, ok := got.body["stream_options"]; ok {
		t.Error("stream_options should be removed from a non-streaming request")
	}
	if resp.Status != 200 || !strings.Contains(string(resp.Body), `"hello"`) {
		t.Errorf("response = %d %s", resp.Status, resp.Body)
	}
	if resp.Header.Get("X-Ratelimit-Remaining-Requests") != "999" {
		t.Error("response headers not passed through")
	}
}

func TestCompleteErrorInsideOK(t *testing.T) {
	srv := fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"error":{"code":502,"message":"provider returned error"}}`)
	})
	_, err := New(Options{}).Complete(context.Background(), newReq(srv.URL, testKey))
	ue := asError(t, err)
	if ue.Kind != KindProvider || !ue.InStream || ue.Status != 200 {
		t.Errorf("got %+v", ue)
	}
}

func TestNoAuthorizationWithoutKey(t *testing.T) {
	var sawAuth []string
	srv := fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_, ok := r.Header["Authorization"]
		sawAuth = append(sawAuth, fmt.Sprint(ok))
		if r.Header.Get("Accept") == "text/event-stream" {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		io.WriteString(w, `{"choices":[]}`)
	})
	c := New(Options{})
	if _, err := c.Complete(context.Background(), newReq(srv.URL, "")); err != nil {
		t.Fatal(err)
	}
	s, err := c.Stream(context.Background(), newReq(srv.URL, ""))
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if strings.Join(sawAuth, ",") != "false,false" {
		t.Errorf("Authorization header present: %v", sawAuth)
	}
}

func TestStream(t *testing.T) {
	var body map[string]json.RawMessage
	srv := fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Ratelimit-Remaining-Tokens", "5000")
		f := w.(http.Flusher)
		io.WriteString(w, ": OPENROUTER PROCESSING\n\n")
		f.Flush()
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n")
		f.Flush()
		io.WriteString(w, "event: message\r\nid: 2\r\ndata: {\"choices\":[{\"delta\":\r\ndata: {\"content\":\"lo\"}}]}\r\n\r\n")
		io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	})

	s, err := New(Options{}).Stream(context.Background(), newReq(srv.URL, testKey))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if string(body["stream"]) != "true" || string(body["stream_options"]) != `{"include_usage":true}` || !s.UsageAdded {
		t.Errorf("stream=%s stream_options=%s UsageAdded=%v", body["stream"], body["stream_options"], s.UsageAdded)
	}
	if string(body["model"]) != `"llama-3.3-70b-versatile"` || string(body["temperature"]) != "0.2" {
		t.Errorf("body = %v", body)
	}
	if s.Header.Get("X-Ratelimit-Remaining-Tokens") != "5000" {
		t.Error("stream headers not passed through")
	}

	var events []string
	for {
		data, err := s.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, string(data))
	}
	want := []string{
		`{"choices":[{"delta":{"content":"Hel"}}]}`,
		"{\"choices\":[{\"delta\":\n{\"content\":\"lo\"}}]}",
		`{"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2}}`,
	}
	if strings.Join(events, "|") != strings.Join(want, "|") {
		t.Errorf("events =\n%q\nwant\n%q", events, want)
	}
}

func TestStreamKeepsClientStreamOptions(t *testing.T) {
	var body map[string]json.RawMessage
	srv := fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: [DONE]\n\n")
	})
	req := newReq(srv.URL, testKey)
	req.Body = []byte(`{"model":"fast","stream_options":{"include_usage":false}}`)
	s, err := New(Options{}).Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s.UsageAdded || string(body["stream_options"]) != `{"include_usage":false}` {
		t.Errorf("UsageAdded=%v stream_options=%s", s.UsageAdded, body["stream_options"])
	}
}

func TestStreamRetriesWithoutStreamOptions(t *testing.T) {
	calls := 0
	srv := fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), "stream_options") {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"Unknown parameter: stream_options"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: [DONE]\n\n")
	})
	s, err := New(Options{}).Stream(context.Background(), newReq(srv.URL, testKey))
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if calls != 2 || s.UsageAdded {
		t.Errorf("calls=%d UsageAdded=%v", calls, s.UsageAdded)
	}
}

func TestStreamInStreamError(t *testing.T) {
	srv := fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n")
		io.WriteString(w, `data: {"id":"gen-1","object":"chat.completion.chunk","error":{"code":429,"message":"Rate limit exceeded","metadata":{"error_type":"rate_limit_exceeded"}},"choices":[{"index":0,"delta":{"content":""},"finish_reason":"error"}]}`+"\n\n")
	})
	s, err := New(Options{}).Stream(context.Background(), newReq(srv.URL, testKey))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Next(); err != nil {
		t.Fatal(err)
	}
	data, err := s.Next()
	ue := asError(t, err)
	if ue.Kind != KindRateLimited || !ue.InStream || ue.Status != 200 {
		t.Errorf("got %+v", ue)
	}
	if !strings.Contains(string(data), `"finish_reason":"error"`) {
		t.Errorf("error event data not returned: %s", data)
	}
}

func TestStreamJSONInsteadOfEvents(t *testing.T) {
	srv := fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"error":{"message":"This model's maximum context length is 8192 tokens"}}`)
	})
	_, err := New(Options{}).Stream(context.Background(), newReq(srv.URL, testKey))
	if ue := asError(t, err); ue.Kind != KindContextTooLong {
		t.Errorf("got %+v", ue)
	}
}

func TestStreamInterrupted(t *testing.T) {
	srv := fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[]}\n\n")
		w.(http.Flusher).Flush()
		// Drop the connection mid-stream, without the chunked terminator.
		conn, _, _ := w.(http.Hijacker).Hijack()
		conn.Close()
	})
	s, err := New(Options{}).Stream(context.Background(), newReq(srv.URL, testKey))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Next(); err != nil {
		t.Fatal(err)
	}
	_, err = s.Next()
	if ue := asError(t, err); ue.Kind != KindProvider || !ue.InStream {
		t.Errorf("got %+v", ue)
	}
}

func TestTimeoutBeforeFirstByte(t *testing.T) {
	srv := fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		waitForDisconnect(r)
	})
	c := New(Options{FirstByteTimeout: 50 * time.Millisecond, ResponseTimeout: 50 * time.Millisecond})
	for _, mode := range []string{"stream", "complete"} {
		start := time.Now()
		var err error
		if mode == "stream" {
			_, err = c.Stream(context.Background(), newReq(srv.URL, testKey))
		} else {
			_, err = c.Complete(context.Background(), newReq(srv.URL, testKey))
		}
		ue := asError(t, err)
		if ue.Kind != KindProvider || ue.Status != 0 || !strings.Contains(ue.Message, "timeout") {
			t.Errorf("%s: got %+v", mode, ue)
		}
		if time.Since(start) > 2*time.Second {
			t.Errorf("%s: timeout took %v", mode, time.Since(start))
		}
	}
}

func TestNoTimeoutOnceStreaming(t *testing.T) {
	srv := fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		// Slower than the first-byte timeout, which must no longer apply.
		time.Sleep(200 * time.Millisecond)
		io.WriteString(w, "data: {\"choices\":[]}\n\ndata: [DONE]\n\n")
	})
	c := New(Options{FirstByteTimeout: 50 * time.Millisecond})
	s, err := c.Stream(context.Background(), newReq(srv.URL, testKey))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Next(); err != nil {
		t.Fatalf("stream cut off after headers: %v", err)
	}
}

func TestConnectionRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listens here now

	c := New(Options{})
	for _, mode := range []string{"complete", "stream"} {
		var err error
		if mode == "complete" {
			_, err = c.Complete(context.Background(), newReq("http://"+addr, testKey))
		} else {
			_, err = c.Stream(context.Background(), newReq("http://"+addr, testKey))
		}
		ue := asError(t, err)
		if ue.Kind != KindProvider || ue.Status != 0 || ue.Header != nil || ue.Err == nil {
			t.Errorf("%s: got %+v", mode, ue)
		}
	}
}

func TestBadClientBody(t *testing.T) {
	called := false
	srv := fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) { called = true })
	for _, body := range []string{``, `null`, `[1]`, `{"model":`} {
		req := newReq(srv.URL, testKey)
		req.Body = []byte(body)
		_, err := New(Options{}).Complete(context.Background(), req)
		if ue := asError(t, err); ue.Kind != KindBadRequest {
			t.Errorf("%q: got %+v", body, ue)
		}
	}
	if called {
		t.Error("upstream called with an invalid body")
	}
}

// Cancelling the caller's context (the client disconnecting) must stop the
// upstream request, and must not be reported as a provider failure.
func TestCancellationStopsUpstream(t *testing.T) {
	t.Run("while waiting for headers", func(t *testing.T) {
		stopped := make(chan struct{})
		srv := fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
			waitForDisconnect(r)
			close(stopped)
		})
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(50*time.Millisecond, cancel)
		_, err := New(Options{}).Complete(ctx, newReq(srv.URL, testKey))
		checkCancelled(t, err, stopped)
	})

	t.Run("mid-stream", func(t *testing.T) {
		stopped := make(chan struct{})
		srv := fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"choices\":[]}\n\n")
			w.(http.Flusher).Flush()
			waitForDisconnect(r)
			close(stopped)
		})
		ctx, cancel := context.WithCancel(context.Background())
		s, err := New(Options{}).Stream(ctx, newReq(srv.URL, testKey))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if _, err := s.Next(); err != nil {
			t.Fatal(err)
		}
		cancel()
		_, err = s.Next()
		checkCancelled(t, err, stopped)
	})

	t.Run("by Close", func(t *testing.T) {
		stopped := make(chan struct{})
		srv := fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.(http.Flusher).Flush()
			waitForDisconnect(r)
			close(stopped)
		})
		s, err := New(Options{}).Stream(context.Background(), newReq(srv.URL, testKey))
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
		select {
		case <-stopped:
		case <-time.After(2 * time.Second):
			t.Fatal("upstream request still running after Close")
		}
	})
}

// waitForDisconnect blocks until the client goes away. The body is read
// first because net/http only notices a closed connection (and cancels
// r.Context) once the request body has been consumed.
func waitForDisconnect(r *http.Request) {
	io.Copy(io.Discard, r.Body)
	<-r.Context().Done()
}

func checkCancelled(t *testing.T, err error, stopped <-chan struct{}) {
	t.Helper()
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	var ue *Error
	if errors.As(err, &ue) {
		t.Errorf("cancellation reported as a provider error: %v", ue)
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream request still running after cancel")
	}
}

func TestRetryAfterHTTPDate(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	h := http.Header{}
	h.Set("Retry-After", now.Add(90*time.Second).Format(http.TimeFormat))
	if got := retryAfter(h, now); got != 90*time.Second {
		t.Errorf("retryAfter = %v", got)
	}
}

func TestSafeMessage(t *testing.T) {
	tests := []struct{ in, want string }{
		{"bad key " + testKey, "bad key [redacted]"},
		{"Authorization: Bearer abc.def.ghi failed", "Authorization: [redacted] failed"},
		{"other key sk-or-v1-0123456789abcdef0123 rejected", "other key [redacted] rejected"},
		{"gemini AIzaSyA0123456789abcdefghijklmnop bad", "gemini [redacted] bad"},
		{"line\none\r\n\ttwo", "line one two"},
		{"keep task-management-something words", "keep task-management-something words"},
	}
	for _, tt := range tests {
		if got := safeMessage(tt.in, testKey); got != tt.want {
			t.Errorf("safeMessage(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	long := strings.Repeat("é", 400)
	if got := safeMessage(long, ""); len(got) > maxMessage+len("…") || !strings.HasSuffix(got, "…") {
		t.Errorf("long message not capped: %d bytes", len(got))
	}
}
