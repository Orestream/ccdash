// Package utilization fetches the Claude subscription rate-limit usage that the
// `claude` CLI surfaces via its /usage command. It reads the OAuth access token
// from the local credentials file and queries the same undocumented endpoint the
// CLI calls (GET /api/oauth/usage), then maps the response to models.Utilization.
//
// The access token is the user's own credential for their own account usage; it
// is never logged. The endpoint is undocumented and may change without notice,
// so callers should treat fetch failures as "usage unavailable" rather than fatal.
package utilization

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/robinmalmstrom/ccdash/backend/internal/models"
)

const (
	defaultBaseURL   = "https://api.anthropic.com"
	usagePath        = "/api/oauth/usage"
	oauthBetaHeader  = "oauth-2025-04-20"
	anthropicVersion = "2023-06-01"
	defaultTTL       = 30 * time.Second
	// After a failed upstream fetch we refuse to retry for this long, so a burst
	// of dashboard polls (e.g. session.usage events) can't turn one failure into a
	// request storm. A 429 backs off harder because the limit is account-wide and
	// shared with the `claude` CLI — hammering it only keeps it tripped.
	errBackoff       = 30 * time.Second
	rateLimitBackoff = 2 * time.Minute
)

// errRateLimited marks an upstream 429 so callers can back off harder. It is the
// error surfaced when the endpoint rate-limits us and we have no cached value.
var errRateLimited = errors.New("usage endpoint: rate limited (HTTP 429)")

// Fetcher retrieves subscription utilization, caching the result for a short TTL
// so frequent dashboard polls don't hammer the endpoint. Crucially it also
// rate-limits *attempts*: after a failure it serves the last good value (or the
// error) until a backoff window elapses, instead of re-hitting the upstream on
// every poll. It is safe for concurrent use.
type Fetcher struct {
	baseURL  string
	credPath string
	client   *http.Client
	ttl      time.Duration

	fetchMu sync.Mutex // serializes upstream calls so a burst collapses to one

	mu          sync.Mutex
	cached      models.Utilization
	cachedAt    time.Time
	hasCache    bool
	nextAttempt time.Time // earliest time we may hit the upstream again
	lastErr     error     // last upstream error (served when there is no cache)
}

// NewFetcher builds a Fetcher that reads the OAuth token from credPath (typically
// ~/.claude/.credentials.json).
func NewFetcher(credPath string) *Fetcher {
	return &Fetcher{
		baseURL:  defaultBaseURL,
		credPath: credPath,
		client:   &http.Client{Timeout: 10 * time.Second},
		ttl:      defaultTTL,
	}
}

// Fetch returns the current utilization. A fresh successful value (within the
// TTL) is served from cache; otherwise it refreshes from upstream, but only if
// it isn't inside a post-failure backoff window. While backing off it serves the
// last good value if there is one, else the last error.
func (f *Fetcher) Fetch(ctx context.Context) (models.Utilization, error) {
	if u, done, err := f.fromCache(); done {
		return u, err
	}

	// Collapse concurrent callers into a single upstream request.
	f.fetchMu.Lock()
	defer f.fetchMu.Unlock()

	// Another goroutine may have refreshed (or just failed) while we waited.
	if u, done, err := f.fromCache(); done {
		return u, err
	}

	tok, err := readToken(f.credPath)
	if err == nil {
		var raw rawUsage
		raw, err = f.get(ctx, tok)
		if err == nil {
			u := raw.toModel(time.Now())
			f.mu.Lock()
			f.cached, f.cachedAt, f.hasCache, f.lastErr, f.nextAttempt =
				u, time.Now(), true, nil, time.Time{}
			f.mu.Unlock()
			return u, nil
		}
	}

	// Failure: arm the backoff and serve the last good value rather than a blip.
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastErr = err
	f.nextAttempt = time.Now().Add(backoffFor(err))
	if f.hasCache {
		return f.cached, nil
	}
	return models.Utilization{}, err
}

// fromCache returns (value, done, err). done is true when the call can be
// answered without hitting upstream: either a fresh success or an active backoff
// window. When backing off without any cached value, it returns the last error.
func (f *Fetcher) fromCache() (models.Utilization, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	if f.hasCache && now.Sub(f.cachedAt) < f.ttl {
		return f.cached, true, nil
	}
	if now.Before(f.nextAttempt) {
		if f.hasCache {
			return f.cached, true, nil
		}
		return models.Utilization{}, true, f.lastErr
	}
	return models.Utilization{}, false, nil
}

func backoffFor(err error) time.Duration {
	if errors.Is(err, errRateLimited) {
		return rateLimitBackoff
	}
	return errBackoff
}

// credFile is the slice of ~/.claude/.credentials.json we need.
type credFile struct {
	ClaudeAiOauth struct {
		AccessToken string `json:"accessToken"`
	} `json:"claudeAiOauth"`
}

func readToken(path string) (string, error) {
	if path == "" {
		return "", errors.New("no credentials path configured")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read credentials: %w", err)
	}
	var c credFile
	if err := json.Unmarshal(b, &c); err != nil {
		return "", fmt.Errorf("parse credentials: %w", err)
	}
	if c.ClaudeAiOauth.AccessToken == "" {
		return "", errors.New("no oauth access token in credentials (not logged in?)")
	}
	return c.ClaudeAiOauth.AccessToken, nil
}

// rawWindow / rawUsage mirror the /api/oauth/usage response. utilization is a
// percentage (e.g. 9.0 == 9%); resets_at is an RFC3339 string and may be null.
type rawWindow struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    *string  `json:"resets_at"`
}

type rawUsage struct {
	FiveHour     *rawWindow `json:"five_hour"`
	SevenDay     *rawWindow `json:"seven_day"`
	SevenDayOpus *rawWindow `json:"seven_day_opus"`
}

func (f *Fetcher) get(ctx context.Context, token string) (rawUsage, error) {
	var raw rawUsage
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.baseURL+usagePath, nil)
	if err != nil {
		return raw, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-beta", oauthBetaHeader)
	req.Header.Set("anthropic-version", anthropicVersion)

	resp, err := f.client.Do(req)
	if err != nil {
		return raw, fmt.Errorf("usage request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return raw, errors.New("usage endpoint: unauthorized (token expired? run claude to refresh)")
		case http.StatusTooManyRequests:
			return raw, errRateLimited
		default:
			return raw, fmt.Errorf("usage endpoint: %s", resp.Status)
		}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&raw); err != nil {
		return raw, fmt.Errorf("decode usage: %w", err)
	}
	return raw, nil
}

func (r rawUsage) toModel(now time.Time) models.Utilization {
	return models.Utilization{
		Session:   r.FiveHour.toWindow(),
		Week:      r.SevenDay.toWindow(),
		WeekOpus:  r.SevenDayOpus.toWindow(),
		FetchedAt: now,
	}
}

func (w *rawWindow) toWindow() *models.UsageWindow {
	if w == nil || w.Utilization == nil {
		return nil
	}
	out := &models.UsageWindow{UsedPercent: *w.Utilization}
	if w.ResetsAt != nil && *w.ResetsAt != "" {
		if t, err := time.Parse(time.RFC3339, *w.ResetsAt); err == nil {
			out.ResetsAt = &t
		}
	}
	return out
}
