package yaft

import (
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
)

// ErrNoProvider is the panic value of Func and Choose when no provider is set
// (R16). Its text matches the other YaFT ports.
var ErrNoProvider = errors.New("FeatureToggleProvider not set")

var current atomic.Pointer[FeatureProvider]

// SetProvider sets the provider every toggle asks; nil clears it.
func SetProvider(p FeatureProvider) {
	if p == nil {
		current.Store(nil)
		return
	}
	current.Store(&p)
}

// Provider returns the provider every toggle asks, or nil.
func Provider() FeatureProvider {
	if p := current.Load(); p != nil {
		return *p
	}
	return nil
}

func requireProvider() FeatureProvider {
	p := Provider()
	if p == nil {
		panic(ErrNoProvider)
	}
	return p
}

// Func puts a function behind the toggle key and evaluates it on every call
// (R15): while the toggle is on, calls go to original; while it is off, to
// fallback. Both have the same type, so a method value keeps its receiver --
// yaft.Func("k", s.Fancy, s.Plain) runs either on s (R19).
//
// A nil fallback returns "nothing": every result's zero value, except that a
// channel the caller can receive from comes back closed rather than nil, so a
// receive returns immediately instead of blocking forever (R18). A send-only
// channel stays nil.
//
// Func panics at the time of wrapping -- not on the first call -- if no
// provider is set (R16) or if original is not a non-nil function.
func Func[F any](key string, original, fallback F) F {
	requireProvider()

	on := reflect.ValueOf(original)
	if !on.IsValid() || on.Kind() != reflect.Func || on.IsNil() {
		panic(fmt.Sprintf("yaft.Func(%q): original must be a non-nil function, got %T", key, original))
	}
	off := reflect.ValueOf(fallback)
	hasFallback := off.IsValid() && !off.IsNil()
	signature := on.Type()

	wrapped := reflect.MakeFunc(signature, func(args []reflect.Value) []reflect.Value {
		switch {
		case requireProvider().IsEnabled(key):
			return call(on, signature, args)
		case hasFallback:
			return call(off, signature, args)
		default:
			return nothing(signature)
		}
	})
	return wrapped.Interface().(F)
}

func call(fn reflect.Value, signature reflect.Type, args []reflect.Value) []reflect.Value {
	if signature.IsVariadic() {
		return fn.CallSlice(args)
	}
	return fn.Call(args)
}

func nothing(signature reflect.Type) []reflect.Value {
	results := make([]reflect.Value, signature.NumOut())
	for i := range results {
		out := signature.Out(i)
		if out.Kind() == reflect.Chan && out.ChanDir()&reflect.RecvDir != 0 {
			ch := reflect.MakeChan(reflect.ChanOf(reflect.BothDir, out.Elem()), 0)
			ch.Close()
			results[i] = ch.Convert(out)
			continue
		}
		results[i] = reflect.Zero(out)
	}
	return results
}

// Choose decides once which implementation stands behind the toggle key and
// returns what its constructor builds (R14) -- the equivalent of a class
// decorator running when the class is loaded. Changing the toggle afterwards
// does not change the result; call Choose again to decide again.
//
// Go cannot build an implementation of an interface at runtime, so there is
// no automatic empty shell: pass a no-op fallback, which yaft-shell generates
// for any interface (see its documentation). Choose panics if no provider is
// set (R16) or a constructor is nil.
func Choose[T any](key string, original, fallback func() T) T {
	return ChooseConstructor(key, original, fallback)()
}

// ChooseConstructor is Choose for code that needs many instances: it decides
// once and returns the chosen constructor.
func ChooseConstructor[T any](key string, original, fallback func() T) func() T {
	provider := requireProvider()
	if original == nil {
		panic(fmt.Sprintf("yaft.Choose(%q): original constructor is nil", key))
	}
	if fallback == nil {
		panic(fmt.Sprintf("yaft.Choose(%q): no fallback. Go cannot build an empty implementation at runtime;"+
			" generate one with yaft-shell and pass its constructor", key))
	}
	if provider.IsEnabled(key) {
		return original
	}
	return fallback
}
