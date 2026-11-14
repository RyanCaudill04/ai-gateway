package ratelimit

import (
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Providers report limits in one of two header families. The tracker
// recognises a family by its header names, not by what the provider is
// called in providers.yaml, because people name providers freely.
//
// Per-kind family (Groq, and OpenAI-style APIs):
//
//	x-ratelimit-limit-requests: 14400
//	x-ratelimit-remaining-requests: 14370
//	x-ratelimit-reset-requests: 2m59.56s   (time until reset, Go-style duration)
//	x-ratelimit-limit-tokens / -remaining-tokens / -reset-tokens
//
// The names don't say which window they describe: Groq documents requests as
// per *day* and tokens as per *minute*, while OpenAI uses both for per-minute
// limits. That part is decided by a convention chosen from the provider's
// base_url (see conventionFor). Headers with an explicit window suffix
// (x-ratelimit-remaining-requests-day) are honoured as named.
//
// Single-window family (OpenRouter, only on its own 429s):
//
//	X-RateLimit-Limit: 20
//	X-RateLimit-Remaining: 0
//	X-RateLimit-Reset: 1763035260000   (Unix milliseconds)
//
// These count requests. OpenRouter's free-model limits are per minute and per
// day, and the header doesn't say which one was hit, so the window is
// inferred from how far away the reset is (see windowFor).
//
// Anything unrecognised or malformed is ignored: a provider changing its
// headers must never break a chat request.

// convention says which window the bare per-kind headers describe.
type convention struct {
	requests, tokens Window
}

var (
	openAIConvention = convention{requests: Minute, tokens: Minute}
	groqConvention   = convention{requests: Day, tokens: Minute}
)

// conventionFor picks the convention from a provider's base_url, which is
// fixed by the provider (unlike its name in providers.yaml).
func conventionFor(baseURL string) convention {
	u, err := url.Parse(baseURL)
	if err != nil {
		return openAIConvention
	}
	host := strings.ToLower(u.Hostname())
	if host == "groq.com" || strings.HasSuffix(host, ".groq.com") {
		return groqConvention
	}
	return openAIConvention
}

// minuteWindowMax separates "per minute" from "per day" when only a reset
// time is known: a per-minute window always resets within a minute, so
// anything further away than this (allowing for clock skew) is a day window.
const minuteWindowMax = 2 * time.Minute

// maxReset rejects reset times further away than any window the tracker
// models (a day, plus slack). A larger value is a malformed header.
const maxReset = 48 * time.Hour

func windowFor(wait time.Duration) Window {
	if wait <= minuteWindowMax {
		return Minute
	}
	return Day
}

// parseHeaders turns a response's rate-limit headers into windows. It never
// fails: unknown or malformed values are skipped.
func parseHeaders(h http.Header, conv convention, now time.Time) []Limit {
	if len(h) == 0 {
		return nil
	}
	var out []Limit
	add := func(l Limit) {
		for i := range out {
			if out[i].Kind == l.Kind && out[i].Window == l.Window {
				out[i] = l // a later, more specific header wins
				return
			}
		}
		out = append(out, l)
	}

	// Per-kind family. Bare names first, so an explicit -minute/-day header
	// for the same window overrides them.
	for _, kind := range []Kind{Requests, Tokens} {
		bare := conv.requests
		if kind == Tokens {
			bare = conv.tokens
		}
		for _, v := range []struct {
			suffix string
			window Window
		}{{"", bare}, {"-minute", Minute}, {"-day", Day}} {
			name := string(kind) + v.suffix
			l, ok := parseWindow(h, "X-Ratelimit-Remaining-"+name, "X-Ratelimit-Limit-"+name, now)
			if !ok {
				continue
			}
			if d, ok := parseDelta(h.Get("X-Ratelimit-Reset-" + name)); ok {
				l.ResetsAt = now.Add(d)
			}
			l.Kind, l.Window = kind, v.window
			add(l)
		}
	}

	// Single-window family. Without a reset time the window can't be told
	// apart, so it is skipped.
	if l, ok := parseWindow(h, "X-Ratelimit-Remaining", "X-Ratelimit-Limit", now); ok {
		if at, ok := parseResetTime(h.Get("X-Ratelimit-Reset"), now); ok {
			l.Kind, l.Window, l.ResetsAt = Requests, windowFor(at.Sub(now)), at
			add(l)
		}
	}
	return out
}

// parseWindow reads a remaining/limit pair. A window is only recorded when
// remaining is readable: a limit alone says nothing about what's left.
func parseWindow(h http.Header, remainingName, limitName string, now time.Time) (Limit, bool) {
	rem, ok := parseCount(h.Get(remainingName))
	if !ok {
		return Limit{}, false
	}
	l := Limit{Remaining: rem, Max: Unknown, CheckedAt: now}
	if size, ok := parseCount(h.Get(limitName)); ok {
		l.Max = size
	}
	return l, true
}

// parseCount reads a non-negative count. Whole numbers written as floats
// ("17997.0") are accepted; anything else is malformed.
func parseCount(v string) (int64, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return n, n >= 0
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || f < 0 || f > math.MaxInt64/2 {
		return 0, false
	}
	return int64(f), true
}

// parseDelta reads a "time until reset": a Go-style duration as Groq sends it
// ("2m59.56s", "7.66s", "2h3m", "120ms") or a plain number of seconds.
func parseDelta(v string) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		f, ferr := strconv.ParseFloat(v, 64)
		if ferr != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		if f < 0 || f > maxReset.Seconds() {
			return 0, false
		}
		d = time.Duration(math.Round(f*1000)) * time.Millisecond
	}
	if d < 0 || d > maxReset {
		return 0, false
	}
	return d, true
}

// parseResetTime reads an absolute reset time. OpenRouter documents Unix
// milliseconds; Unix seconds and a seconds-from-now delta are accepted too,
// told apart by size (a Unix timestamp is far larger than any delta).
func parseResetTime(v string, now time.Time) (time.Time, bool) {
	v = strings.TrimSpace(v)
	n, err := strconv.ParseInt(v, 10, 64)
	var at time.Time
	switch {
	case err != nil:
		d, ok := parseDelta(v)
		if !ok {
			return time.Time{}, false
		}
		at = now.Add(d)
	case n >= 1e12:
		at = time.UnixMilli(n).UTC()
	case n >= 1e9:
		at = time.Unix(n, 0).UTC()
	default:
		d, ok := parseDelta(v)
		if !ok {
			return time.Time{}, false
		}
		at = now.Add(d)
	}
	// A reset in the past just means the window is full again; one absurdly
	// far ahead is a misread header.
	if at.Sub(now) > maxReset {
		return time.Time{}, false
	}
	return at, true
}
