package yaft

import (
	"maps"
	"sync/atomic"
)

// FeatureProvider supplies feature data and answers whether a feature is on.
//
// A feature-shape provider holds full Feature records and delegates to
// Evaluate (R20), like LocalFeatureProvider. A boolean-shape provider holds
// plain booleans with no time logic (R21), like LocalBooleanProvider.
type FeatureProvider interface {
	IsEnabled(key string) bool
}

// ProviderFunc adapts a function to FeatureProvider:
//
//	yaft.SetProvider(yaft.ProviderFunc(func(key string) bool {
//		return os.Getenv("FEATURE_"+key) != ""
//	}))
type ProviderFunc func(key string) bool

// IsEnabled calls f.
func (f ProviderFunc) IsEnabled(key string) bool { return f(key) }

// LocalFeatureProvider is a feature-shape provider over data held in memory.
// Time bounds are evaluated against its clock. Safe for concurrent use;
// Replace swaps the data atomically.
type LocalFeatureProvider struct {
	clock Clock
	data  atomic.Pointer[map[string]Feature]
}

// NewLocalFeatureProvider evaluates data against clock; a nil clock means
// time.Now. The map is copied.
func NewLocalFeatureProvider(data map[string]Feature, clock Clock) *LocalFeatureProvider {
	p := &LocalFeatureProvider{clock: clock}
	p.Replace(data)
	return p
}

// LocalFeatureProviderFromResponse builds a provider from a parsed JSON
// response, in any of the backend's envelopes and field spellings.
func LocalFeatureProviderFromResponse(response any, clock Clock) *LocalFeatureProvider {
	return NewLocalFeatureProvider(NormaliseCollection(response), clock)
}

// Replace swaps the data, for example after reloading it. The map is copied.
func (p *LocalFeatureProvider) Replace(data map[string]Feature) {
	copied := maps.Clone(data)
	if copied == nil {
		copied = map[string]Feature{}
	}
	p.data.Store(&copied)
}

// Data returns a copy of the current data.
func (p *LocalFeatureProvider) Data() map[string]Feature {
	return maps.Clone(*p.data.Load())
}

// IsEnabled evaluates the feature stored under key; a missing key is off.
func (p *LocalFeatureProvider) IsEnabled(key string) bool {
	feature, ok := (*p.data.Load())[key]
	if !ok {
		return false
	}
	return Evaluate(&feature, p.clock.now())
}

// LocalBooleanProvider is a boolean-shape provider: {"myToggle": true}. It
// maps straight onto IsEnabled with no time logic, by design (R21). Safe for
// concurrent use.
type LocalBooleanProvider struct {
	data atomic.Pointer[map[string]bool]
}

// NewLocalBooleanProvider answers from data. The map is copied.
func NewLocalBooleanProvider(data map[string]bool) *LocalBooleanProvider {
	p := &LocalBooleanProvider{}
	p.Replace(data)
	return p
}

// LocalBooleanProviderFromResponse builds a provider from a parsed JSON
// object, keeping only real booleans (R29).
func LocalBooleanProviderFromResponse(response any) *LocalBooleanProvider {
	return NewLocalBooleanProvider(NormaliseBooleans(response))
}

// Replace swaps the data. The map is copied.
func (p *LocalBooleanProvider) Replace(data map[string]bool) {
	copied := maps.Clone(data)
	if copied == nil {
		copied = map[string]bool{}
	}
	p.data.Store(&copied)
}

// Data returns a copy of the current data.
func (p *LocalBooleanProvider) Data() map[string]bool {
	return maps.Clone(*p.data.Load())
}

// IsEnabled reports the stored boolean; a missing key is off.
func (p *LocalBooleanProvider) IsEnabled(key string) bool {
	return (*p.data.Load())[key]
}
