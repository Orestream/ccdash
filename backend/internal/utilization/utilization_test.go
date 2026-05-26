package utilization

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// sampleBody mirrors a real /api/oauth/usage response: utilization is already a
// percentage, resets_at is RFC3339 (or null), and some windows are null.
const sampleBody = `{
  "five_hour": {"utilization": 3.0, "resets_at": "2026-05-26T16:50:00.881631+00:00"},
  "seven_day": {"utilization": 9.0, "resets_at": "2026-05-29T06:00:00.881656+00:00"},
  "seven_day_opus": null,
  "seven_day_sonnet": {"utilization": 0.0, "resets_at": null}
}`

func writeCred(t *testing.T, token string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".credentials.json")
	body := `{"claudeAiOauth":{"accessToken":"` + token + `"}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write cred: %v", err)
	}
	return path
}

func TestFetchMapsResponse(t *testing.T) {
	var gotAuth, gotBeta string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != usagePath {
			t.Errorf("path = %q, want %q", r.URL.Path, usagePath)
		}
		gotAuth = r.Header.Get("Authorization")
		gotBeta = r.Header.Get("anthropic-beta")
		_, _ = w.Write([]byte(sampleBody))
	}))
	defer srv.Close()

	f := NewFetcher(writeCred(t, "tok-123"))
	f.baseURL = srv.URL

	u, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if gotAuth != "Bearer tok-123" {
		t.Errorf("Authorization = %q, want Bearer tok-123", gotAuth)
	}
	if gotBeta != oauthBetaHeader {
		t.Errorf("anthropic-beta = %q, want %q", gotBeta, oauthBetaHeader)
	}

	if u.Session == nil || u.Session.UsedPercent != 3.0 {
		t.Fatalf("session = %+v, want UsedPercent 3.0", u.Session)
	}
	if u.Session.ResetsAt == nil || u.Session.ResetsAt.IsZero() {
		t.Errorf("session.ResetsAt not parsed: %+v", u.Session.ResetsAt)
	}
	if u.Week == nil || u.Week.UsedPercent != 9.0 {
		t.Fatalf("week = %+v, want UsedPercent 9.0", u.Week)
	}
	if u.WeekOpus != nil {
		t.Errorf("weekOpus = %+v, want nil (null window omitted)", u.WeekOpus)
	}
	if u.FetchedAt.IsZero() {
		t.Error("FetchedAt is zero")
	}
}

func TestFetchCachesWithinTTL(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(sampleBody))
	}))
	defer srv.Close()

	f := NewFetcher(writeCred(t, "tok"))
	f.baseURL = srv.URL

	for i := 0; i < 3; i++ {
		if _, err := f.Fetch(context.Background()); err != nil {
			t.Fatalf("Fetch %d: %v", i, err)
		}
	}
	if calls != 1 {
		t.Errorf("endpoint called %d times, want 1 (cached)", calls)
	}
}

func TestFetchUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	f := NewFetcher(writeCred(t, "tok"))
	f.baseURL = srv.URL

	if _, err := f.Fetch(context.Background()); err == nil {
		t.Fatal("expected error on 401, got nil")
	}
}

func TestFetchBacksOffAfterFailure(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	f := NewFetcher(writeCred(t, "tok"))
	f.baseURL = srv.URL

	// First call hits upstream and gets 429; with no cache it surfaces the error.
	_, err := f.Fetch(context.Background())
	if !errors.Is(err, errRateLimited) {
		t.Fatalf("first Fetch err = %v, want errRateLimited", err)
	}
	// Subsequent calls are inside the backoff window: no further upstream hits.
	for i := 0; i < 5; i++ {
		if _, err := f.Fetch(context.Background()); !errors.Is(err, errRateLimited) {
			t.Fatalf("Fetch %d err = %v, want errRateLimited", i, err)
		}
	}
	if calls != 1 {
		t.Errorf("endpoint called %d times, want 1 (backoff caps retries)", calls)
	}
}

func TestFetchServesStaleCacheOnError(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			_, _ = w.Write([]byte(sampleBody))
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	f := NewFetcher(writeCred(t, "tok"))
	f.baseURL = srv.URL
	f.ttl = 0 // every call is considered stale, so each attempts a refresh

	// Prime the cache with a good value.
	first, err := f.Fetch(context.Background())
	if err != nil || first.Session == nil || first.Session.UsedPercent != 3.0 {
		t.Fatalf("prime Fetch = %+v, err = %v", first, err)
	}

	// Next call attempts upstream, hits 429, and serves the stale cache (no error).
	got, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch after 429 returned error %v, want stale cache", err)
	}
	if got.Session == nil || got.Session.UsedPercent != 3.0 {
		t.Fatalf("stale value = %+v, want UsedPercent 3.0", got.Session)
	}
	if calls != 2 {
		t.Fatalf("endpoint called %d times, want 2", calls)
	}

	// A further call is inside the backoff window and must not hit upstream again.
	if _, err := f.Fetch(context.Background()); err != nil {
		t.Fatalf("backoff Fetch returned error %v", err)
	}
	if calls != 2 {
		t.Errorf("endpoint called %d times, want 2 (backoff holds)", calls)
	}
}

func TestFetchSingleFlightsConcurrentCalls(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(20 * time.Millisecond) // widen the race window
		_, _ = w.Write([]byte(sampleBody))
	}))
	defer srv.Close()

	f := NewFetcher(writeCred(t, "tok"))
	f.baseURL = srv.URL

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.Fetch(context.Background()); err != nil {
				t.Errorf("concurrent Fetch: %v", err)
			}
		}()
	}
	wg.Wait()

	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("endpoint called %d times, want 1 (single-flight)", n)
	}
}

func TestReadTokenErrors(t *testing.T) {
	if _, err := readToken(""); err == nil {
		t.Error("empty path: expected error")
	}
	if _, err := readToken(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("missing file: expected error")
	}

	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, []byte(`{"claudeAiOauth":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readToken(empty); err == nil {
		t.Error("empty token: expected error")
	}
}
