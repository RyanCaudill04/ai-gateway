package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/RyanCaudill04/ai-gateway/router/internal/admin"
	"github.com/RyanCaudill04/ai-gateway/router/internal/apikeys"
	"github.com/RyanCaudill04/ai-gateway/router/internal/auth"
)

// keysFixture runs the real admin and public handlers on one key store.
func keysFixture(t *testing.T) (*admin.Client, http.Handler) {
	t.Helper()
	cfg := testConfig(t)
	pkeys := testKeys(t)
	enabled := providerEnabled(cfg, pkeys.Has)
	apiKeys, _ := testAPIKeys(t) // holds "test-svc"
	srv := httptest.NewServer(newAdminHandler(cfg, pkeys, enabled, apiKeys))
	t.Cleanup(srv.Close)
	return admin.NewClient(strings.TrimPrefix(srv.URL, "http://")), newHandler(handlerDeps{authn: auth.Disabled(), cfg: cfg, enabled: enabled, apiKeys: apiKeys})
}

var keyPattern = regexp.MustCompile(`sk-[a-z0-9-]+-[a-z2-7]{52}`)

func TestKeysCLI(t *testing.T) {
	c, pub := keysFixture(t)

	// Everything printed or returned, except the one line create prints the
	// key on, is checked for keys and hashes at the end.
	var seen strings.Builder
	cli := func(args ...string) (string, error) {
		var out, errOut bytes.Buffer
		err := runKeys(context.Background(), c, args, &out, &errOut)
		seen.WriteString(errOut.String())
		if err != nil {
			seen.WriteString(err.Error())
		}
		return out.String(), err
	}
	models := func(key string) int {
		return do(pub, "GET", "/v1/models", "Authorization", "Bearer "+key).Code
	}

	out, err := cli("create", "mc-bot", "--daily-cap", "2")
	if err != nil {
		t.Fatal(err)
	}
	key := strings.TrimSpace(out)
	if !keyPattern.MatchString(key) || !strings.HasPrefix(key, "sk-mc-bot-") || strings.Count(out, "\n") != 1 {
		t.Fatalf("create printed %q, want the key alone", out)
	}
	hash := apikeys.Hash(key)
	secret := key[len("sk-mc-bot-"):]

	// Usable at once; cap of 2.
	for i, want := range []int{200, 200, 429} {
		if got := models(key); got != want {
			t.Errorf("request %d = %d, want %d", i, got, want)
		}
	}

	out, err = cli("list")
	if err != nil {
		t.Fatal(err)
	}
	seen.WriteString(out)
	mcLine := regexp.MustCompile(`(?m)^mc-bot\s+sk-mc-bot-[a-z2-7]{4}\.\.\.\s+2\s+2\s+\S+Z\s+-$`)
	if !strings.HasPrefix(out, "NAME") || !mcLine.MatchString(out) || !strings.Contains(out, "test-svc") {
		t.Errorf("list =\n%s", out)
	}

	// Error paths.
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"create", "mc-bot"}, "already exists"},
		{[]string{"create", key}, "invalid key name"}, // a key pasted as the name
		{[]string{"create", "Bad_Name"}, "invalid key name"},
		{[]string{"create", "x", "--daily-cap", "0"}, "at least 1"},
		{[]string{"create", "x", "--daily-cap=-3"}, "at least 1"},
		{[]string{"create", "x", "--daily-cap"}, "needs a number"},
		{[]string{"create", "x", "--nope"}, "unknown flag"},
		{[]string{"create"}, "exactly one"},
		{[]string{"create", "a", "b"}, "exactly one"},
		{[]string{"revoke", "nobody"}, "no key named nobody"},
		{[]string{"revoke", key}, "invalid key name"},
		{[]string{"revoke"}, "exactly one"},
		{[]string{"list", "x"}, "no arguments"},
		{[]string{"rotate"}, "unknown subcommand"},
		{nil, "missing subcommand"},
	} {
		if _, err := cli(tc.args...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("keys %v = %v, want an error containing %q", tc.args, err, tc.want)
		}
	}

	out, err = cli("revoke", "mc-bot")
	if err != nil || !strings.Contains(out, "Revoked key mc-bot") {
		t.Fatalf("revoke = %q, %v", out, err)
	}
	seen.WriteString(out)
	if got := models(key); got != http.StatusUnauthorized {
		t.Errorf("revoked key = %d, want 401", got)
	}
	out, err = cli("revoke", "mc-bot")
	if err != nil || !strings.Contains(out, "already revoked") {
		t.Errorf("second revoke = %q, %v", out, err)
	}
	seen.WriteString(out)

	// Re-create the revoked name: a new key, and the old one stays dead.
	out, err = cli("create", "mc-bot")
	if err != nil {
		t.Fatal(err)
	}
	if newKey := strings.TrimSpace(out); newKey == key || models(newKey) != http.StatusOK || models(key) != http.StatusUnauthorized {
		t.Errorf("re-created key doesn't behave: new=%d old=%d", models(newKey), models(key))
	}
	listOut, _ := cli("list")
	seen.WriteString(listOut)

	all := seen.String()
	for what, s := range map[string]string{
		"key":           secret,
		"hash (hex)":    hex.EncodeToString(hash[:]),
		"any full key":  keyPattern.FindString(all),
		"hash (prefix)": hex.EncodeToString(hash[:8]),
	} {
		if s != "" && strings.Contains(all, s) {
			t.Errorf("CLI output contains the %s:\n%s", what, all)
		}
	}
}

