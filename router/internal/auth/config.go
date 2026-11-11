package auth

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Mode selects how requests are authenticated.
type Mode string

const (
	// ModeCloudflareAccess verifies the JWT that Cloudflare Access attaches. The default.
	ModeCloudflareAccess Mode = "cloudflare-access"
	// ModeNone disables authentication. Local development only.
	ModeNone Mode = "none"
)

// Config holds the auth settings read from the environment.
type Config struct {
	Mode Mode
	// TeamDomain is the Zero Trust team host, e.g. "myteam.cloudflareaccess.com".
	TeamDomain string
	// Audience is the Access application's AUD tag. It ties a token to *this*
	// application, so a token issued for another app on the same team is refused.
	Audience string
	// AllowedEmails may use the gateway. Lower-cased.
	AllowedEmails []string
}

// ConfigFromEnv reads AUTH_MODE, CF_ACCESS_TEAM_DOMAIN, CF_ACCESS_AUD and
// AUTH_ALLOWED_EMAILS. An unset AUTH_MODE means cloudflare-access, so
// forgetting to configure auth can never mean "no auth".
func ConfigFromEnv(getenv func(string) string) (Config, error) {
	mode := Mode(strings.ToLower(strings.TrimSpace(getenv("AUTH_MODE"))))
	if mode == "" {
		mode = ModeCloudflareAccess
	}

	switch mode {
	case ModeNone:
		return Config{Mode: ModeNone}, nil
	case ModeCloudflareAccess:
	default:
		return Config{}, fmt.Errorf("unknown AUTH_MODE %q (want %q or %q)", mode, ModeCloudflareAccess, ModeNone)
	}

	cfg := Config{Mode: mode, Audience: strings.TrimSpace(getenv("CF_ACCESS_AUD"))}
	var problems []error

	domain, err := normalizeTeamDomain(getenv("CF_ACCESS_TEAM_DOMAIN"))
	if err != nil {
		problems = append(problems, err)
	}
	cfg.TeamDomain = domain

	if cfg.Audience == "" {
		problems = append(problems, errors.New("CF_ACCESS_AUD is empty"))
	}

	for _, e := range strings.Split(getenv("AUTH_ALLOWED_EMAILS"), ",") {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			cfg.AllowedEmails = append(cfg.AllowedEmails, e)
		}
	}
	if len(cfg.AllowedEmails) == 0 {
		problems = append(problems, errors.New("AUTH_ALLOWED_EMAILS is empty"))
	}

	if len(problems) > 0 {
		msgs := make([]string, len(problems))
		for i, p := range problems {
			msgs[i] = p.Error()
		}
		return Config{}, errors.New(strings.Join(msgs, "; "))
	}
	return cfg, nil
}

// normalizeTeamDomain accepts "myteam.cloudflareaccess.com" with or without
// "https://" and returns the bare host.
func normalizeTeamDomain(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("CF_ACCESS_TEAM_DOMAIN is empty")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.User != nil {
		return "", fmt.Errorf("CF_ACCESS_TEAM_DOMAIN must be a host such as myteam.cloudflareaccess.com")
	}
	return strings.ToLower(u.Host), nil
}

// normalizeEmail makes comparison case- and whitespace-insensitive.
func normalizeEmail(e string) string {
	return strings.ToLower(strings.TrimSpace(e))
}
