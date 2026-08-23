## What and why

<!-- What changes, and what problem it solves. The diff says what; this should say why. -->

## How it's verified

<!-- Which test covers this? Confirm it FAILS without the fix — stash the change and watch it go red.
     For behaviour that tests can't reach, say what you ran by hand and what you saw. -->

- [ ] `make check` is green (read the output — a pipeline's exit code is its *last* command's)
- [ ] New behaviour has a test that fails without the change
- [ ] Structural change? An ADR in `docs/design/adr/` explains the decision
- [ ] Token usage touched? Charged in **both** `internal/state` and `internal/metrics`
- [ ] Docs updated if this changes a flag, a config field, or a documented claim