func TestKeysCLIRouterNotRunning(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	var out, errOut bytes.Buffer
	err = runKeys(context.Background(), admin.NewClient(addr), []string{"create", "mc-bot"}, &out, &errOut)
	if !errors.Is(err, admin.ErrNotRunning) {
		t.Errorf("err = %v, want ErrNotRunning", err)
	}
	// The key was generated but the router never stored it: it must not be
	// printed, or someone might use a key that doesn't work.
	if out.Len() != 0 {
		t.Errorf("printed %q although nothing was stored", out.String())
	}
}

// TestAdminAPIKeysNeverReturnSecrets calls the admin endpoints directly.
func TestAdminAPIKeysNeverReturnSecrets(t *testing.T) {
	cfg := testConfig(t)
	pkeys := testKeys(t)
	apiKeys, _ := testAPIKeys(t)
	h := newAdminHandler(cfg, pkeys, providerEnabled(cfg, pkeys.Has), apiKeys)
	key, prefix, hash, _ := apikeys.Generate("digest")
	hexHash := hex.EncodeToString(hash[:])

	send := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Host = "127.0.0.1:8081"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if strings.Contains(w.Body.String(), hexHash) || strings.Contains(w.Body.String(), key) {
			t.Errorf("%s %s returned a secret: %s", method, path, w.Body)
		}
		return w
	}
	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"name":"digest","prefix":"` + prefix + `","hash":"` + hexHash + `","daily_cap":10}`, 201},
		{`{"name":"digest","prefix":"` + prefix + `","hash":"` + hexHash + `"}`, 409},
		{`{"name":"other","prefix":"` + key + `","hash":"` + hexHash + `"}`, 400},
		{`{"name":"other","prefix":"sk-other-abcd","hash":"zz"}`, 400},
		{`{"name":"other","prefix":"sk-other-abcd","hash":"` + hexHash + `","daily_cap":-1}`, 400},
		{`{"name":"other","key":"` + key + `"}`, 400}, // unknown field: no plain keys accepted
	} {
		if w := send("POST", "/admin/keys", tc.body); w.Code != tc.want {
			t.Errorf("POST %s = %d %s, want %d", tc.body, w.Code, w.Body, tc.want)
		}
	}
	if w := send("GET", "/admin/keys", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"prefix":"`+prefix+`"`) {
		t.Errorf("GET = %d %s", w.Code, w.Body)
	}
	if w := send("DELETE", "/admin/keys/digest", ""); w.Code != 200 {
		t.Errorf("DELETE = %d %s", w.Code, w.Body)
	}
}
