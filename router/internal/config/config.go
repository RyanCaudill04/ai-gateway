// Package config loads and validates providers.yaml: which upstream providers
// and models exist, and which virtual models (routes) clients can ask for.
//
// Provider API keys are not in this file. They are registered through the app
// and stored encrypted (#40); Load only asks whether a key exists.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the validated configuration.
type Config struct {
	Providers map[string]*Provider
	// Routes maps a virtual model name to its ordered list of "provider/model" IDs.
	Routes map[string][]string
}

// Provider is one upstream service.
type Provider struct {
	Name    string
	BaseURL string // ${VAR} references already expanded
	Local   bool   // only local providers may serve the `private` route
	Enabled bool   // has a registered key, or is local
	Models  []Model
}

// Model is one model offered by a provider.
type Model struct {
	ID            string
	ContextLength int
}

// Lookup returns a model by its full "provider/model" ID.
func (c *Config) Lookup(id string) (*Provider, Model, bool) {
	name, modelID, ok := strings.Cut(id, "/")
	if !ok {
		return nil, Model{}, false
	}
	p := c.Providers[name]
	if p == nil {
		return nil, Model{}, false
	}
	for _, m := range p.Models {
		if m.ID == modelID {
			return p, m, true
		}
	}
	return nil, Model{}, false
}

// DisabledProviders lists providers with no key and not local, sorted.
func (c *Config) DisabledProviders() []string {
	var out []string
	for name, p := range c.Providers {
		if !p.Enabled {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// raw mirrors the YAML file. Unknown fields are rejected so a typo (or the old
// api_key_env field) is an error instead of being silently ignored.
type raw struct {
	Providers map[string]struct {
		BaseURL string `yaml:"base_url"`
		Local   bool   `yaml:"local"`
		Models  []struct {
			ID            string `yaml:"id"`
			ContextLength int    `yaml:"context_length"`
		} `yaml:"models"`
	} `yaml:"providers"`
	Routes map[string][]string `yaml:"routes"`
}

// HasKey reports whether a provider key is registered in the app.
type HasKey func(provider string) bool

// LoadFile reads and validates the file at path.
func LoadFile(path string, getenv func(string) string, hasKey HasKey) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read providers config: %w", err)
	}
	cfg, err := Parse(data, getenv, hasKey)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Parse validates YAML bytes. Every problem found is reported, not just the
// first, and each names the offending field.
func Parse(data []byte, getenv func(string) string, hasKey HasKey) (*Config, error) {
	var r raw
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&r); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}

	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	cfg := &Config{Providers: map[string]*Provider{}, Routes: map[string][]string{}}

	if len(r.Providers) == 0 {
		bad("providers: at least one provider is required")
	}
	for _, name := range sortedKeys(r.Providers) {
		rp := r.Providers[name]
		field := "providers." + name

		if name == "" || strings.ContainsAny(name, "/ ") {
			bad("%s: provider name must be non-empty with no '/' or spaces", field)
		}
		p := &Provider{Name: name, Local: rp.Local}

		if rp.BaseURL == "" {
			bad("%s.base_url: required", field)
		} else if expanded, err := expandEnv(rp.BaseURL, getenv); err != nil {
			bad("%s.base_url: %v", field, err)
		} else if u, err := url.Parse(expanded); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			bad("%s.base_url: %q is not an http(s) URL", field, expanded)
		} else {
			p.BaseURL = strings.TrimRight(expanded, "/")
		}

		if len(rp.Models) == 0 {
			bad("%s.models: at least one model is required", field)
		}
		seen := map[string]bool{}
		for i, m := range rp.Models {
			mfield := fmt.Sprintf("%s.models[%d]", field, i)
			switch {
			case m.ID == "":
				bad("%s.id: required", mfield)
			case seen[m.ID]:
				bad("%s.id: duplicate model %q", mfield, m.ID)
			}
			seen[m.ID] = true
			if m.ContextLength <= 0 {
				bad("%s.context_length: must be a positive number (model %q)", mfield, m.ID)
			}
			p.Models = append(p.Models, Model{ID: m.ID, ContextLength: m.ContextLength})
		}

		p.Enabled = p.Local || (hasKey != nil && hasKey(name))
		cfg.Providers[name] = p
	}

	for _, name := range sortedKeys(r.Routes) {
		entries := r.Routes[name]
		field := "routes." + name

		if name == "" || strings.ContainsAny(name, " ") {
			bad("%s: route name must be non-empty with no spaces", field)
		}
		// A client's `model` is either a route or a provider/model ID, so the
		// two namespaces must not overlap.
		if _, _, ok := cfg.Lookup(name); ok {
			bad("%s: route name collides with the model ID %q", field, name)
		}
		if len(entries) == 0 {
			bad("%s: route has no entries", field)
		}
		seen := map[string]bool{}
		for i, id := range entries {
			efield := fmt.Sprintf("%s[%d]", field, i)
			if _, _, ok := cfg.Lookup(id); !ok {
				bad("%s: %q is not a known provider/model", efield, id)
			}
			if seen[id] {
				bad("%s: %q is listed twice", efield, id)
			}
			seen[id] = true
		}
		cfg.Routes[name] = entries
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return cfg, nil
}

// expandEnv replaces ${VAR} with its value. A reference to an unset or empty
// variable is an error, because it would otherwise turn into a broken URL.
func expandEnv(s string, getenv func(string) string) (string, error) {
	var out strings.Builder
	for {
		start := strings.Index(s, "${")
		if start < 0 {
			out.WriteString(s)
			return out.String(), nil
		}
		end := strings.Index(s[start:], "}")
		if end < 0 {
			return "", fmt.Errorf("unterminated ${ in %q", s)
		}
		name := s[start+2 : start+end]
		if name == "" {
			return "", fmt.Errorf("empty ${} in %q", s)
		}
		val := getenv(name)
		if val == "" {
			return "", fmt.Errorf("environment variable %s is not set", name)
		}
		out.WriteString(s[:start])
		out.WriteString(val)
		s = s[start+end+1:]
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
