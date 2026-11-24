package apikeys

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RyanCaudill04/ai-gateway/router/internal/store"
)

func newStore(t *testing.T) (*Store, *store.Store) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return NewStore(db), db
}

func TestGenerate(t *testing.T) {
	key, err := Generate("portfolio")
	if err != nil {
		t.Fatal(err)
	}
	// sk-<name>-<52 characters>
	if !strings.HasPrefix(key, "sk-portfolio-") || len(key) != len("sk-portfolio-")+52 {
		t.Errorf("key = %q", key)
	}
	other, _ := Generate("portfolio")
	if key == other {
		t.Error("two keys are the same")
	}
	for _, bad := range []string{"", "Has Space", "-lead", "UPPER", strings.Repeat("a", 33)} {
		if _, err := Generate(bad); !errors.Is(err, ErrInvalidName) {
			t.Errorf("Generate(%q) error = %v, want ErrInvalidName", bad, err)
		}
	}
}

func TestCreateVerifyRevoke(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)

	key, err := s.Create(ctx, "site", nil)
	if err != nil {
		t.Fatal(err)
	}
	if name, err := s.Verify(ctx, key); err != nil || name != "site" {
		t.Fatalf("Verify = %q, %v", name, err)
	}
	if _, err := s.Verify(ctx, key+"x"); !errors.Is(err, ErrInvalid) {
		t.Errorf("wrong key: %v", err)
	}
	if _, err := s.Create(ctx, "site", nil); !errors.Is(err, ErrExists) {
		t.Errorf("creating an active name: %v", err)
	}

	// Revoking applies to the very next check.
	if err := s.Revoke(ctx, "site"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(ctx, key); !errors.Is(err, ErrInvalid) {
		t.Errorf("revoked key still works: %v", err)
	}
	if err := s.Revoke(ctx, "site"); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoking twice: %v", err)
	}
	if err := s.Revoke(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoking an unknown name: %v", err)
	}
}

func TestRotationReusesTheName(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	old, _ := s.Create(ctx, "site", nil)
	s.Revoke(ctx, "site")

	capN := 100
	fresh, err := s.Create(ctx, "site", &capN)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(ctx, old); !errors.Is(err, ErrInvalid) {
		t.Error("the old key works after rotation")
	}
	if name, err := s.Verify(ctx, fresh); err != nil || name != "site" {
		t.Errorf("new key: %q, %v", name, err)
	}
	list, _ := s.List(ctx)
	if len(list) != 1 || list[0].RevokedAt != nil || list[0].DailyCap == nil || *list[0].DailyCap != 100 {
		t.Errorf("list = %+v", list)
	}
}

func TestPlainKeyIsNeverStored(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	key, _ := s.Create(ctx, "site", nil)

	var n int
	err := db.Reader().QueryRow(`SELECT COUNT(*) FROM api_keys WHERE instr(prefix, ?) > 0 OR instr(CAST(key_hash AS TEXT), ?) > 0`, key, key).Scan(&n)
	if err != nil || n != 0 {
		t.Errorf("plain key found in the database (n=%d, err=%v)", n, err)
	}
	list, _ := s.List(ctx)
	if strings.Contains(list[0].Prefix, key[len(key)-10:]) {
		t.Errorf("prefix %q shows too much of the key", list[0].Prefix)
	}
}

type fakeVerifier struct{ err error }

func (f fakeVerifier) Verify(_ context.Context, key string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if key == "good" {
		return "svc", nil
	}
	return "", ErrInvalid
}

func TestRequire(t *testing.T) {
	var gotName string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotName, _ = NameFrom(r.Context())
	})
	for name, tc := range map[string]struct {
		header string
		v      Verifier
		want   int
	}{
		"valid":      {"Bearer good", fakeVerifier{}, 200},
		"lowercase":  {"bearer good", fakeVerifier{}, 200},
		"missing":    {"", fakeVerifier{}, 401},
		"malformed":  {"good", fakeVerifier{}, 401},
		"wrong key":  {"Bearer bad", fakeVerifier{}, 401},
		"db failure": {"Bearer good", fakeVerifier{err: errors.New("disk")}, 503},
	} {
		gotName = ""
		r := httptest.NewRequest("GET", "/v1/models", nil)
		if tc.header != "" {
			r.Header.Set("Authorization", tc.header)
		}
		w := httptest.NewRecorder()
		Require(tc.v, next).ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("%s: got %d, want %d", name, w.Code, tc.want)
		}
		if (tc.want == 200) != (gotName == "svc") {
			t.Errorf("%s: service name in context = %q", name, gotName)
		}
		if tc.want == 401 && w.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Errorf("%s: missing WWW-Authenticate", name)
		}
	}
}
