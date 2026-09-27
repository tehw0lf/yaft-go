# CLAUDE.md

Go port of YaFT (`github.com/tehw0lf/yaft-go`, package `yaft`). The normative
rules are `yaft-conformance/SPEC.md`; the TypeScript and Java ports are
references for behaviour, not for API shape.

## Layout

- `evaluate.go` — `Feature`, `Clock`, `Evaluate`, `ParseTimestamp` (R3–R13, R27, R28)
- `mapping.go` — backend response normalisation (R22–R25, R29)
- `provider.go` — `FeatureProvider`, local providers
- `api.go` — `APIFeatureProvider` over `net/http`; bare names resolve within
  the group (a lesson from yaft-java)
- `toggle.go` — `Func` (per call, `reflect.MakeFunc`), `Choose` /
  `ChooseConstructor` (once), `SetProvider`
- `cmd/yaft-shell` — go:generate tool for empty shells (R17); Go cannot build
  interface implementations at runtime
- `internal/shelltest` — the adapter's interface and its generated shell
- `conformance_test.go` — the suite adapter; unknown case values and unknown
  case-file format versions must fail, never be skipped

## Constraints

- Standard library only.
- `go.mod` stays on the oldest supported Go release (currently 1.26).
- `VERSION` is the release: a push to main tags `v<VERSION>`, and the tag is
  what `go get` resolves — proxy.golang.org caches it forever. Bump the patch
  on every PR.

## Pre-commit validation

```bash
go generate ./... && gofmt -l . && go vet ./... && go test -race ./... && go build ./...
```

`gofmt -l` must print nothing.
