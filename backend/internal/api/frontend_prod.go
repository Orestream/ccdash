//go:build prod

package api

import (
	"io/fs"
	"net/http"

	"github.com/robinmalmstrom/ccdash/backend/internal/web"
)

// frontendHandler serves the production frontend embedded into the binary by
// the web package, with SPA fallback to index.html.
func (s *Server) frontendHandler() http.Handler {
	sub, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		panic(err) // embed is fixed at build time; a failure here is a build bug
	}
	return spaHandler(sub)
}
