package providerkeys

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RyanCaudill04/ai-gateway/router/internal/store"
)

const testKey = "gsk_TEST_abcdefghijklmnop_wxyz"

func testCipher(t *testing.T, fill byte) *Cipher {
	t.Helper()
	c, err := NewCipher(bytes.Repeat([]byte{fill}, MasterKeySize))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func openStore(t *testing.T, path string) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	c := testCipher(t, 1)
	nonce, ct, err := c.Encrypt("groq", []byte(testKey))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct, []byte(testKey)) {
		t.Fatal("ciphertext contains the plaintext")
	}
	got, err := c.Decrypt("groq", nonce, ct)
	if err != nil || string(got) != testKey {
		t.Fatalf("Decrypt = %q, %v", got, err)
	}
}

func TestDecryptFailures(t *testing.T) {
	c := testCipher(t, 1)
	nonce, ct, err := c.Encrypt("groq", []byte(testKey))
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Clone(ct)
	tampered[0] ^= 1

	for _, tc := range []struct {
		name      string
		c         *Cipher
		provider  string
		nonce, ct []byte
	}{
		{"moved to another provider (AAD)", c, "gemini", nonce, ct},
		{"wrong master key", testCipher(t, 2), "groq", nonce, ct},
		{"tampered ciphertext", c, "groq", nonce, tampered},
		{"short nonce", c, "groq", nonce[:4], ct},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.c.Decrypt(tc.provider, tc.nonce, tc.ct)
			if !errors.Is(err, ErrDecrypt) || got != nil {
				t.Fatalf("Decrypt = %q, %v; want ErrDecrypt", got, err)
			}
		})
	}
}

func TestNoncesAreFresh(t *testing.T) {
	c := testCipher(t, 1)
	seen := map[string]bool{}
	cts := map[string]bool{}
	for i := 0; i < 50; i++ {
		nonce, ct, err := c.Encrypt("groq", []byte(testKey))
		if err != nil {
			t.Fatal(err)
		}
		if seen[string(nonce)] || cts[string(ct)] {
			t.Fatal("nonce or ciphertext repeated for the same key")
		}
		seen[string(nonce)], cts[string(ct)] = true, true
	}
}

func TestParseMasterKey(t *testing.T) {
	good := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	short := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 16))
	for _, tc := range []struct {
		name, value, wantErr string
	}{
		{"valid", good, ""},
		{"valid with trailing newline", good + "\n", ""},
		{"missing", "", "is not set"},
		{"blank", "   ", "is not set"},
		{"not base64", "this is not base64!!", "not valid base64"},
		{"wrong length", short, "16 bytes, want exactly 32"},
		{"raw hex", strings.Repeat("ab", 32), "bytes, want exactly 32"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := MasterKeyFromEnv(func(k string) string {
				if k == MasterKeyEnv {
					return tc.value
				}
				return ""
			})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if strings.TrimSpace(tc.value) != "" && strings.Contains(err.Error(), strings.TrimSpace(tc.value)) {
				t.Fatalf("error repeats the master key: %v", err)
			}
			if !strings.Contains(err.Error(), "openssl rand -base64 32") {
				t.Errorf("error doesn't say how to generate a key: %v", err)
			}
		})
	}
}

func TestSetIsVisibleImmediatelyAndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "gateway.db")
	c := testCipher(t, 1)

	db := openStore(t, path)
	k, err := Load(ctx, db, c)
	if err != nil {
		t.Fatal(err)
	}
	if k.Has("groq") {
		t.Fatal("key present before Set")
	}
	info, err := k.Set(ctx, "groq", "  "+testKey+"\n")
	if err != nil {
		t.Fatal(err)
	}
	if info.Last4 != "wxyz" || info.CreatedAt == "" || info.UpdatedAt == "" {
		t.Fatalf("info = %+v", info)
	}
	if got, ok := k.Get("groq"); !ok || got != testKey {
		t.Fatalf("Get after Set = %q, %v", got, ok)
	}

	// Replacing keeps created_at.
	info2, err := k.Set(ctx, "groq", "gsk_SECOND_key_123456_9876")
	if err != nil {
		t.Fatal(err)
	}
	if info2.CreatedAt != info.CreatedAt || info2.Last4 != "9876" {
		t.Fatalf("after replace: %+v (first %+v)", info2, info)
	}
	if _, err := k.Set(ctx, "gemini", testKey); err != nil {
		t.Fatal(err)
	}
	db.Close()

	// The raw files must hold only ciphertext. Close checkpoints the WAL, but
	// read any leftover -wal file too.
	for _, f := range []string{path, path + "-wal"} {
		raw, err := os.ReadFile(f)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{testKey, "gsk_SECOND_key_123456_9876"} {
			if bytes.Contains(raw, []byte(secret)) {
				t.Fatalf("%s contains a plaintext key", filepath.Base(f))
			}
		}
	}

	db = openStore(t, path)
	defer db.Close()
	k, err = Load(ctx, db, c)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := k.Get("groq"); !ok || got != "gsk_SECOND_key_123456_9876" {
		t.Fatalf("Get after restart = %q, %v", got, ok)
	}
	if list := k.List(); len(list) != 2 || list[0].Provider != "gemini" || list[1].Provider != "groq" {
		t.Fatalf("List = %+v", list)
	}

	if err := k.Remove(ctx, "groq"); err != nil {
		t.Fatal(err)
	}
	if k.Has("groq") {
		t.Fatal("key still present after Remove")
	}
	if err := k.Remove(ctx, "groq"); !errors.Is(err, ErrNoKey) {
		t.Fatalf("second Remove = %v, want ErrNoKey", err)
	}
}

