package yaft_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	yaft "github.com/tehw0lf/yaft-go"
)

const group = "896ea308-382f-46b0-bc59-d93a28013633"

// backend serves canned answers per path and counts requests.
type backend struct {
	mu     sync.Mutex
	routes map[string]func(http.ResponseWriter)
	hits   map[string]int
	server *httptest.Server
}

func newBackend(t *testing.T) *backend {
	b := &backend{routes: map[string]func(http.ResponseWriter){}, hits: map[string]int{}}
	b.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.hits[r.URL.Path]++
		route, ok := b.routes[r.URL.Path]
		b.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":"Feature not found"}`)
			return
		}
		route(w)
	}))
	t.Cleanup(b.server.Close)
	return b
}

func (b *backend) json(path string, status int, body string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.routes[path] = func(w http.ResponseWriter) {
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}
}

func (b *backend) serve(hash, features string) {
	b.json("/collectionHash/"+group, 200, `{"collectionHash":"`+hash+`"}`)
	b.json("/features/"+group, 200, features)
}

func (b *backend) count(path string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.hits[path]
}

func mustProvider(t *testing.T, base string, opts ...yaft.APIOption) *yaft.APIFeatureProvider {
	t.Helper()
	p, err := yaft.NewAPIFeatureProvider(base, group, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAPILoadsAGroupInEitherSpelling(t *testing.T) {
	b := newBackend(t)
	b.serve("h1", `{"toggles": [
		{"key": "`+group+`|new", "value": "true", "activeAt": null, "disabledAt": null, "tags": null},
		{"Key": "`+group+`|old", "Value": "false", "ActiveAt": null, "DisabledAt": null}]}`)
	p := mustProvider(t, b.server.URL)

	if p.IsEnabled("new") {
		t.Fatal("nothing is fetched before the first refresh")
	}
	if changed, err := p.Refresh(context.Background()); err != nil || !changed {
		t.Fatalf("Refresh = %v, %v", changed, err)
	}
	// By bare name within the group, as a constant key would name it, and by full key.
	if !p.IsEnabled("new") || !p.IsEnabled(group+"|new") {
		t.Error("new should be on")
	}
	if p.IsEnabled("old") || p.IsEnabled("missing") {
		t.Error("old and missing should be off")
	}
	if p.IsEnabled("00000000-0000-0000-0000-000000000000|new") {
		t.Error("another group's key must not match")
	}
}

func TestAPIRefetchesOnlyWhenTheHashChanges(t *testing.T) {
	b := newBackend(t)
	b.serve("h1", `{"toggles": []}`)
	p := mustProvider(t, b.server.URL)
	ctx := context.Background()

	p.Refresh(ctx)
	if changed, _ := p.Refresh(ctx); changed {
		t.Error("unchanged hash reported a change")
	}
	if n := b.count("/features/" + group); n != 1 {
		t.Errorf("features fetched %d times, want 1", n)
	}
	b.serve("h2", `{"toggles": [{"key": "k", "value": "true"}]}`)
	if changed, _ := p.Refresh(ctx); !changed || !p.IsEnabled("k") {
		t.Error("new hash did not reload")
	}
}

func TestAPIEvaluatesBoundsLocally(t *testing.T) {
	b := newBackend(t)
	b.serve("h1", `{"toggles": [{"key": "k", "value": "true", "activeAt": "2026-09-18T12:00:00Z"}]}`)
	now := time.Date(2026, 9, 18, 11, 59, 59, 0, time.UTC)
	p := mustProvider(t, b.server.URL, yaft.WithClock(func() time.Time { return now }))
	p.Refresh(context.Background())

	if p.IsEnabled("k") {
		t.Error("on before activeAt")
	}
	now = now.Add(time.Second) // no refresh: the flip does not wait for the backend (R26)
	if !p.IsEnabled("k") {
		t.Error("off at activeAt")
	}
}

func TestAPIKeepsDataWhenARefreshFails(t *testing.T) {
	b := newBackend(t)
	b.serve("h1", `{"toggles": [{"key": "k", "value": "true"}]}`)
	p := mustProvider(t, b.server.URL)
	p.Refresh(context.Background())

	b.json("/collectionHash/"+group, 500, `{}`)
	if _, err := p.Refresh(context.Background()); err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("err = %v, want a 500", err)
	}
	if !p.IsEnabled("k") || p.RefreshQuietly(context.Background()) || !p.IsEnabled("k") {
		t.Error("previous data lost")
	}
}

// Found in review: a 200 with a body that is not a group -- a proxy's error
// page, null -- used to replace the data with nothing, silently and for good.
func TestAPIKeepsDataOnABodyThatIsNotAGroup(t *testing.T) {
	for _, body := range []string{`null`, `[]`, `"x"`, `{"error":"proxy says no"}`,
		`{"toggles": [null]}`, `{"toggles": [{}]}`, `{"value": [{"Value": "true"}]}`} {
		t.Run(body, func(t *testing.T) {
			b := newBackend(t)
			b.serve("h1", `{"toggles": [{"key": "k", "value": "true"}]}`)
			p := mustProvider(t, b.server.URL)
			p.Refresh(context.Background())

			b.serve("h2", body)
			if changed, err := p.Refresh(context.Background()); err == nil || changed {
				t.Errorf("Refresh = %v, %v; want an error", changed, err)
			}
			if !p.IsEnabled("k") {
				t.Error("the previous data was replaced")
			}
			// The hash was not recorded, so a corrected body loads.
			b.serve("h2", `{"toggles": [{"key": "k", "value": "false"}]}`)
			if changed, err := p.Refresh(context.Background()); err != nil || !changed || p.IsEnabled("k") {
				t.Errorf("the corrected body did not load: %v, %v", changed, err)
			}
		})
	}
}

func TestAPIAcceptsAnEmptyGroupAndASingleToggle(t *testing.T) {
	b := newBackend(t)
	b.serve("h1", `{"toggles": []}`)
	p := mustProvider(t, b.server.URL)
	if _, err := p.Refresh(context.Background()); err != nil {
		t.Errorf("empty group refused: %v", err)
	}
	// An unusable entry next to a good one is skipped, not fatal (R25).
	b.serve("h3", `{"toggles": [null, {"key": "`+group+`|kept", "value": "true"}]}`)
	if _, err := p.Refresh(context.Background()); err != nil || !p.IsEnabled("kept") {
		t.Errorf("mixed collection refused: %v", err)
	}
	b.serve("h2", `{"key": "`+group+`|solo", "value": "true"}`)
	if _, err := p.Refresh(context.Background()); err != nil || !p.IsEnabled("solo") {
		t.Errorf("single toggle refused: %v", err)
	}
}

func TestAPIRetriesAFailedFetchEvenIfTheHashIsUnchanged(t *testing.T) {
	b := newBackend(t)
	b.json("/collectionHash/"+group, 200, `{"collectionHash":"h1"}`)
	b.json("/features/"+group, 503, `{}`)
	p := mustProvider(t, b.server.URL)
	if _, err := p.Refresh(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
	b.serve("h1", `{"toggles": [{"key": "k", "value": "true"}]}`)
	if changed, err := p.Refresh(context.Background()); err != nil || !changed || !p.IsEnabled("k") {
		t.Errorf("hash was recorded before the fetch succeeded: %v %v", changed, err)
	}
}

func TestAPIRejectsAnOversizedBody(t *testing.T) {
	b := newBackend(t)
	b.serve("h1", `{"toggles": [`+strings.Repeat(`{"key":"k","value":"true"},`, 100)+`{}]}`)
	p := mustProvider(t, b.server.URL, yaft.WithMaxBodyBytes(512))
	if _, err := p.Refresh(context.Background()); err == nil || !strings.Contains(err.Error(), "more than 512 bytes") {
		t.Errorf("err = %v", err)
	}
}

func TestAPIRejectsGarbageAndAMissingHash(t *testing.T) {
	b := newBackend(t)
	b.serve("h1", `not json`)
	if _, err := mustProvider(t, b.server.URL).Refresh(context.Background()); err == nil || !strings.Contains(err.Error(), "does not parse") {
		t.Errorf("err = %v", err)
	}
	b.json("/collectionHash/"+group, 200, `{"somethingElse": 1}`)
	if _, err := mustProvider(t, b.server.URL).Refresh(context.Background()); err == nil || !strings.Contains(err.Error(), "no collectionHash") {
		t.Errorf("err = %v", err)
	}
}

func TestAPIDoesNotFollowRedirects(t *testing.T) {
	b := newBackend(t)
	b.routes["/collectionHash/"+group] = func(w http.ResponseWriter) {
		w.Header().Set("Location", "/elsewhere")
		w.WriteHeader(http.StatusFound)
	}
	b.json("/elsewhere", 200, `{"collectionHash":"h1"}`)
	if _, err := mustProvider(t, b.server.URL).Refresh(context.Background()); err == nil {
		t.Error("expected an error")
	}
	if b.count("/elsewhere") != 0 {
		t.Error("followed the redirect")
	}
}

// A body that starts and then stalls must not outlast the timeout: the Java
// port had exactly this hole, found in review.
func TestAPITimesOutOnABodyThatStalls(t *testing.T) {
	b := newBackend(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	b.routes["/collectionHash/"+group] = func(w http.ResponseWriter) {
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(200)
		fmt.Fprint(w, `{"collec`)
		w.(http.Flusher).Flush()
		<-release
	}
	p := mustProvider(t, b.server.URL, yaft.WithTimeout(300*time.Millisecond))

	start := time.Now()
	if _, err := p.Refresh(context.Background()); err == nil {
		t.Fatal("expected a timeout")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("refresh took %v against a 300ms timeout", elapsed)
	}
}

func TestAPIRejectsUnusableInput(t *testing.T) {
	for _, bad := range []string{"../secret", group + "/x", group + "%2F", "1-1-1-1-1", group + " ", "", "not-a-uuid"} {
		if _, err := yaft.NewAPIFeatureProvider("https://host", bad); err == nil {
			t.Errorf("group %q accepted", bad)
		}
	}
	for _, bad := range []string{"file:///etc/passwd", "ftp://host", "localhost:8080", "https://host/?q=1", "https://host/#f", "https://"} {
		if _, err := yaft.NewAPIFeatureProvider(bad, group); err == nil {
			t.Errorf("base URL %q accepted", bad)
		}
	}
	// Upper case is the same UUID.
	if _, err := yaft.NewAPIFeatureProvider("https://host/", strings.ToUpper(group)); err != nil {
		t.Error(err)
	}
}

func TestAPIWorksWithFunc(t *testing.T) {
	b := newBackend(t)
	b.serve("h1", `{"toggles": [{"key": "`+group+`|loud", "value": "true"}]}`)
	p := mustProvider(t, b.server.URL)
	p.Refresh(context.Background())
	saved := yaft.Provider()
	t.Cleanup(func() { yaft.SetProvider(saved) })
	yaft.SetProvider(p)

	greet := yaft.Func("loud", func() string { return "HELLO" }, func() string { return "hello" })
	if got := greet(); got != "HELLO" {
		t.Errorf("greet() = %q", got)
	}
}
