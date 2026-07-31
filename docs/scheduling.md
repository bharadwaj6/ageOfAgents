# Scheduling `aoa`

`aoa run` reconciles until the work is settled and then exits `0`. It holds no state between
invocations — everything it knows is replayed from the Event Log — so **running it again is always
safe**, whether the last run finished, crashed, or was killed mid-dispatch. A run with nothing to do
prints `no goals submitted` and exits `0`.

That property is the whole scheduling story: `aoa` needs no daemon, no supervisor, and no leader
election. Point any ordinary scheduler at it.

> **Why there is no `aoa daemon`.** A long-lived supervisor would be a second control loop, and the
> design has exactly one (ADR 003). `cron`, `systemd`, and GitHub Actions already solve restart,
> backoff, logging, and alerting better than a bespoke daemon would, and none of them can corrupt the
> log. See [`design/loop_engineering.md`](design/loop_engineering.md) for the fuller argument.

## cron

```cron
# Reconcile every 15 minutes. Output goes to the log; `aoa` is quiet when idle.
*/15 * * * * cd /srv/myrepo-workspace && /usr/local/bin/aoa run --path . >> /var/log/aoa.log 2>&1
```

Overlapping runs must be prevented — two runs against one workspace race the same Event Log. Wrap the
command in `flock`:

```cron
*/15 * * * * /usr/bin/flock -n /tmp/aoa.lock /usr/local/bin/aoa run --path /srv/myrepo-workspace
```

`flock -n` skips the tick entirely when the previous run is still going, which is the behavior you
want: the next tick picks the work up anyway.

## systemd timer

A timer gives you `journalctl` and restart policy for free. Two units:

```ini
# /etc/systemd/system/aoa.service
[Unit]
Description=Age of Agents reconcile pass

[Service]
Type=oneshot
WorkingDirectory=/srv/myrepo-workspace
ExecStart=/usr/local/bin/aoa run --path .
# Give a stuck run a hard bound; the next timer tick resumes from the log.
TimeoutStartSec=30m
```

```ini
# /etc/systemd/system/aoa.timer
[Unit]
Description=Reconcile Age of Agents every 15 minutes

[Timer]
OnBootSec=5min
OnUnitActiveSec=15min
# Do not stack ticks if a run overruns.
AccuracySec=1min

[Install]
WantedBy=timers.target
```

```bash
systemctl enable --now aoa.timer
```

`Type=oneshot` means systemd will not start a second run while one is active, so no lock file is needed.

## GitHub Actions

The bundled action (`action.yml`) runs a single pass. Drive it on a schedule:

```yaml
name: aoa
on:
  schedule:
    - cron: "*/30 * * * *"   # every 30 minutes, UTC
  workflow_dispatch:          # and on demand

concurrency:
  group: aoa-${{ github.ref }}
  cancel-in-progress: false   # queue, never overlap

jobs:
  reconcile:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: bharadwaj6/ageOfAgents@main
        with:
          path: .
          otel: "true"        # optional: stream the run to your OTLP backend
```

The `concurrency` block is the Actions equivalent of `flock` — required, for the same reason.

## Interactive: `--interval`

For a machine you are sitting at, `aoa run --interval 5m` reconciles, prints status, waits, and repeats
until you interrupt it:

```bash
aoa run --path . --interval 5m
```

It exits cleanly on `ctrl-c` (`SIGINT`) or `SIGTERM`, and a failing cycle is reported to stderr without
ending the loop. This is a convenience, not a deployment target — it is a foreground process holding no
state, so prefer one of the schedulers above for anything unattended. `--interval` and `--once` are
mutually exclusive.

## Webhook-driven

`aoa serve` reconciles in response to `@aoa <goal>` issue comments rather than on a clock:

```bash
aoa serve --path . --port 8080 --secret "$GITHUB_WEBHOOK_SECRET"
```

Always set `--secret` — without it, signatures are not verified and anyone who can reach the port can
queue work. Deliveries are deduplicated by `X-GitHub-Delivery` and written to the log with an
idempotency key, so GitHub's at-least-once redelivery cannot fork a duplicate Goal. Runs are
single-flight: a delivery arriving mid-run is queued and reconciled on the next cycle.

Scheduling and webhooks compose — a timer catches anything the webhook missed while the process was
down.

## What is *not* scheduled

Nothing discovers work on its own. Goals enter through `aoa goal` or an `@aoa` comment; `aoa` does not
poll CI failures, triage issues, or scan commits. A scheduled run reconciles the work you gave it — it
does not go looking for more. That gap is tracked in
[`design/loop_engineering.md`](design/loop_engineering.md).
