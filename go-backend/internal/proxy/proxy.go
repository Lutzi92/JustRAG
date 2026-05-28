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

	rp := httputil.NewSingleHostReverseProxy(targetURL)

	originalDirector := rp.Director
	rp.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = targetURL.Host
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
