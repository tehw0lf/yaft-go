// Package yaft is YaFT -- Yet another Feature Toggle -- for Go.
//
// Go has no decorators, so toggles wrap functions and constructors instead:
//
//	yaft.SetProvider(yaft.NewLocalFeatureProvider(features, nil))
//
//	// Method toggle: evaluated on every call.
//	greet := yaft.Func("fancyGreeting", s.Fancy, s.Plain)
//
//	// Class toggle: decided once, when Choose runs.
//	checkout := yaft.Choose("newCheckout", NewCheckout, NewClassicCheckout)
//
// The behaviour follows the language-neutral rules of yaft-conformance
// (https://github.com/tehw0lf/yaft-conformance), whose cases this package
// passes.
package yaft

//go:generate bash scripts/fetch-conformance.sh testdata/conformance
