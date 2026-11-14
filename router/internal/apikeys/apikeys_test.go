package apikeys

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RyanCaudill04/ai-gateway/router/internal/store"
)

// clock is a settable time source for the store.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

var day1 = time.Date(2026, 11, 13, 15, 30, 0, 0, time.UTC)

func openDB(t *testing.T, path string) *store.Store {
	t.Helper()
	db, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// fixture opens a store on a fresh database with a fixed clock.
func fixture(t *testing.T) (*Store, *clock, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gateway.db")
	db := openDB(t, path)
	clk := &clock{t: day1}
	s, err := open(context.Background(), db, clk.now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Close(context.Background())
		db.Close()
	})
	return s, clk, path
}

// create makes a key the way the CLI does and returns the plain key.
func create(t *testing.T, s *Store, name string, dailyCap int64) string {
	t.Helper()
	key, prefix, hash, err := Generate(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(context.Background(), NewKey{Name: name, Prefix: prefix, Hash: hash, DailyCap: dailyCap}); err != nil {
		t.Fatal(err)
	}
	return key
}

// echo answers 200 with the calling service's name.
var echo = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	name, ok := NameFrom(r.Context())
	if !ok {
		http.Error(w, "no name in context", http.StatusInternalServerError)
		return
	}
	w.Write([]byte(name))
})

func call(h http.Handler, auth string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/v1/models", nil)
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

type envelope struct {
	Error struct {
		Message, Type, Code, Provider string
		LimitType                     string `json:"limit_type"`
		Window                        string
		ResetsAt                      string `json:"resets_at"`
	} `json:"error"`
}

func decode(t *testing.T, w *httptest.ResponseRecorder) envelope {
	t.Helper()
	var e envelope
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
		t.Fatalf("body is not JSON: %v: %s", err, w.Body)
	}
	return e
}

func TestValidateName(t *testing.T) {
	for name, ok := range map[string]bool{
		"mc-bot":                 true,
		"digest":                 true,
		"webui2":                 true,
		"a":                      true,
		strings.Repeat("a", 32):  true,
		"":                       false,
		strings.Repeat("a", 33):  false,
		"Mc-bot":                 false,
		"1bot":                   false,
		"-bot":                   false,
		"bot-":                   false,
		"mc--bot":                false,
		"mc_bot":                 false,
		"mc bot":                 false,
		"mc.bot":                 false,
		"bot\n":                  false,
		"sk-mc-bot-abcdefghijkl": true, // shaped like a key but a valid name
	} {
		err := ValidateName(name)
		if (err == nil) != ok {
			t.Errorf("ValidateName(%q) = %v, want ok=%v", name, err, ok)
		}
		if err != nil && name != "" && strings.Contains(err.Error(), name) {
			t.Errorf("ValidateName(%q) error repeats the name: %v", name, err)
		}
	}
}

func TestGenerate(t *testing.T) {
	key, prefix, hash, err := Generate("mc-bot")
	if err != nil {
		t.Fatal(err)
	}
	random, ok := strings.CutPrefix(key, "sk-mc-bot-")
	if !ok || len(random) != 52 || strings.ContainsAny(random, "-_=") {
		t.Errorf("key %q isn't sk-mc-bot-<52 base32 chars>", key)
	}
	if prefix != "sk-mc-bot-"+random[:4] || !validPrefix("mc-bot", prefix) {
		t.Errorf("prefix = %q", prefix)
	}
	if hash != Hash(key) {
		t.Error("hash isn't Hash(key)")
	}
	if len(key) > maxKeyLength {
		t.Errorf("key longer than the middleware accepts")
	}
	other, _, _, _ := Generate("mc-bot")
	if other == key {
		t.Error("two keys are equal")
	}
	if _, _, _, err := Generate("Bad Name"); !errors.Is(err, ErrInvalidName) {
		t.Errorf("Generate(bad name) = %v", err)
	}
	if long, _, _, _ := Generate(strings.Repeat("a", 32)); len(long) > maxKeyLength {
		t.Errorf("longest key is %d chars, over maxKeyLength", len(long))
	}
}

