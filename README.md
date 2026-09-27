# YaFT for Go

<div align="center">
  <img src="./logo.svg" alt="YaFT Logo" width="140">
</div>

---

Feature toggles for functions and implementations in Go, following the same
rules as [`@tehw0lf/yaft`](https://github.com/tehw0lf/yaft-ts) and
[`de.tehwolf:yaft`](https://github.com/tehw0lf/yaft-java). It passes every case
of [yaft-conformance](https://github.com/tehw0lf/yaft-conformance) and depends
only on the standard library.

---

## Installation

```bash
go get github.com/tehw0lf/yaft-go
```

```go
import yaft "github.com/tehw0lf/yaft-go"
```

## Providers

Set one provider at startup:

```go
yaft.SetProvider(yaft.NewLocalFeatureProvider(map[string]yaft.Feature{
	"newCheckout": {Key: "newCheckout", Value: "true"},
}, nil))
```

| Provider | Data | Time bounds |
|---|---|---|
| `LocalFeatureProvider` | full `Feature` records | yes — `ActiveAt`, `DisabledAt` |
| `LocalBooleanProvider` | `{"myToggle": true}` | none, by design |
| `APIFeatureProvider` | a group from the YaFT backend | yes, evaluated locally |

`FeatureProvider` is a one-method interface; `yaft.ProviderFunc` adapts a
function.

A feature is on only when `Value` is exactly `"true"` and the current time is
inside `[ActiveAt, DisabledAt)`. Bounds must be RFC 3339 with an offset; any
other format is ignored with a warning, never guessed at. The clock is
injectable (`yaft.Clock`).

### From a YaFT backend

```go
p, err := yaft.NewAPIFeatureProvider("https://yaft.tehwolf.de", groupUUID)
if err != nil { … }
if _, err := p.Refresh(ctx); err != nil { … }   // fail the start rather than run with everything off
yaft.SetProvider(p)

go func() {
	for range time.Tick(30 * time.Second) {
		p.RefreshQuietly(ctx)
	}
}()
```

- Toggles are looked up by name within the group: `p.IsEnabled("newCheckout")`
  finds `<uuid>|newCheckout`, so code never needs the group's UUID. The full key
  works too.
- `Refresh` asks `/collectionHash/{uuid}` first and fetches the group only when
  it changed. A failed refresh keeps the previous data.
- Evaluation is local, so a scheduled toggle flips at its exact instant.
- The group UUID is checked strictly before it enters the URL; only `http(s)`,
  no redirects, a 5-second timeout for the whole exchange **including the
  body**, and a 1 MiB body limit. `WithTimeout`, `WithMaxBodyBytes`,
  `WithHTTPClient` and `WithClock` change these.

## Toggling code

Go has no decorators, so toggles wrap functions and constructors.

### Functions — evaluated on every call

```go
s := &Service{}
greet := yaft.Func("fancyGreeting", s.Fancy, s.Plain)
greet("ada")   // s.Fancy while the toggle is on, s.Plain while it is off
```

Original and fallback have the same type, and a method value keeps its
receiver, so the fallback runs on the same `s`. With a `nil` fallback a
disabled call returns nothing: every result's zero value — except a channel you
can receive from, which comes back **closed** instead of nil, so
`<-ch` returns rather than blocking forever.

### Implementations — decided once

```go
checkout := yaft.Choose("newCheckout", NewCheckout, NewClassicCheckout)
```

`Choose` reads the toggle **once**, when it runs; changing the toggle later does
not swap `checkout`. `ChooseConstructor` returns the chosen constructor instead,
for creating many instances.

### An implementation without a fallback

A disabled implementation without its own fallback becomes an *empty shell*:
every method returns nothing. Go cannot build one at runtime, so `yaft-shell`
generates it:

```go
//go:generate go run github.com/tehw0lf/yaft-go/cmd/yaft-shell -type Checkout

checkout := yaft.Choose("newCheckout", NewCheckout, NewNoopCheckout)
```

This writes `checkout_yaftshell.go` with `NoopCheckout` and `NewNoopCheckout`.
It refuses what it cannot generate correctly: a generic interface, or one that
embeds an interface from another package.

### When a toggle is read

| | Evaluated |
|---|---|
| `yaft.Choose` | **once**, when it runs |
| `yaft.Func` | on **every call** |

### Fail fast

`Func` and `Choose` panic when they are called — not on the first toggled call
— if no provider is set (`yaft.ErrNoProvider`, "FeatureToggleProvider not
set"), or if they are given something unusable. A misconfiguration shows at
startup.

## Conformance

`conformance.lock` pins a release of the suite by version and checksum.
`go generate ./...` fetches and verifies it; `go test ./...` then runs every case
next to this package's own tests.

## Development

```bash
go generate ./... && go vet ./... && go test -race ./...
```

## License

MIT
