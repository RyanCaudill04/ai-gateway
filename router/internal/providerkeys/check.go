package providerkeys

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// CheckTimeout bounds one Check call.
const CheckTimeout = 15 * time.Second

// Check makes one cheap authenticated call, GET <baseURL>/models, to see
// whether key works before it's saved. Listing models costs no tokens on any
// OpenAI-compatible provider. It returns nil on any 2xx answer.
//
// The error says what the provider answered (status only, never the body,
// which some providers fill with the rejected key) and is redacted anyway in
// case a misconfigured base_url or proxy echoes it.
func Check(ctx context.Context, client *http.Client, baseURL, key string) error {
	if client == nil {
		client = http.DefaultClient
	}
	ctx, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/models", nil)
	if err != nil {
		return fmt.Errorf("build test request: %s", Redact(err.Error(), key))
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("test request to %s failed: %s", baseURL, Redact(err.Error(), key))
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20)) // lets the connection be reused

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		hint := ""
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			hint = " (the key was rejected)"
		case http.StatusBadRequest:
			hint = " (Gemini answers 400 for an invalid key)"
		case http.StatusNotFound:
			hint = " (check base_url in providers.yaml)"
		}
		return fmt.Errorf("test request to %s/models: provider answered %s%s", baseURL, Redact(resp.Status, key), hint)
	}
	return nil
}
