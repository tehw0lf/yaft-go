package yaft_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	yaft "github.com/tehw0lf/yaft-go"
)

func withToggles(t *testing.T, on map[string]bool) {
	t.Helper()
	saved := yaft.Provider()
	t.Cleanup(func() { yaft.SetProvider(saved) })
	yaft.SetProvider(yaft.NewLocalBooleanProvider(on))
}

func TestNothingIsTheZeroValueExceptForReceivableChannels(t *testing.T) {
	withToggles(t, nil)

	number := yaft.Func("k", func() int { return 7 }, nil)
	multi := yaft.Func("k", func() (string, error) { return "x", errors.New("boom") }, nil)
	recv := yaft.Func("k", func() <-chan int { return nil }, nil)
	both := yaft.Func("k", func() chan int { return nil }, nil)
	send := yaft.Func("k", func() chan<- int { return nil }, nil)
	void := yaft.Func("k", func() { t.Error("ran") }, nil)

	if number() != 0 {
		t.Error("int is not 0")
	}
	if s, err := multi(); s != "" || err != nil {
		t.Errorf("got %q, %v", s, err)
	}
	for name, ch := range map[string]<-chan int{"<-chan": recv(), "chan": both()} {
		select {
		case _, open := <-ch:
			if open {
				t.Errorf("%s is open", name)
			}
		case <-time.After(time.Second):
			t.Errorf("%s blocks: nothing must be closed, not nil", name)
		}
	}
	if send() != nil {
		t.Error("a send-only channel must stay nil; a closed one would panic on send")
	}
	void()
}

func TestFuncHandlesVariadicFunctions(t *testing.T) {
	withToggles(t, map[string]bool{"k": true})
	sum := yaft.Func("k", func(prefix string, xs ...int) string {
		total := 0
		for _, x := range xs {
			total += x
		}
		return prefix + strings.Repeat("|", total)
	}, nil)
	if got := sum("s", 1, 2); got != "s|||" {
		t.Errorf("got %q", got)
	}
}

func TestFuncPropagatesPanics(t *testing.T) {
	withToggles(t, map[string]bool{"k": true})
	boom := yaft.Func("k", func() { panic("boom") }, nil)
	defer func() {
		if recover() != "boom" {
			t.Error("the original panic was not propagated")
		}
	}()
	boom()
}

func TestWrappingFailsForUnusableArguments(t *testing.T) {
	withToggles(t, nil)
	cases := map[string]func(){
		"nil original func": func() { yaft.Func[func()]("k", nil, nil) },
		"not a function":    func() { yaft.Func("k", 42, 0) },
		"nil constructor":   func() { yaft.Choose[int]("k", nil, func() int { return 0 }) },
		"no fallback":       func() { yaft.Choose("k", func() int { return 1 }, nil) },
	}
	for name, wrap := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("did not panic at wrapping")
				}
			}()
			wrap()
		})
	}
}

func TestNoFallbackPointsAtYaftShell(t *testing.T) {
	withToggles(t, nil)
	defer func() {
		msg, _ := recover().(string)
		if !strings.Contains(msg, "yaft-shell") {
			t.Errorf("panic %q does not name yaft-shell", msg)
		}
	}()
	yaft.Choose("k", func() int { return 1 }, nil)
}

func TestEvaluateEdges(t *testing.T) {
	at := func(s string) time.Time { v, _ := time.Parse(time.RFC3339Nano, s); return v }
	cases := []struct {
		name  string
		value string
		want  time.Time
		ok    bool
	}{
		{"fraction truncated", "2026-09-18T12:00:00.123999999Z", at("2026-09-18T12:00:00.123Z"), true},
		{"beyond eighteen hours", "2026-09-18T20:00:00+20:00", at("2026-09-18T00:00:00Z"), true},
		{"offset hour 24", "2026-09-18T12:00:00+24:00", time.Time{}, false},
		{"offset minute 60", "2026-09-18T12:00:00+02:60", time.Time{}, false},
		{"non-ASCII digits", "٢٠٢٦-09-18T12:00:00Z", time.Time{}, false},
		{"february 29 of a leap year", "2028-02-29T00:00:00Z", at("2028-02-29T00:00:00Z"), true},
		{"february 29 of 2100", "2100-02-29T00:00:00Z", time.Time{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := yaft.ParseTimestamp(c.value)
			if ok != c.ok || !got.Equal(c.want) {
				t.Errorf("ParseTimestamp(%q) = %v, %v; want %v, %v", c.value, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestProvidersCopyTheirData(t *testing.T) {
	data := map[string]bool{"t": true}
	p := yaft.NewLocalBooleanProvider(data)
	data["t"] = false
	if !p.IsEnabled("t") {
		t.Error("the provider shares the caller's map")
	}
	p.Data()["t"] = false
	if !p.IsEnabled("t") {
		t.Error("Data() exposes the provider's map")
	}

	features := yaft.NewLocalFeatureProvider(map[string]yaft.Feature{"f": {Key: "f", Value: "true"}}, nil)
	if !features.IsEnabled("f") || features.IsEnabled("missing") {
		t.Error("feature provider answers wrongly")
	}
}
