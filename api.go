package yaft

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// APIFeatureProvider is a feature-shape provider that loads one toggle group
// from a YaFT backend.
//
// Refresh asks GET /collectionHash/{uuid} whether the group changed and only
// then fetches GET /features/{uuid}. The response goes through
// NormaliseCollection, so every envelope and field spelling the backend has
// sent is understood, and evaluation is local, against the clock (R26): a
// scheduled toggle flips at its exact instant, not when the backend's cron job
// gets to it.
//
// Nothing is fetched until the first Refresh; until then every feature is
// off. A failed refresh returns an error and keeps the previous data, so a
// backend outage does not switch everything off. Safe for concurrent use.
type APIFeatureProvider struct {
	features       string
	collectionHash string
	keyPrefix      string
	client         *http.Client
	maxBodyBytes   int64
	clock          Clock

	mu   sync.Mutex // serialises Refresh
	hash string
	data atomic.Pointer[map[string]Feature]
}

// APIOption changes a default of NewAPIFeatureProvider.
type APIOption func(*apiConfig)

type apiConfig struct {
	client       *http.Client
	timeout      time.Duration
	maxBodyBytes int64
	clock        Clock
}

// WithTimeout bounds each request, from connecting until the last byte of the
// body. Default 5 seconds. Ignored when WithHTTPClient is used.
func WithTimeout(timeout time.Duration) APIOption {
	return func(c *apiConfig) { c.timeout = timeout }
}

// WithMaxBodyBytes is the largest response body accepted. Default 1 MiB, far
// above any real group; the limit keeps a misbehaving endpoint from exhausting
// memory.
func WithMaxBodyBytes(n int64) APIOption {
	return func(c *apiConfig) { c.maxBodyBytes = n }
}

// WithClock sets the source of "now" for evaluation. Default time.Now.
func WithClock(clock Clock) APIOption {
	return func(c *apiConfig) { c.clock = clock }
}

// WithHTTPClient uses client, for example one with a proxy or custom TLS. Its
// own timeout and redirect policy then apply; the default client has a
// 5-second timeout and follows no redirects.
func WithHTTPClient(client *http.Client) APIOption {
	return func(c *apiConfig) { c.client = client }
}

// The UUID goes into the request path, so only the canonical 8-4-4-4-12 hex
// form is accepted; that also rules out "/", ".." and percent-encoding.
var canonicalUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// NewAPIFeatureProvider prepares a provider for the toggle group groupUUID on
// the backend at baseURL, which must be http or https without query or
// fragment. Nothing is fetched yet.
func NewAPIFeatureProvider(baseURL, groupUUID string, opts ...APIOption) (*APIFeatureProvider, error) {
	base, err := url.Parse(baseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, fmt.Errorf("yaft: base URL must be http or https: %q", baseURL)
	}
	if base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("yaft: base URL must not carry a query or fragment: %q", baseURL)
	}
	if !canonicalUUID.MatchString(groupUUID) {
		return nil, fmt.Errorf("yaft: group is not a UUID: %q", groupUUID)
	}
	group := strings.ToLower(groupUUID)

	config := apiConfig{timeout: 5 * time.Second, maxBodyBytes: 1 << 20}
	for _, opt := range opts {
		opt(&config)
	}
	if config.timeout <= 0 || config.maxBodyBytes <= 0 {
		return nil, errors.New("yaft: timeout and maxBodyBytes must be positive")
	}
	client := config.client
	if client == nil {
		client = &http.Client{
			// Covers the whole exchange, body included: a server that sends
			// headers and then stalls cannot hold a refresh forever.
			Timeout: config.timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}

	root := strings.TrimRight(base.String(), "/")
	p := &APIFeatureProvider{
		features:       root + "/features/" + group,
		collectionHash: root + "/collectionHash/" + group,
		keyPrefix:      group + "|",
		client:         client,
		maxBodyBytes:   config.maxBodyBytes,
		clock:          config.clock,
	}
	empty := map[string]Feature{}
	p.data.Store(&empty)
	return p, nil
}

// Refresh fetches the group if it changed since the last successful refresh
// and reports whether new data was loaded. On error the previous data stays.
func (p *APIFeatureProvider) Refresh(ctx context.Context) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	response, err := p.get(ctx, p.collectionHash)
	if err != nil {
		return false, err
	}
	hash, err := hashOf(response)
	if err != nil {
		return false, fmt.Errorf("yaft: GET %s: %w", p.collectionHash, err)
	}
	if hash == p.hash {
		return false, nil
	}

	response, err = p.get(ctx, p.features)
	if err != nil {
		return false, err
	}
	data := NormaliseCollection(response)
	p.data.Store(&data)
	// Recorded only after the group loaded, so a failed fetch is retried.
	p.hash = hash
	return true, nil
}

// RefreshQuietly is Refresh for a ticker: it logs a failure instead of
// returning it. The previous data stays in place.
func (p *APIFeatureProvider) RefreshQuietly(ctx context.Context) bool {
	changed, err := p.Refresh(ctx)
	if err != nil {
		slog.Warn("yaft: refresh failed; keeping the previous data", "url", p.features, "error", err)
	}
	return changed
}

// Data returns a copy of the data of the last successful refresh.
func (p *APIFeatureProvider) Data() map[string]Feature {
	return maps.Clone(*p.data.Load())
}

// IsEnabled answers for a toggle of this group, by its name or by its full
// "uuid|name" key. The key is looked up as given first, then as a name within
// the group, so a toggle can be named without knowing the group's UUID.
func (p *APIFeatureProvider) IsEnabled(key string) bool {
	data := *p.data.Load()
	feature, ok := data[key]
	if !ok && !strings.HasPrefix(key, p.keyPrefix) {
		feature, ok = data[p.keyPrefix+key]
	}
	if !ok {
		return false
	}
	return Evaluate(&feature, p.clock.now())
}

func (p *APIFeatureProvider) get(ctx context.Context, target string) (any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("yaft: GET %s: %w", target, err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("yaft: GET %s: %w", target, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("yaft: GET %s answered %d", target, resp.StatusCode)
	}
	// One byte past the limit tells "at the limit" from "over it" without
	// trusting Content-Length.
	body, err := io.ReadAll(io.LimitReader(resp.Body, p.maxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("yaft: GET %s: %w", target, err)
	}
	if int64(len(body)) > p.maxBodyBytes {
		return nil, fmt.Errorf("yaft: GET %s sent more than %d bytes", target, p.maxBodyBytes)
	}

	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("yaft: GET %s sent a body that does not parse: %w", target, err)
	}
	return parsed, nil
}

// hashOf reads the hash by presence, as the other ports do: collectionHash,
// else value.
func hashOf(response any) (string, error) {
	body, ok := response.(map[string]any)
	if ok {
		value, present := body["collectionHash"]
		if !present {
			value = body["value"]
		}
		if s, ok := value.(string); ok && s != "" {
			return s, nil
		}
	}
	return "", errors.New("no collectionHash")
}
