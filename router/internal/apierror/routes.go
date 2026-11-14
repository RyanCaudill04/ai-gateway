package apierror

import "net/http"

// Routes makes mux answer unknown paths and wrong methods in the error format.
// ServeMux's own 404 and 405 are plain text; this keeps them (and the Allow
// header on a 405) but swaps the body. Everything else, including the mux's
// redirects, is served untouched.
func Routes(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, pattern := mux.Handler(r)
		if pattern != "" {
			h.ServeHTTP(w, r)
			return
		}
		// No route matched: run the mux's fallback against a throwaway writer
		// to learn which of 404, 405 or a redirect it chose, and its headers.
		p := &probe{header: http.Header{}}
		h.ServeHTTP(p, r)
		switch p.status {
		case http.StatusNotFound:
			Write(w, NotFound())
		case http.StatusMethodNotAllowed:
			if allow := p.header.Get("Allow"); allow != "" {
				w.Header().Set("Allow", allow)
			}
			Write(w, MethodNotAllowed())
		default:
			h.ServeHTTP(w, r)
		}
	})
}

// probe records the status and headers a handler writes and drops the body.
type probe struct {
	header http.Header
	status int
}

func (p *probe) Header() http.Header         { return p.header }
func (p *probe) Write(b []byte) (int, error) { return len(b), nil }
func (p *probe) WriteHeader(status int) {
	if p.status == 0 {
		p.status = status
	}
}
