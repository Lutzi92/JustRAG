package proxy

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
)

type Proxy struct {
	rp *httputil.ReverseProxy
}

func New(target string) (*Proxy, error) {
	targetURL, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy target URL %q: %w", target, err)
	}

	// Rewrite rather than Director/NewSingleHostReverseProxy: Director is
	// deprecated as of Go 1.26. SetURL reproduces NewSingleHostReverseProxy's
	// path-join semantics AND clears Out.Host so the outbound Host header
	// derives from the target — exactly what the old explicit
	// `req.Host = targetURL.Host` line did.
	//
	// One deliberate behaviour change: Director silently appended the client
	// IP to any client-supplied X-Forwarded-For. SetXForwarded starts from the
	// stripped inbound headers instead, so a client cannot spoof the chain.
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(targetURL)
			pr.SetXForwarded()
		},
	}

	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		slog.Error("proxy error", "path", r.URL.Path, "error", err)
		http.Error(w, `{"error":"Backend service unavailable"}`, http.StatusBadGateway)
	}

	return &Proxy{rp: rp}, nil
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.rp.ServeHTTP(w, r)
}
