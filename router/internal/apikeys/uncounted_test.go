package apikeys

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// callAt is call with a chosen method and path.
func callAt(h http.Handler, method, path, auth string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// GET /v1/usage shows a key's own count, so reading it must not change the
// count, and must work for a key that has used up its day.
func TestUsageReadIsNotCountedAndWorksOverTheCap(t *testing.T) {
	s, _, _ := fixture(t)
	key := create(t, s, "mc-bot", 2)
	h := Require(s, echo)
	bearer := "Bearer " + key

	for i := 0; i < 5; i++ {
		if w := callAt(h, "GET", "/v1/usage", bearer); w.Code != http.StatusOK {
			t.Fatalf("usage read %d = %d", i, w.Code)
		}
	}
	if u, _ := s.Usage("mc-bot"); u.Used != 0 {
		t.Fatalf("used after 5 usage reads = %d, want 0", u.Used)
	}

	// Use the cap up: the key is refused for anything else...
	for i, want := range []int{200, 200, 429} {
		if w := call(h, bearer); w.Code != want {
			t.Fatalf("request %d = %d, want %d", i, w.Code, want)
		}
	}
	// ...but can still read its usage, and the count stays at the cap.
	if w := callAt(h, "GET", "/v1/usage", bearer); w.Code != http.StatusOK || w.Body.String() != "mc-bot" {
		t.Fatalf("usage read over the cap = %d %q", w.Code, w.Body)
	}
	if u, _ := s.Usage("mc-bot"); u.Used != 2 {
		t.Fatalf("used = %d, want 2", u.Used)
	}
}

// The exemption skips only the counting and the cap. It never lets an
// unknown, revoked or missing key in, and it is exactly one method and path.
func TestUsageExemptionIsNarrow(t *testing.T) {
	s, _, _ := fixture(t)
	key := create(t, s, "mc-bot", 100)
	h := Require(s, echo)
	bearer := "Bearer " + key

	for _, tc := range []struct{ name, auth string }{
		{"no key", ""},
		{"unknown key", "Bearer sk-mc-nobody-0123456789"},
		{"malformed", "Basic abc"},
	} {
		if w := callAt(h, "GET", "/v1/usage", tc.auth); w.Code != http.StatusUnauthorized {
			t.Errorf("%s = %d, want 401", tc.name, w.Code)
		}
	}

	counted := []struct{ method, path string }{
		{"HEAD", "/v1/usage"}, {"POST", "/v1/usage"}, {"GET", "/v1/usage/"},
		{"GET", "/v1/models"}, {"GET", "/v1/usage2"}, {"GET", "/v1"},
	}
	for i, tc := range counted {
		callAt(h, tc.method, tc.path, bearer)
		if u, _ := s.Usage("mc-bot"); u.Used != int64(i+1) {
			t.Errorf("%s %s: used = %d, want %d (should count)", tc.method, tc.path, u.Used, i+1)
		}
	}

	// A revoked key is refused even for the usage read.
	if _, _, err := s.Revoke(context.Background(), "mc-bot"); err != nil {
		t.Fatal(err)
	}
	if w := callAt(h, "GET", "/v1/usage", bearer); w.Code != http.StatusUnauthorized {
		t.Errorf("revoked key reading usage = %d, want 401", w.Code)
	}
}
