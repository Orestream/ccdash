package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestSpaHandlerServesExistingAsset(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":    {Data: []byte("<!doctype html><body>SPA</body>")},
		"assets/app.js": {Data: []byte("console.log('app');")},
	}
	h := spaHandler(fsys)

	req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	if string(body) != "console.log('app');" {
		t.Fatalf("body = %q, want the asset body", string(body))
	}
}

func TestSpaHandlerFallsBackToIndexForUnknownRoute(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><body>SPA</body>")},
	}
	h := spaHandler(fsys)

	req := httptest.NewRequest(http.MethodGet, "/projects/123", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	if string(body) != "<!doctype html><body>SPA</body>" {
		t.Fatalf("body = %q, want the SPA shell", string(body))
	}
}

func TestSpaHandlerRootServesIndex(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><body>SPA</body>")},
	}
	h := spaHandler(fsys)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	if string(body) != "<!doctype html><body>SPA</body>" {
		t.Fatalf("body = %q, want the SPA shell", string(body))
	}
}
