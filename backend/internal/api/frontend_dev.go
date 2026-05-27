//go:build !prod

package api

import (
	"net/http"
	"net/http/httputil"
	"net/url"
)

// frontendHandler reverse-proxies every non-/api, non-/ws request to the Vite
// dev server on an internal port. The browser only ever talks to Go's port,
// but HMR (WebSocket upgrades included) tunnels through this proxy.
func (s *Server) frontendHandler() http.Handler {
	target, _ := url.Parse("http://localhost:10001")
	return httputil.NewSingleHostReverseProxy(target)
}
