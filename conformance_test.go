package yaft_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	yaft "github.com/tehw0lf/yaft-go"
	"github.com/tehw0lf/yaft-go/internal/shelltest"
)

// The conformance suite, fetched by `go generate` from the version pinned in
// conformance.lock. Every case must pass; an unknown value in a case fails the
// test instead of being skipped, because a skipped case is a rule nothing
// enforces.

// formats are the case-file format versions this adapter implements. A file
// in another format may carry a field this adapter never reads.
var formats = map[string]float64{"evaluation": 1, "decorator": 1, "mapping": 3}

type caseFile struct {
	Suite   string           `json:"suite"`
	Version float64          `json:"version"`
	Cases   []map[string]any `json:"cases"`
}

func loadCases(t *testing.T, suite string) []map[string]any {
	t.Helper()
	path := filepath.Join("testdata", "conformance", "cases", suite+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("conformance cases missing at %s -- run `go generate ./...` first: %v", path, err)
	}
	var file caseFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if file.Suite != suite {
		t.Fatalf("%s declares suite %q, expected %q", path, file.Suite, suite)
	}
	if file.Version != formats[suite] {
		t.Fatalf("%s is format version %v, but this adapter implements %v; extend the adapter", path, file.Version, formats[suite])
	}
	if len(file.Cases) == 0 {
		t.Fatalf("%s contains no cases", path)
	}
	return file.Cases
}

func title(c map[string]any) string {
	return fmt.Sprintf("%s %v", c["name"], c["rules"])
}

func unsupported(t *testing.T, kind string, value any, c map[string]any) {
	t.Helper()
	t.Fatalf("case %q uses %s %q, which this adapter does not implement; extend the adapter rather than skipping the case",
		c["name"], kind, value)
}

func TestConformanceEvaluation(t *testing.T) {
	for _, c := range loadCases(t, "evaluation") {
		t.Run(title(c), func(t *testing.T) {
			// Parsed with time.Parse, not the code under test, so a parser bug
			// cannot move the case's own clock.
			now, err := time.Parse(time.RFC3339Nano, c["now"].(string))
			if err != nil {
				t.Fatalf("case now: %v", err)
			}

			var feature *yaft.Feature
			if raw, ok := c["features"].(map[string]any)[c["key"].(string)].(map[string]any); ok {
				// Taken as they are: a missing or non-string field stays "".
				str := func(k string) string { s, _ := raw[k].(string); return s }
				feature = &yaft.Feature{Key: str("key"), Value: str("value"), ActiveAt: str("activeAt"), DisabledAt: str("disabledAt")}
			}

			if got, want := yaft.Evaluate(feature, now), c["expected"].(bool); got != want {
				t.Errorf("Evaluate = %v, want %v", got, want)
			}
		})
	}
}

func TestConformanceMapping(t *testing.T) {
	for _, c := range loadCases(t, "mapping") {
		t.Run(title(c), func(t *testing.T) {
			expected := c["expected"].(map[string]any)
			switch c["shape"] {
			case "feature":
				if held, ok := c["held"].(map[string]any); ok {
					b, p := refreshOver(t, held, c["response"])
					if got := fields(p.Data()); !reflect.DeepEqual(got, expected) {
						t.Errorf("data after the refresh =\n  %v\nwant\n  %v", got, expected)
					}
					if retry, ok := c["retry"].(map[string]any); ok {
						// Same hash: a port that recorded it on the rejected
						// body never fetches again (R30).
						b.serve("response", mustJSON(t, retry["response"]))
						if _, err := p.Refresh(context.Background()); err != nil {
							t.Errorf("retry: %v", err)
						}
						if got := fields(p.Data()); !reflect.DeepEqual(got, retry["expected"]) {
							t.Errorf("data after the retry =\n  %v\nwant\n  %v", got, retry["expected"])
						}
					}
					return
				}
				if got := fields(yaft.NormaliseCollection(c["response"])); !reflect.DeepEqual(got, expected) {
					t.Errorf("NormaliseCollection =\n  %v\nwant\n  %v", got, expected)
				}
			case "boolean":
				provider := yaft.LocalBooleanProviderFromResponse(c["response"])
				got := map[string]any{}
				for key, b := range provider.Data() {
					got[key] = b
				}
				if !reflect.DeepEqual(got, expected) {
					t.Errorf("data = %v, want %v", got, expected)
				}
				// Includes keys absent from the data (R21).
				for key, want := range c["isEnabled"].(map[string]any) {
					if got := provider.IsEnabled(key); got != want.(bool) {
						t.Errorf("IsEnabled(%q) = %v, want %v", key, got, want)
					}
				}
			default:
				unsupported(t, "shape", c["shape"], c)
			}
		})
	}
}