func TestUnreadableRowsDontStopStartup(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "gateway.db")
	db := openStore(t, path)
	defer db.Close()

	k, err := Load(ctx, db, testCipher(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Set(ctx, "groq", testKey); err != nil {
		t.Fatal(err)
	}
	// Copy groq's encrypted row to gemini: the AAD must make it useless.
	if _, err := db.Writer().Exec(`INSERT INTO provider_keys
		SELECT 'gemini', ciphertext, nonce, last4, created_at, updated_at FROM provider_keys WHERE provider = 'groq'`); err != nil {
		t.Fatal(err)
	}

	same, err := Load(ctx, db, testCipher(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	if !same.Has("groq") || same.Has("gemini") {
		t.Fatalf("moved row: groq=%v gemini=%v, want true/false", same.Has("groq"), same.Has("gemini"))
	}
	if info, _ := same.Info("gemini"); !info.Unreadable {
		t.Fatal("moved row not marked unreadable")
	}

	wrong, err := Load(ctx, db, testCipher(t, 2))
	if err != nil {
		t.Fatalf("wrong master key stopped Load: %v", err)
	}
	if wrong.Has("groq") || wrong.Has("gemini") {
		t.Fatal("a key decrypted under the wrong master key")
	}
	// Re-registering fixes it.
	if _, err := wrong.Set(ctx, "groq", testKey); err != nil || !wrong.Has("groq") {
		t.Fatalf("re-register: %v", err)
	}
}

func TestCleanKey(t *testing.T) {
	for _, tc := range []struct {
		in, want, wantErr string
	}{
		{testKey + "\n", testKey, ""},
		{"", "", "empty"},
		{"\n", "", "empty"},
		{"x7Q2zK", "", "shorter"},
		{"abc def ghijklmnop", "", "spaces"},
		{testKey + "\n" + testKey, "", "line breaks"},
		{strings.Repeat("a", MaxKeyLength+1), "", "longer"},
	} {
		got, err := CleanKey(tc.in)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("CleanKey(%.20q) err = %v, want %q", tc.in, err, tc.wantErr)
			}
			if err != nil && len(tc.in) > 4 && strings.Contains(err.Error(), strings.TrimSpace(tc.in)) {
				t.Errorf("CleanKey error repeats the key: %v", err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("CleanKey(%q) = %q, %v", tc.in, got, err)
		}
	}
}

func TestCheck(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			// Some providers quote the rejected key in the body.
			http.Error(w, `{"error":"invalid key `+r.Header.Get("Authorization")+`"}`, http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	if err := Check(context.Background(), srv.Client(), srv.URL+"/v1", testKey); err != nil {
		t.Fatalf("good key: %v", err)
	}
	if gotAuth != "Bearer "+testKey || gotPath != "/v1/models" {
		t.Fatalf("request: auth=%q path=%q", gotAuth, gotPath)
	}

	bad := "gsk_WRONG_key_000000000000"
	err := Check(context.Background(), srv.Client(), srv.URL+"/v1", bad)
	if err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), bad) {
		t.Fatalf("bad key: %v", err)
	}

	srv.Close()
	err = Check(context.Background(), nil, srv.URL+"/v1", bad)
	if err == nil || strings.Contains(err.Error(), bad) {
		t.Fatalf("unreachable: %v", err)
	}
}
