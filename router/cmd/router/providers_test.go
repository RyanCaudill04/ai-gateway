package main

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
	"testing"

	"github.com/RyanCaudill04/ai-gateway/router/internal/admin"
	"github.com/RyanCaudill04/ai-gateway/router/internal/config"
	"github.com/RyanCaudill04/ai-gateway/router/internal/providerkeys"
	"github.com/RyanCaudill04/ai-gateway/router/internal/store"
)

const cliTestKey = "gsk_CLI_test_key_abcdef_5678"

func testKeys(t *testing.T) *providerkeys.Keys {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	c, err := providerkeys.NewCipher(bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	keys, err := providerkeys.Load(context.Background(), db, c)
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

// cliFixture runs the real admin handler behind a test server, with a fake
// provider (for --test) that accepts only cliTestKey.
func cliFixture(t *testing.T) (*admin.Client, *providerkeys.Keys) {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+cliTestKey {
			http.Error(w, "bad key", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(upstream.Close)
	cfg, err := config.Parse([]byte(strings.Replace(testProviders, "https://groq.example/v1", upstream.URL+"/v1", 1)), os.Getenv, nil)
	if err != nil {
		t.Fatal(err)
	}
	keys := testKeys(t)
	apiKeys, _ := testAPIKeys(t)
	srv := httptest.NewServer(newAdminHandler(cfg, keys, providerEnabled(cfg, keys.Has), apiKeys))
	t.Cleanup(srv.Close)
	return admin.NewClient(strings.TrimPrefix(srv.URL, "http://")), keys
}

func runCLI(t *testing.T, c *admin.Client, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err = runProviders(context.Background(), c, args, strings.NewReader(stdin), &out, &errOut)
	return out.String(), errOut.String(), err
}

func TestProvidersCLI(t *testing.T) {
	c, keys := cliFixture(t)
	wrongKey := "gsk_WRONG_key_000000_9999"

	// Everything the CLI printed or returned, checked for keys at the end.
	var seen strings.Builder
	cli := func(stdin string, args ...string) (string, error) {
		out, errOut, err := runCLI(t, c, stdin, args...)
		seen.WriteString(out + errOut)
		if err != nil {
			seen.WriteString(err.Error())
		}
		return out, err
	}

	// Unknown provider: rejected before reading the key, without repeating
	// the name (here, a key typed where the provider goes).
	if _, err := cli(cliTestKey, "set-key", wrongKey); err == nil || !strings.Contains(err.Error(), "providers.yaml has: groq, ollama") {
		t.Fatalf("unknown provider: %v", err)
	}
	if _, err := cli(cliTestKey, "set-key", "ollama"); err == nil || !strings.Contains(err.Error(), "local provider") {
		t.Fatalf("local provider: %v", err)
	}
	if _, err := cli("", "set-key", "groq"); err == nil || !strings.Contains(err.Error(), "no key on stdin") {
		t.Fatalf("empty stdin: %v", err)
	}

	// A failed --test saves nothing.
	if _, err := cli(wrongKey+"\n", "set-key", "--test", "groq"); err == nil || !strings.Contains(err.Error(), "nothing saved") {
		t.Fatalf("failed test: %v", err)
	}
	if keys.Has("groq") {
		t.Fatal("key saved despite a failed --test")
	}

	out, err := cli(cliTestKey+"\n", "set-key", "groq", "--test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "ends in 5678, tested OK") || !strings.Contains(out, "Enabled: yes") {
		t.Fatalf("set-key output: %q", out)
	}
	if got, _ := keys.Get("groq"); got != cliTestKey {
		t.Fatal("key not available to requests right after set-key")
	}

	out, err = cli("", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"PROVIDER", "groq", "...5678", "ollama", "not needed (local)"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q:\n%s", want, out)
		}
	}

	out, err = cli("", "remove-key", "groq")
	if err != nil || !strings.Contains(out, "Enabled: no") {
		t.Fatalf("remove-key: %q %v", out, err)
	}
	if _, err := cli("", "remove-key", "groq"); err == nil || !strings.Contains(err.Error(), "no key registered") {
		t.Fatalf("second remove-key: %v", err)
	}

	for _, args := range [][]string{{}, {"bogus"}, {"set-key"}, {"set-key", "--nope", "groq"}, {"list", "x"}, {"remove-key"}} {
		if _, err := cli("", args...); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Errorf("%v: err = %v, want usage", args, err)
		}
	}

	if s := seen.String(); strings.Contains(s, cliTestKey) || strings.Contains(s, wrongKey) {
		t.Fatalf("CLI output contained a key:\n%s", s)
	}
}

func TestProvidersCLIRouterNotRunning(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	_, _, err = runCLI(t, admin.NewClient(addr), cliTestKey, "set-key", "groq")
	if !errors.Is(err, admin.ErrNotRunning) || !strings.Contains(err.Error(), "router is not running") {
		t.Fatalf("err = %v", err)
	}
}