func TestRejectedRequests(t *testing.T) {
	s, _, _ := fixture(t)
	h := Require(s, echo)
	key := create(t, s, "mc-bot", 0)
	revoked := create(t, s, "old", 0)
	if _, _, err := s.Revoke(context.Background(), "old"); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	var message string
	for name, header := range map[string]string{
		"no header":          "",
		"empty bearer":       "Bearer ",
		"basic scheme":       "Basic " + key,
		"no scheme":          key,
		"control characters": "Bearer sk-mc-bot-\x01abc",
		"too long":           "Bearer " + strings.Repeat("a", maxKeyLength+1),
		"unknown key":        "Bearer sk-mc-bot-" + strings.Repeat("a", 52),
		"key with suffix":    "Bearer " + key + "x",
		"revoked key":        "Bearer " + revoked,
	} {
		w := call(h, header)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, w.Code)
			continue
		}
		e := decode(t, w)
		if e.Error.Type != "unauthorized" || e.Error.Code != "unauthorized" {
			t.Errorf("%s: error %+v", name, e.Error)
		}
		// Every rejection reads the same, so a caller can't tell which.
		if message == "" {
			message = e.Error.Message
		} else if e.Error.Message != message {
			t.Errorf("%s: message %q differs from %q", name, e.Error.Message, message)
		}
		if w.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%s: no WWW-Authenticate header", name)
		}
	}
	if strings.Contains(logs.String(), key[10:]) || strings.Contains(logs.String(), revoked[10:]) {
		t.Errorf("a key's secret part was logged:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "revoked key") || !strings.Contains(logs.String(), "service=old") {
		t.Errorf("revoked-key rejection not logged by name:\n%s", logs.String())
	}
	if n := s.List()[0].UsedToday; n != 0 {
		t.Errorf("rejected requests were counted: %d", n)
	}
}

func TestValidKeyReachesHandlerWithName(t *testing.T) {
	s, _, _ := fixture(t)
	key := create(t, s, "mc-bot", 0)
	h := Require(s, echo)
	for _, header := range []string{"Bearer " + key, "bearer " + key, "Bearer  " + key + " "} {
		w := call(h, header)
		if w.Code != http.StatusOK || w.Body.String() != "mc-bot" {
			t.Errorf("%q: %d %s", header, w.Code, w.Body)
		}
	}
	if u, _ := s.Usage("mc-bot"); u.Used != 3 || u.DailyCap != 0 {
		t.Errorf("usage = %+v, want 3 used, no cap", u)
	}
}

func TestNameFromEmptyContext(t *testing.T) {
	if _, ok := NameFrom(context.Background()); ok {
		t.Error("NameFrom on a bare context reported a name")
	}
	if name, ok := NameFrom(ContextWithName(context.Background(), "webui")); !ok || name != "webui" {
		t.Errorf("NameFrom = %q, %v", name, ok)
	}
}

func TestCreateAndRevokeApplyImmediately(t *testing.T) {
	s, _, _ := fixture(t)
	h := Require(s, echo)
	key, prefix, hash, _ := Generate("digest")
	if w := call(h, "Bearer "+key); w.Code != http.StatusUnauthorized {
		t.Fatalf("before create: %d", w.Code)
	}
	if _, err := s.Create(context.Background(), NewKey{Name: "digest", Prefix: prefix, Hash: hash}); err != nil {
		t.Fatal(err)
	}
	if w := call(h, "Bearer "+key); w.Code != http.StatusOK {
		t.Fatalf("right after create: %d", w.Code)
	}
	info, changed, err := s.Revoke(context.Background(), "digest")
	if err != nil || !changed || info.RevokedAt == "" {
		t.Fatalf("Revoke = %+v, %v, %v", info, changed, err)
	}
	if w := call(h, "Bearer "+key); w.Code != http.StatusUnauthorized {
		t.Fatalf("right after revoke: %d", w.Code)
	}
}

