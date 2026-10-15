# Dev server for local runs and golden histories. It registers the Search
# Attributes the workloads in internal/workloads use.
dev_port := "17233"

default:
    @just --list

build:
    go build -o bin/tcopt ./cmd/tcopt

# Build with Ziggy. Only for internal use until brand approves the mascot.
build-brand:
    go build -tags brand -o bin/tcopt ./cmd/tcopt

test:
    go test ./...
    go test -tags brand ./internal/tui

vet:
    go vet ./...
    go vet -tags brand ./internal/tui

# Registers every Search Attribute the workload groups need.
dev-server port=dev_port:
    temporal server start-dev --port {{port}} --ui-port $(( {{port}} + 1000 )) --headless \
      $(go run ./tools/genhistories -search-attributes)

# Re-record testdata/histories from the workloads. Needs `just dev-server`.
goldens group="" port=dev_port:
    go run ./tools/genhistories -address localhost:{{port}} -group "{{group}}"

# Start n executions of the workloads, so analyze has something to find.
demo-load n="300":
    go run ./tools/genhistories -address localhost:{{dev_port}} -load {{n}}

# Analyze the dev server with the `devlocal` profile in .dev/temporal.toml.
demo-analyze *args:
    go run ./cmd/tcopt analyze --config-file .dev/temporal.toml --profile devlocal {{args}}

demo-tui *args:
    go run -tags brand ./cmd/tcopt --config-file .dev/temporal.toml --profile devlocal {{args}}

# Regenerate the half-block Ziggy sprite from the buildkit's brand assets.
ziggy buildkit="~/Source/temporal-toolkit-demo-buildkit":
    go run ./tools/ziggygen -in {{buildkit}}/assets/brand/ziggy-peek.png \
      -blink {{buildkit}}/assets/brand/ziggy-blink.png -out internal/tui/ziggy_brand.go
