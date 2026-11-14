package ratelimit

import (
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

// win is a short way to write an expected window. reset < 0 means no reset.
func win(kind Kind, window Window, remaining, max int64, reset time.Duration) Limit {
	l := Limit{Kind: kind, Window: window, Remaining: remaining, Max: max, CheckedAt: t0}
	if reset >= 0 {
		l.ResetsAt = t0.Add(reset)
	}
	return l
}

// sameLimit compares times as instants, ignoring their location.
func sameLimit(a, b Limit) bool {
	return a.Kind == b.Kind && a.Window == b.Window && a.Remaining == b.Remaining && a.Max == b.Max &&
		a.ResetsAt.Equal(b.ResetsAt) && a.CheckedAt.Equal(b.CheckedAt)
}

func sortLimits(ls []Limit) {
	slices.SortFunc(ls, func(a, b Limit) int {
		if a.Kind != b.Kind {
			if a.Kind < b.Kind {
				return -1
			}
			return 1
		}
		if a.Window < b.Window {
			return -1
		}
		if a.Window > b.Window {
			return 1
		}
		return 0
	})
}

func TestParseHeaders(t *testing.T) {
	groqOK := hdr( // the example from Groq's rate-limit docs
		"x-ratelimit-limit-requests", "14400",
		"x-ratelimit-limit-tokens", "18000",
		"x-ratelimit-remaining-requests", "14370",
		"x-ratelimit-remaining-tokens", "17997",
		"x-ratelimit-reset-requests", "2m59.56s",
		"x-ratelimit-reset-tokens", "7.66s",
	)
	ms := func(d time.Duration) string { return strconv.FormatInt(t0.Add(d).UnixMilli(), 10) }

	tests := []struct {
		name string
		h    http.Header
		conv convention
		want []Limit
	}{
		{"groq: requests are per day, tokens per minute", groqOK, groqConvention, []Limit{
			win(Requests, Day, 14370, 14400, 2*time.Minute+59560*time.Millisecond),
			win(Tokens, Minute, 17997, 18000, 7660*time.Millisecond),
		}},
		{"groq durations 1m26.4s and 2h3m", hdr(
			"x-ratelimit-limit-requests", "1000",
			"x-ratelimit-remaining-requests", "0",
			"x-ratelimit-reset-requests", "2h3m",
			"x-ratelimit-limit-tokens", "6000",
			"x-ratelimit-remaining-tokens", "12",
			"x-ratelimit-reset-tokens", "1m26.4s",
		), groqConvention, []Limit{
			win(Requests, Day, 0, 1000, 2*time.Hour+3*time.Minute),
			win(Tokens, Minute, 12, 6000, time.Minute+26400*time.Millisecond),
		}},
		{"same headers, OpenAI convention: both per minute", groqOK, openAIConvention, []Limit{
			win(Requests, Minute, 14370, 14400, 2*time.Minute+59560*time.Millisecond),
			win(Tokens, Minute, 17997, 18000, 7660*time.Millisecond),
		}},
		{"explicit window suffixes override the convention", hdr(
			"x-ratelimit-limit-requests-day", "14400",
			"x-ratelimit-remaining-requests-day", "14000",
			"x-ratelimit-reset-requests-day", "33011.38",
			"x-ratelimit-limit-tokens-minute", "60000",
			"x-ratelimit-remaining-tokens-minute", "59000",
			"x-ratelimit-reset-tokens-minute", "11.5",
		), openAIConvention, []Limit{
			win(Requests, Day, 14000, 14400, 33011*time.Second+380*time.Millisecond),
			win(Tokens, Minute, 59000, 60000, 11500*time.Millisecond),
		}},
		{"openrouter 429, minute window (reset in Unix ms)", hdr(
			"X-RateLimit-Limit", "20",
			"X-RateLimit-Remaining", "0",
			"X-RateLimit-Reset", ms(40*time.Second),
		), openAIConvention, []Limit{
			win(Requests, Minute, 0, 20, 40*time.Second),
		}},
		{"openrouter 429, day window", hdr(
			"X-RateLimit-Limit", "50",
			"X-RateLimit-Remaining", "0",
			"X-RateLimit-Reset", ms(9*time.Hour),
		), openAIConvention, []Limit{
			win(Requests, Day, 0, 50, 9*time.Hour),
		}},
		{"openrouter without a reset: window unknown, skipped", hdr(
			"X-RateLimit-Limit", "20",
			"X-RateLimit-Remaining", "0",
		), openAIConvention, nil},
		{"remaining without limit or reset", hdr(
			"x-ratelimit-remaining-tokens", "500",
		), groqConvention, []Limit{
			win(Tokens, Minute, 500, Unknown, -1),
		}},
		{"limit without remaining is skipped", hdr(
			"x-ratelimit-limit-requests", "1000",
			"x-ratelimit-reset-requests", "5s",
		), groqConvention, nil},
		{"malformed values are ignored, good ones kept", hdr(
			"x-ratelimit-limit-requests", "lots",
			"x-ratelimit-remaining-requests", "99",
			"x-ratelimit-reset-requests", "soon",
			"x-ratelimit-remaining-tokens", "-5",
			"x-ratelimit-reset-tokens", "1s",
		), groqConvention, []Limit{
			win(Requests, Day, 99, Unknown, -1),
		}},
		{"negative and absurd resets are ignored", hdr(
			"x-ratelimit-remaining-requests", "1",
			"x-ratelimit-reset-requests", "-3s",
			"x-ratelimit-remaining-tokens", "1",
			"x-ratelimit-reset-tokens", "9000h",
			"X-RateLimit-Remaining", "0",
			"X-RateLimit-Reset", "99999999999999999",
		), groqConvention, []Limit{
			win(Requests, Day, 1, Unknown, -1),
			win(Tokens, Minute, 1, Unknown, -1),
		}},
		{"float counts are accepted", hdr(
			"x-ratelimit-remaining-tokens", "17997.0",
			"x-ratelimit-limit-tokens", "1e400",
		), groqConvention, []Limit{
			win(Tokens, Minute, 17997, Unknown, -1),
		}},
		{"gemini / workers ai: no rate-limit headers", hdr(
			"Content-Type", "application/json",
			"Server", "scaffolding on HTTPServer2",
		), openAIConvention, nil},
		{"nil headers", nil, groqConvention, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseHeaders(tt.h, tt.conv, t0)
			sortLimits(got)
			sortLimits(tt.want)
			if !slices.EqualFunc(got, tt.want, sameLimit) {
				t.Errorf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestConventionFor(t *testing.T) {
	tests := []struct {
		url  string
		want convention
	}{
		{"https://api.groq.com/openai/v1", groqConvention},
		{"https://API.GROQ.COM/openai/v1", groqConvention},
		{"https://openrouter.ai/api/v1", openAIConvention},
		{"https://notgroq.com/v1", openAIConvention},
		{"http://localhost:11434/v1", openAIConvention},
		{"::not a url", openAIConvention},
	}
	for _, tt := range tests {
		if got := conventionFor(tt.url); got != tt.want {
			t.Errorf("conventionFor(%q) = %+v, want %+v", tt.url, got, tt.want)
		}
	}
}