func TestNameRules(t *testing.T) {
	s, _, _ := fixture(t)
	ctx := context.Background()
	h := Require(s, echo)
	old := create(t, s, "mc-bot", 5)
	call(h, "Bearer "+old)
	call(h, "Bearer "+old)

	// Duplicate active name.
	_, prefix, hash, _ := Generate("mc-bot")
	if _, err := s.Create(ctx, NewKey{Name: "mc-bot", Prefix: prefix, Hash: hash}); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate active name: %v, want ErrExists", err)
	}

	// Revoke twice: the second changes nothing and isn't an error.
	if _, changed, err := s.Revoke(ctx, "mc-bot"); err != nil || !changed {
		t.Fatalf("first revoke: %v %v", changed, err)
	}
	info, changed, err := s.Revoke(ctx, "mc-bot")
	if err != nil || changed || info.RevokedAt == "" {
		t.Errorf("second revoke = %+v, %v, %v", info, changed, err)
	}
	if _, _, err := s.Revoke(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoke unknown: %v", err)
	}

	// Re-create a revoked name: the new key works, the old one never does,
	// and today's count carries over (rotation doesn't reset the cap).
	key, prefix, hash, _ := Generate("mc-bot")
	info, err = s.Create(ctx, NewKey{Name: "mc-bot", Prefix: prefix, Hash: hash, DailyCap: 3})
	if err != nil || info.RevokedAt != "" || info.UsedToday != 2 || info.DailyCap != 3 {
		t.Fatalf("re-create = %+v, %v", info, err)
	}
	if w := call(h, "Bearer "+old); w.Code != http.StatusUnauthorized {
		t.Errorf("old key after re-create: %d", w.Code)
	}
	if w := call(h, "Bearer "+key); w.Code != http.StatusOK {
		t.Errorf("new key: %d", w.Code)
	}
	if w := call(h, "Bearer "+key); w.Code != http.StatusTooManyRequests {
		t.Errorf("4th request of the day with cap 3: %d", w.Code)
	}
	if n := len(s.List()); n != 1 {
		t.Errorf("List has %d keys, want 1", n)
	}

	// Malformed creates.
	for name, k := range map[string]NewKey{
		"bad name":        {Name: "Bad", Prefix: "sk-Bad-abcd", Hash: Hash("x")},
		"prefix mismatch": {Name: "other", Prefix: "sk-mc-bot-abcd", Hash: Hash("y")},
		"whole key":       {Name: "other", Prefix: key, Hash: Hash("z")},
		"no hash":         {Name: "other", Prefix: "sk-other-abcd"},
		"negative cap":    {Name: "other", Prefix: "sk-other-abcd", Hash: Hash("w"), DailyCap: -1},
		"reused hash":     {Name: "other", Prefix: "sk-other-abcd", Hash: hash},
	} {
		if _, err := s.Create(ctx, k); err == nil {
			t.Errorf("%s: Create succeeded", name)
		}
	}
}

func TestCapExactUnderConcurrency(t *testing.T) {
	s, _, _ := fixture(t)
	const capN, callers = 25, 200
	key := create(t, s, "mc-bot", capN)
	h := Require(s, echo)

	var (
		wg              sync.WaitGroup
		mu              sync.Mutex
		ok, limited     int
		sample          *httptest.ResponseRecorder
		start           = make(chan struct{})
		unexpectedCodes []int
	)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			w := call(h, "Bearer "+key)
			mu.Lock()
			defer mu.Unlock()
			switch w.Code {
			case http.StatusOK:
				ok++
			case http.StatusTooManyRequests:
				limited++
				sample = w
			default:
				unexpectedCodes = append(unexpectedCodes, w.Code)
			}
		}()
	}
	close(start)
	wg.Wait()
	if ok != capN || limited != callers-capN || len(unexpectedCodes) > 0 {
		t.Fatalf("ok=%d limited=%d other=%v, want %d and %d", ok, limited, unexpectedCodes, capN, callers-capN)
	}

	e := decode(t, sample)
	if e.Error.Type != "rate_limited" || e.Error.Code != "rate_limit_exceeded" || e.Error.LimitType != "requests" ||
		e.Error.Window != "day" || e.Error.ResetsAt != "2026-11-14T00:00:00Z" || e.Error.Provider != "" {
		t.Errorf("429 body = %+v", e.Error)
	}
	// day1 is 15:30 UTC: 8.5 hours to midnight.
	if got := sample.Header().Get("Retry-After"); got != "30600" {
		t.Errorf("Retry-After = %q, want 30600", got)
	}
	if u, _ := s.Usage("mc-bot"); u.Used != capN {
		t.Errorf("Used = %d, want %d (rejected requests aren't counted)", u.Used, capN)
	}
}

