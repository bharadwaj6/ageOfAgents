# Examples

A copy-paste path for running `aoa` on **your own** repository, with cost accounting and
OpenTelemetry export.

## Adopt your repo and run

```sh
go build -o aoa ./cmd/aoa

# Point aoa at your existing repo (on whatever branch it's on). No files are
# written into your tree; the Gate is auto-detected from the project.
./aoa init --path ./ws --adopt /path/to/your-repo

# Edit ws/aoa.toml to taste — see sample-aoa.toml here and docs/config-reference.md.
# (Set backend = "claudecode" or "grok" for real work; "mock" stays offline.)

./aoa goal   --path ./ws "fix the failing test in the parser"
./aoa run    --path ./ws
./aoa status --path ./ws         # goals, tasks, tokens, $ cost, "needs human" handoffs
```

## With OpenTelemetry (any OTLP backend)

```sh
export OTEL_EXPORTER_OTLP_ENDPOINT="http://localhost:4318"   # Collector / vendor endpoint
export OTEL_SERVICE_NAME="aoa"

./aoa run --path ./ws --otel     # reconcile, then export traces + metrics
# or, after the fact:
./aoa otel export --path ./ws
```

See [`docs/integrations/`](../docs/integrations/README.md) for Honeycomb and other backends.

## Cost-sensitive evaluation

```sh
# Run a task suite under a $ ceiling, priced per model, exporting each task to OTLP.
./aoa eval --tasks tasks.toml --backend grok \
           --price-file sample-aoa.toml --max-cost 5.00 --otel
```

`--max-cost` stops launching further tasks once cumulative spend crosses the ceiling; the report footer
shows tasks run vs skipped. `sample-aoa.toml`'s `[pricing]` table doubles as the `--price-file`.