// refreshOver runs a refresh case (R30) through the real API provider: held
// is served and loaded first, then response under a new hash. The second
// refresh fails for a body that is not a group; that is expected, and the
// data it leaves behind is what the case asserts.
func refreshOver(t *testing.T, held map[string]any, response any) (*backend, *yaft.APIFeatureProvider) {
	t.Helper()
	toggles := make([]any, 0, len(held))
	for _, f := range held {
		toggles = append(toggles, f)
	}
	b := newBackend(t)
	b.serve("held", mustJSON(t, map[string]any{"toggles": toggles}))
	p := mustProvider(t, b.server.URL)
	if changed, err := p.Refresh(context.Background()); err != nil || !changed {
		t.Fatalf("loading held: changed=%v err=%v", changed, err)
	}
	b.serve("response", mustJSON(t, response))
	if _, err := p.Refresh(context.Background()); err != nil {
		t.Logf("refresh failed, as it must for a body that is not a group: %v", err)
	}
	return b, p
}

// fields writes features in the case files' own spelling.
func fields(data map[string]yaft.Feature) map[string]any {
	got := map[string]any{}
	for key, f := range data {
		tags := make([]any, len(f.Tags))
		for i, tag := range f.Tags {
			tags[i] = tag
		}
		got[key] = map[string]any{"key": f.Key, "value": f.Value, "activeAt": f.ActiveAt, "disabledAt": f.DisabledAt, "tags": tags}
	}
	return got
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// --- decorator --------------------------------------------------------------

type which = shelltest.Which

type original struct{}

func (original) Which() string { return "original" }

type fallbackImpl struct{}

func (fallbackImpl) Which() string { return "fallback" }

// switchable is a provider whose answer can change between wrapping and call.
type switchable struct{ on bool }

func (s *switchable) IsEnabled(string) bool { return s.on }

// subject records what ran and what the fallback was handed.
type subject struct {
	ran      string
	args     []any
	receiver *subject
}

func (s *subject) Run(a string, b int) *string {
	s.ran = "original"
	r := "original"
	return &r
}

func (s *subject) Fallback(a string, b int) *string {
	s.ran, s.args, s.receiver = "fallback", []any{a, b}, s
	r := "fallback"
	return &r
}

func (s *subject) Async() <-chan string {
	s.ran = "original"
	ch := make(chan string, 1)
	ch <- "original"
	close(ch)
	return ch
}

func TestConformanceDecorator(t *testing.T) {
	saved := yaft.Provider()
	t.Cleanup(func() { yaft.SetProvider(saved) })

	for _, c := range loadCases(t, "decorator") {
		t.Run(title(c), func(t *testing.T) {
			defer yaft.SetProvider(saved)

			toggle := c["toggle"].(string)
			if toggle == "no-provider" {
				noProvider(t, c)
				return
			}
			provider := &switchable{}
			switch toggle {
			case "on", "on-then-off":
				provider.on = true
			case "off", "off-then-on":
			default:
				unsupported(t, "toggle", toggle, c)
			}
			yaft.SetProvider(provider)
			flip := func() {
				if toggle == "on-then-off" {
					provider.on = false
				}
				if toggle == "off-then-on" {
					provider.on = true
				}
			}

			switch c["target"] {
			case "method":
				methodCase(t, c, flip)
			case "async-method":
				asyncCase(t, c, flip)
			case "class":
				classCase(t, c, flip)
			default:
				unsupported(t, "target", c["target"], c)
			}
		})
	}
}

func methodCase(t *testing.T, c map[string]any, flip func()) {
	s := &subject{}
	var run func(string, int) *string
	switch c["fallback"] {
	case "method":
		run = yaft.Func("k", s.Run, s.Fallback)
	case "none":
		run = yaft.Func("k", s.Run, nil)
	default:
		unsupported(t, "fallback", c["fallback"], c)
	}
	flip()
	result := run("a", 1)

	switch c["expected"] {
	case "original", "fallback":
		want := c["expected"].(string)
		if s.ran != want || result == nil || *result != want {
			t.Errorf("ran %q, result %v, want %q", s.ran, result, want)
		}
	case "nothing":
		// Go's "nothing": the zero value -- nil for the *string result.
		if s.ran != "" || result != nil {
			t.Errorf("ran %q, result %v, want nothing", s.ran, result)
		}
	default:
		unsupported(t, "expected", c["expected"], c)
	}

	assertions, _ := c["assertions"].([]any)
	for _, a := range assertions {
		switch a {
		case "same-arguments":
			if !reflect.DeepEqual(s.args, []any{"a", 1}) {
				t.Errorf("fallback got %v", s.args)
			}
		case "same-receiver":
			// A method value keeps its receiver.
			if s.receiver != s {
				t.Errorf("fallback ran on %p, want %p", s.receiver, s)
			}
		default:
			unsupported(t, "assertion", a, c)
		}
	}
}

func asyncCase(t *testing.T, c map[string]any, flip func()) {
	if c["fallback"] != "none" {
		unsupported(t, "fallback", c["fallback"], c)
	}
	s := &subject{}
	async := yaft.Func("k", s.Async, nil)
	flip()
	ch := async()

	switch c["expected"] {
	case "original":
		if v := <-ch; v != "original" || s.ran != "original" {
			t.Errorf("got %q, ran %q", v, s.ran)
		}
	case "resolved-nothing":
		// Go's resolved promise: a closed channel. A nil one would block the
		// receive below forever, which is what R18 exists to prevent.
		select {
		case v, open := <-ch:
			if open || v != "" || s.ran != "" {
				t.Errorf("got %q (open %v), ran %q", v, open, s.ran)
			}
		case <-time.After(time.Second):
			t.Fatal("receive blocked: nothing must be a closed channel, not nil")
		}
	default:
		unsupported(t, "expected", c["expected"], c)
	}
}

func classCase(t *testing.T, c map[string]any, flip func()) {
	var fallback func() which
	switch c["fallback"] {
	case "class":
		fallback = func() which { return fallbackImpl{} }
	case "none":
		// Go cannot synthesise an implementation at runtime; "no fallback"
		// is the shell yaft-shell generated for Which.
		fallback = shelltest.NewNoopWhich
	default:
		unsupported(t, "fallback", c["fallback"], c)
	}

	constructor := yaft.ChooseConstructor("k", func() which { return original{} }, fallback)
	flip() // decided already, so this must not matter (R14)
	instance := constructor()

	switch c["expected"] {
	case "original", "fallback":
		if got := instance.Which(); got != c["expected"] {
			t.Errorf("Which() = %q, want %q", got, c["expected"])
		}
	case "empty-shell":
		if _, isShell := instance.(shelltest.NoopWhich); !isShell || instance.Which() != "" {
			t.Errorf("got %T, want the empty shell answering nothing", instance)
		}
	default:
		unsupported(t, "expected", c["expected"], c)
	}
}

// noProvider: wrapping itself must fail, not the first call (R16).
func noProvider(t *testing.T, c map[string]any) {
	if c["expected"] != "decoration-error" {
		unsupported(t, "expected", c["expected"], c)
	}
	yaft.SetProvider(nil)

	var wrap func()
	switch c["target"] {
	case "method":
		wrap = func() { yaft.Func("k", (&subject{}).Run, nil) }
	case "async-method":
		wrap = func() { yaft.Func("k", (&subject{}).Async, nil) }
	case "class":
		wrap = func() { yaft.Choose("k", func() which { return original{} }, shelltest.NewNoopWhich) }
	default:
		unsupported(t, "target", c["target"], c)
	}

	defer func() {
		err, _ := recover().(error)
		if !errors.Is(err, yaft.ErrNoProvider) || !strings.Contains(err.Error(), "FeatureToggleProvider not set") {
			t.Errorf("recovered %v, want ErrNoProvider", err)
		}
	}()
	wrap()
}