func TestCapResetsOnNewUTCDay(t *testing.T) {
	s, clk, _ := fixture(t)
	key := create(t, s, "mc-bot", 2)
	h := Require(s, echo)
	for i, want := range []int{200, 200, 429} {
		if w := call(h, "Bearer "+key); w.Code != want {
			t.Fatalf("request %d: %d, want %d", i, w.Code, want)
		}
	}
	clk.set(time.Date(2026, 11, 13, 23, 59, 59, 0, time.UTC))
	if w := call(h, "Bearer "+key); w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "1" {
		t.Fatalf("one second before midnight: %d, Retry-After %q", w.Code, w.Header().Get("Retry-After"))
	}
	clk.set(time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC))
	if u, _ := s.Usage("mc-bot"); u.Used != 0 || !u.ResetsAt.Equal(time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("usage at midnight = %+v", u)
	}
	for i, want := range []int{200, 200, 429} {
		if w := call(h, "Bearer "+key); w.Code != want {
			t.Fatalf("day 2 request %d: %d, want %d", i, w.Code, want)
		}
	}
}

func TestKeysAndCountsSurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	db := openDB(t, path)
	clk := &clock{t: day1}
	s, err := open(context.Background(), db, clk.now)
	if err != nil {
		t.Fatal(err)
	}
	active := create(t, s, "mc-bot", 3)
	revoked := create(t, s, "old", 0)
	h := Require(s, echo)
	call(h, "Bearer "+active)
	call(h, "Bearer "+active)
	if _, _, err := s.Revoke(context.Background(), "old"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	db.Close()

	db = openDB(t, path)
	defer db.Close()
	s, err = open(context.Background(), db, clk.now)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	h = Require(s, echo)

	list := s.List()
	if len(list) != 2 || list[0].Name != "mc-bot" || list[0].UsedToday != 2 || list[0].DailyCap != 3 ||
		list[1].Name != "old" || list[1].RevokedAt == "" {
		t.Fatalf("after reopen: %+v", list)
	}
	if w := call(h, "Bearer "+revoked); w.Code != http.StatusUnauthorized {
		t.Errorf("revoked key after reopen: %d", w.Code)
	}
	for i, want := range []int{200, 429} {
		if w := call(h, "Bearer "+active); w.Code != want {
			t.Errorf("after reopen request %d: %d, want %d", i, w.Code, want)
		}
	}

	// A stale count from an earlier day isn't loaded as today's.
	s.Close(context.Background())
	clk.set(day1.Add(24 * time.Hour))
	s2, err := open(context.Background(), db, clk.now)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close(context.Background())
	if u, _ := s2.Usage("mc-bot"); u.Used != 0 {
		t.Errorf("next day after reopen: used %d", u.Used)
	}
}

func TestDatabaseHoldsNoPlainKey(t *testing.T) {
	s, _, path := fixture(t)
	key := create(t, s, "mc-bot", 10)
	call(Require(s, echo), "Bearer "+key)
	if err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	secret := key[len("sk-mc-bot-"):]
	hash := Hash(key)
	var found bool
	for _, p := range []string{path, path + "-wal"} {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if bytes.Contains(data, []byte(secret)) {
			t.Errorf("%s contains the key's secret part", filepath.Base(p))
		}
		if bytes.Contains(data, hash[:]) {
			found = true
		}
	}
	if !found {
		t.Error("the key's hash wasn't found in the database files (test isn't looking in the right place)")
	}
	// The stored hash is exactly SHA-256 of the key.
	var stored []byte
	if err := s.db.Reader().QueryRow(`SELECT key_hash FROM api_keys WHERE name = 'mc-bot'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(stored) != hex.EncodeToString(hash[:]) {
		t.Error("stored hash isn't SHA-256 of the key")
	}
}
