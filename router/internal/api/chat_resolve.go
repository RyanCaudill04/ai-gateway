package api

import (
	"github.com/RyanCaudill04/ai-gateway/router/internal/apierror"
	"github.com/RyanCaudill04/ai-gateway/router/internal/config"
)

// target is one provider model a request can be sent to.
type target struct {
	provider *config.Provider
	model    config.Model
}

// resolution is what a client's `model` means.
type resolution struct {
	// targets are the enabled entries, in the order to try them. For a pinned
	// model there is exactly one.
	targets []target
	// pinned is true for a provider/model ID. Pinned requests never fall back
	// (#11); routes do.
	pinned bool
}

// resolve turns the client's `model` into targets. A route name gives its
// entries in order, minus those whose provider is not enabled right now; a
// provider/model ID gives that one model. Model IDs may themselves contain
// slashes ("groq/openai/gpt-oss-20b" is provider groq, model
// "openai/gpt-oss-20b"); config.Lookup splits at the first one, and provider
// names can't contain a slash.
//
// Errors:
//   - unknown model: 404 model_not_found, as OpenAI answers.
//   - pinned model whose provider is disabled: 503 provider_not_enabled, with
//     the one target still returned so the caller can name it in headers.
//   - route with no enabled entry: 503 no_enabled_provider.
//
// The last two are 503 rather than 4xx because the request is fine: it will
// work as soon as a key is registered for the provider, and a 4xx would tell
// the client to change it. They are not 404 either: the model exists, and
// "does not exist" would send someone hunting for a typo.
func resolve(cfg *config.Config, enabled EnabledFunc, model string) (resolution, *apierror.Error) {
	if entries, ok := cfg.Routes[model]; ok {
		var res resolution
		for _, id := range entries {
			p, m, ok := cfg.Lookup(id)
			if !ok || !enabled(p.Name) {
				continue
			}
			res.targets = append(res.targets, target{provider: p, model: m})
		}
		if len(res.targets) == 0 {
			return res, apierror.Unavailable("no_enabled_provider",
				"None of this route's providers is enabled. Register an API key for one of them.")
		}
		return res, nil
	}

	p, m, ok := cfg.Lookup(model)
	if !ok {
		return resolution{}, apierror.ModelNotFound()
	}
	res := resolution{targets: []target{{provider: p, model: m}}, pinned: true}
	if !enabled(p.Name) {
		e := apierror.Unavailable("provider_not_enabled",
			"Provider "+p.Name+" is not enabled. Register its API key to use this model.")
		e.Provider, e.Model = p.Name, m.ID
		return res, e
	}
	return res, nil
}
