# uptime-monitor

A small, self-hosted uptime monitor written in Go. It checks HTTP(S) endpoints and TCP ports on a schedule, opens and resolves incidents without flapping, sends alerts to Slack and email, serves a public status page, and exposes Prometheus metrics. State lives in Postgres, and you can run several replicas at once: each check still runs exactly once per interval.

```
Go 1.24, Postgres 16, database/sql + pgx, html/template, Prometheus, Docker, Terraform (Hetzner Cloud), GitHub Actions
```

## Features

- **Checks:** HTTP(S) with method, expected status, timeout and optional body substring; TCP connect checks.
- **Scheduler:** polls Postgres for due monitors, runs them with bounded concurrency, per-check timeouts and jitter.
- **Incidents:** open after N consecutive failures and resolve after M consecutive successes, so a single blip does not page anyone.
- **Alerts:** Slack incoming webhook and SMTP email, sent through a transactional outbox with retries, exponential backoff and deduplication.
- **Status page:** server-rendered HTML with current status, uptime over 24h/7d/30d, p50/p95 latency, 90-day uptime bars and recent incidents. The same data is available as JSON.
- **Operations:** `/metrics` for Prometheus, `/healthz` and `/readyz`, structured JSON logs, graceful shutdown.
- **Multi-replica safe:** check claims use `FOR UPDATE SKIP LOCKED`, and singleton jobs use Postgres advisory locks.

## Architecture

```mermaid
flowchart LR
    subgraph replica["Each replica (identical binary)"]
        sched["Scheduler"]
        checkers["HTTP / TCP checkers"]
        record["RecordResult transaction<br/>result + rollup + incident + outbox"]
        disp["Alert dispatcher"]
        ret["Retention job"]
        http["HTTP server<br/>status page, JSON API, /metrics"]
    end

    pg[("Postgres")]
    targets["Monitored targets"]
    slack["Slack webhook"]
    smtp["SMTP server"]
    users["Visitors and operators"]
    prom["Prometheus"]

    sched -- "claim due monitors<br/>FOR UPDATE SKIP LOCKED" --> pg
    sched --> checkers --> targets
    checkers --> record --> pg
    disp -- "lease outbox rows<br/>FOR UPDATE SKIP LOCKED" --> pg
    disp --> slack
    disp --> smtp
    ret -- "pg_try_advisory_lock" --> pg
    users --> http
    prom --> http
    http --> pg
```

Lifecycle of one check:

1. A replica's scheduler asks Postgres for up to *free slots* due monitors. The same statement moves their `next_run_at` forward, which is the claim.
2. The check runs with a context deadline equal to the monitor's timeout.
3. One transaction inserts the raw result, bumps the daily rollup, locks the monitor's state row, applies the incident state machine and, on a transition, inserts the incident change plus one outbox row per alert channel.
4. A dispatcher on any replica leases due outbox rows, sends them, and records success or schedules a retry.

Incident state machine (pure function in `internal/incident`, thresholds per monitor):

```mermaid
stateDiagram-v2
    [*] --> unknown
    unknown --> up: 1 success
    unknown --> down: N consecutive failures (incident opened)
    up --> down: N consecutive failures (incident opened)
    down --> up: M consecutive successes (incident resolved)
```

### Code layout

| Path | Responsibility |
| --- | --- |
| `cmd/uptime-monitor` | Wiring, signal handling, `healthcheck` subcommand |
| `internal/monitor` | Monitor spec, defaults, validation, YAML file loading |
| `internal/checker` | HTTP and TCP checkers |
| `internal/scheduler` | Poll, claim, bounded concurrency, timeouts |
| `internal/incident` | Flap-resistant state machine (no I/O) |
| `internal/stats` | Uptime percentage, nearest-rank percentile, daily bars |
| `internal/notify` | `Notifier` interface, Slack, SMTP, outbox dispatcher |
| `internal/store` | Postgres queries, embedded migrations, retention |
| `internal/status` | Builds and caches the public status snapshot |
| `internal/api` | HTTP routes, auth, status page template |
| `internal/metrics` | Prometheus registry and database-backed gauges |
| `internal/clock` | Clock interface with a fake for tests |
| `deploy/` | Production compose file, Caddyfile, Terraform |

## Quickstart

### Docker Compose (local demo)

```sh
docker compose up --build
```

- Status page: http://localhost:8080
- Mailpit inbox for alert emails: http://localhost:8025
- Monitors come from `examples/monitors.yml` (two demo HTTP services plus Postgres itself).

To see an incident open and resolve:

```sh
docker compose stop whoami    # about 30s later: incident opens, email arrives
docker compose start whoami   # about 30s later: incident resolves
```

To run two replicas that share the work:

```sh
docker compose --profile replicas up --build   # second replica on :8081
```

### Without Docker

```sh
createdb uptime
export DATABASE_URL='postgres://localhost:5432/uptime?sslmode=disable'
export ADMIN_TOKEN=dev-token
go run ./cmd/uptime-monitor
```

Migrations run automatically at startup.

### Managing monitors

```sh
TOKEN=dev-admin-token   # the compose default

curl -s -X POST localhost:8080/api/v1/monitors \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"homepage","type":"http","target":"https://example.com","interval":"1m","body_contains":"Example Domain"}'

curl -s localhost:8080/api/v1/monitors -H "Authorization: Bearer $TOKEN"
curl -s -X PUT localhost:8080/api/v1/monitors/1 -H "Authorization: Bearer $TOKEN" -d '{"name":"homepage","type":"tcp","target":"example.com:443"}'
curl -s -X DELETE localhost:8080/api/v1/monitors/1 -H "Authorization: Bearer $TOKEN"
```

Validation errors come back together as a `422` with a `details` list. Monitors defined in the monitors file are read-only through the API (`409`).

### Endpoints

| Method and path | Auth | Description |
| --- | --- | --- |
| `GET /` | public | Status page |
| `GET /api/v1/status` | public | Status page data as JSON |
| `GET /metrics` | public on the app port; blocked by Caddy in the VM deployment | Prometheus metrics |
| `GET /healthz`, `GET /readyz` | public | Liveness, and readiness (database ping) |
| `GET, POST /api/v1/monitors` | bearer | List, create |
| `GET, PUT, DELETE /api/v1/monitors/{id}` | bearer | Read, replace, delete |

## Configuration

All configuration comes from environment variables. Secrets are never read from files in the repo.

| Variable | Default | Meaning |
| --- | --- | --- |
| `DATABASE_URL` | required | Postgres connection string |
| `LISTEN_ADDR` | `:8080` | HTTP listen address |
| `ADMIN_TOKEN` | empty | Bearer token for the management API. If empty, the API is disabled (403), not open. |
| `MONITORS_FILE` | empty | YAML file of monitors synced at startup |
| `PUBLIC_URL` | `http://localhost:8080` | Base URL used in alert messages |
| `STATUS_PAGE_TITLE` | `Service Status` | Status page heading |
| `CHECK_CONCURRENCY` | `32` | Max checks in flight per replica |
| `POLL_INTERVAL` | `1s` | How often each replica looks for due checks |
| `CHECK_JITTER` | `0.1` | Up to this fraction of the interval is added at random to each run |
| `RESULT_RETENTION` | `840h` (35 days) | Raw results older than this are deleted hourly. Minimum 31 days. |
| `SLACK_WEBHOOK_URL` | empty | Enables Slack alerts |
| `SMTP_HOST`, `SMTP_PORT` | empty, `587` | Enables email alerts. STARTTLS is used when offered. |
| `SMTP_USERNAME`, `SMTP_PASSWORD` | empty | SMTP auth (only sent over TLS or to localhost) |
| `SMTP_FROM`, `SMTP_TO` | empty | Sender, and comma-separated recipients |

Monitor fields (JSON API and YAML file use the same names):

| Field | Default | Notes |
| --- | --- | --- |
| `name` | required | Unique, 1-100 characters |
| `type` | required | `http` or `tcp` |
| `target` | required | URL for `http`, `host:port` for `tcp` |
| `method` | `GET` | `GET`, `HEAD`, `POST`, `PUT`, `OPTIONS` |
| `expected_status` | `200` | Status of the final response after up to 5 redirects |
| `body_contains` | empty | Substring searched in the first 1 MiB of the body |
| `interval` | `1m` | `5s` to `24h` |
| `timeout` | `min(10s, interval/2)` | Must be shorter than the interval |
| `failure_threshold` | `3` | Consecutive failures that open an incident |
| `recovery_threshold` | `2` | Consecutive successes that resolve it |
| `enabled` | `true` | Disabled monitors are not checked or shown |

## Design decisions and trade-offs

### Flap detection: consecutive thresholds (hysteresis)

An incident opens after `failure_threshold` consecutive failures and resolves after `recovery_threshold` consecutive successes. A single dropped packet does not page anyone, and a service that alternates between up and down never reaches either threshold, so it does not cause an alert storm. The cost is detection delay: with a 60s interval and a threshold of 3, an outage is reported after about 2 to 3 minutes. Thresholds are per monitor, so a critical endpoint can use a short interval with threshold 2.

The incident's start time is the time of the *first* failure in the streak and its end is the *first* success, so the thresholds delay the alert but do not distort the recorded outage duration.

I considered alternatives. A failure-percentage window ("40% of the last 10 checks") catches a service that is failing often but intermittently, which pure consecutive counts miss, but it is harder to explain and to test. Exponential smoothing has the same problem. The state machine is a pure function (`incident.Next`) with table-driven tests, and the store applies it while holding a row lock (`SELECT ... FOR UPDATE` on `monitor_state`), so concurrent results for one monitor cannot open two incidents. A partial unique index (`one open incident per monitor`) enforces the same rule in the schema.

### Running several replicas: SKIP LOCKED claims vs an advisory-lock leader

There are two common designs:

- **Leader election with an advisory lock.** One replica holds `pg_advisory_lock` and runs every check, and the others wait to take over. It is simple to reason about, but one replica does all the work, and failover depends on Postgres noticing the leader's session is gone (seconds to minutes with TCP keepalives). Session-level locks also do not work through a transaction-pooling PgBouncer.
- **Row claims with `FOR UPDATE SKIP LOCKED`** (what this project does). Every replica polls for due monitors, and one statement both selects and claims them (simplified from `internal/store/schedule.go`):

  ```sql
  WITH due AS (
    SELECT id FROM monitors WHERE enabled AND next_run_at <= now()
    ORDER BY next_run_at LIMIT $1 FOR UPDATE SKIP LOCKED)
  UPDATE monitors m SET next_run_at = now() + interval * (1 + random() * jitter)
  FROM due WHERE m.id = due.id RETURNING ...
  ```

  Rows another replica is claiming are skipped instead of waited on, so replicas never block each other and the work spreads naturally. There is no leader, so nothing has to fail over. Time comes from the database's `now()`, so clock skew between replicas does not matter.

The first version used a plain `SELECT` followed by an `UPDATE`. The concurrent-claim integration test (`TestClaimDueAcrossReplicas`) showed the same monitor being claimed two or three times by different replicas, and the single-statement claim fixed it. That history is in the commit log.

Each replica claims only as many monitors as it has free slots. If it claimed more, a busy replica would hold work that an idle one could have run on time.

**Delivery semantics:** checks are *at most once* per interval. If a replica dies after claiming a monitor, that run is skipped and the next interval picks the monitor up. For a monitor that is the right trade: a missing sample is harmless, while a duplicated one skews uptime. Alerts are the opposite (see below).

Advisory locks are still the right tool for jobs that should have exactly one runner and no natural unit of work to share:

- migrations at startup (`pg_advisory_lock`, so replicas wait their turn),
- syncing the monitors file (`pg_advisory_xact_lock`),
- hourly retention (`pg_try_advisory_lock`: whoever gets it does the work, the others skip).

### Alerts: transactional outbox, at least once, deduplicated

The incident transition and the alert rows for each channel are written in the **same transaction**. If the process crashes after the incident opens, the alert is still in the table and some replica will send it. A dispatcher leases due rows with `SKIP LOCKED`, sends them, and marks them sent, or schedules a retry with exponential backoff (30s doubling, capped at 30m, 8 attempts). HTTP 4xx from Slack (except 429) and SMTP 5xx are treated as permanent and not retried.

Deduplication: `UNIQUE (incident_id, event, channel)` plus `ON CONFLICT DO NOTHING` means one message per incident event per channel, however many times the transition is recorded. The lease (2x the send timeout) keeps two replicas from sending the same row at the same time. The remaining duplicate window is a crash between "sent" and "marked sent", which makes delivery **at least once**. Neither Slack webhooks nor SMTP offer idempotency keys, so exactly once is not achievable without the receiver's help.

`Notifier` is a one-method interface. The dispatcher tests use a scripted fake to cover retry schedules, giving up, and permanent errors. Slack is tested against `httptest`, and email against a minimal fake SMTP server.

### Storage growth, retention and rollups

Every check writes one row to `check_results`. At a 60s interval that is 1,440 rows per monitor per day, about 50k per monitor over the 35-day default retention. At roughly 100-150 bytes per row including indexes, 100 monitors need on the order of 0.5-1 GB. These are estimates, not measurements.

- **Rollup:** `daily_stats (monitor_id, day, total, ok)` is upserted in the same transaction as each result. The 90-day bars read only the rollup, and it survives raw-data retention, so history costs a few bytes per monitor per day.
- **Retention:** an hourly job deletes raw rows older than `RESULT_RETENTION` in batches of 10k, so no single transaction holds locks on, or writes WAL for, millions of rows.
- **Indexes:** `(monitor_id, checked_at DESC)` serves the per-monitor window queries. A BRIN index on `checked_at` serves the retention scan at a tiny fraction of a B-tree's size, because rows arrive in time order.
- **Status page cost:** 24h/7d/30d uptime and p50/p95 come from one `LATERAL` aggregate per monitor (with `percentile_disc`, the same nearest-rank definition as `stats.Percentile`, which an integration test checks). Each replica caches the snapshot for 15 seconds.

### What changes at 10k monitors

At 10k monitors and a 60s interval, the system does about 170 checks per second and 245k result rows per hour. The design holds up, but these parts change:

- **Writes:** one transaction per result becomes the bottleneck. Batch results (multi-row `INSERT` or `COPY`) every second or so, and update state and rollups in bulk.
- **Retention:** partition `check_results` by day (native partitioning or TimescaleDB) and drop whole partitions instead of running `DELETE`.
- **Rollups:** add hourly rollups, and read 7d and 30d uptime from them instead of scanning raw rows.
- **Status page:** a single page with 10k monitors is not useful. Add groups or components, and precompute snapshots in a background job instead of computing them on request.
- **Metrics:** per-monitor histogram labels mean 10k monitors times 12 buckets, too many series. Keep per-monitor `up` and drop per-monitor latency histograms, or aggregate them.
- **Checking capacity:** more replicas and higher `CHECK_CONCURRENCY`. Watch file descriptors and ephemeral ports, because every check opens a fresh connection on purpose.
- **False positives:** with one vantage point, a network problem at the monitor looks like an outage everywhere. Run checkers in several regions and open an incident only when a quorum agrees.
- **Claim query:** `SKIP LOCKED` with the partial index on `next_run_at` stays cheap. Polling once per second per replica is fine. At much larger scale, replicas could own hash ranges of monitor IDs instead.

### Smaller choices

- **database/sql with pgx, hand-written SQL.** It is a standard interface most Go developers know, and `sql.Conn` gives the dedicated connection that session-level advisory locks need. pgx's native pool would add batch and `COPY` support, which is the first thing I would reach for at 10k monitors. With about 20 queries, sqlc would add a code generation step for little gain, and several queries rely on Postgres-specific features that read better written out.
- **Standard library router.** Go 1.22's `ServeMux` supports method and path-parameter patterns, which is everything this API needs, so there is no router dependency.
- **Embedded SQL migrations** with a small runner (`internal/store/migrate.go`) instead of goose or golang-migrate. The binary carries its schema, there is no separate migration step, and the advisory lock makes concurrent starts safe. The trade-off is no down-migrations, which I would not run against production data anyway.
- **Fresh connection per HTTP check** (keep-alives off). A pooled connection would skip DNS, TCP and TLS setup and hide exactly the failures a monitor should catch.
- **Shutdown finishes in-flight checks.** Cancelling them would record false failures. Each check is already bounded by its timeout, so shutdown is too.
- **Public output is minimal.** The status page and JSON show monitor names and status only, not targets or error strings, which can reveal internal hostnames.
- **Metrics from two sources.** Counters (checks, alert attempts) are per replica and summed by Prometheus. Monitor up/down and backlog gauges are read from Postgres at scrape time, so every replica reports the same value.
- **Security notes.** Only admins can create monitors, and a monitor is effectively a request from inside your network (an SSRF surface), so the admin token protects the whole management API and comparisons are constant-time. With no token configured, the API is disabled rather than open.

## Testing

```sh
go test ./...                     # unit tests; integration tests skip

# Integration tests need a Postgres superuser URL. Each test creates and
# drops its own database.
export TEST_DATABASE_URL='postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable'
go test -race ./...

go vet ./...
golangci-lint run ./...           # config in .golangci.yml
```

What is covered:

- **Incident state machine:** table-driven, including flapping, reopen after resolve, threshold of 1, and streak timestamps.
- **Uptime and latency math:** percentages, nearest-rank percentiles, and 90-day bars including UTC day boundaries and missing days.
- **Scheduler:** fake clock, store and checker. Covers once-per-interval runs, the concurrency bound, claims limited to free slots, the per-check deadline, and shutdown waiting for in-flight checks.
- **Checkers:** `httptest` servers for status, method, body match, redirects, redirect loops and timeouts, plus real TCP listeners.
- **Alerts:** retry and backoff schedule, max attempts, permanent errors, Slack status handling, SMTP against a fake server, and header-injection safety.
- **Postgres integration:**
  - concurrent migrations from 4 replicas
  - incident open and resolve with rollups crossing midnight
  - 40 concurrent failures from 2 pools opening exactly 1 incident
  - outbox dedup, and concurrent dispatchers claiming each delivery once
  - SQL percentiles matching the Go reference
  - retention skipping while another replica holds the lock
  - **4 replicas racing `ClaimDue`**, and **3 real schedulers sharing one database**, each checking every monitor exactly once
- **API:** CRUD, validation, auth, read-only file monitors, status page rendering and JSON.

## Deploying to a VM (Hetzner Cloud)

`deploy/terraform` creates one small server (default `cx22`) with a firewall that allows SSH only from CIDRs you choose, plus 80/443 and ping. cloud-init installs Docker, writes the config, **generates the database password and admin token on the server** (so they never enter Terraform state), clones this repo and starts `deploy/compose.prod.yml`: the app, Postgres 16, and Caddy, which gets a TLS certificate automatically when `domain` is set and hides `/metrics` from the internet.

```sh
cd deploy/terraform
cp terraform.tfvars.example terraform.tfvars   # edit
export TF_VAR_hcloud_token=...
terraform init
terraform plan
terraform apply
terraform output admin_token_hint
```

To deploy a new version, run `ssh root@<ip> /opt/uptime/deploy.sh` (it is idempotent). `user_data` changes are ignored on purpose, because changing it would replace the server and its database. Optional Slack and SMTP secrets passed as variables do end up in Terraform state, so use an encrypted remote backend if you set them.

`.terraform.lock.hcl` is not committed. Terraform and OpenTofu resolve the provider from different registries, so generate the lock file with whichever binary you use.

## Known limitations

- Postgres runs on the same VM with a Docker volume. There are no automated database backups beyond Hetzner's optional server backups (`enable_backups`). Use a managed database or `pg_dump` to object storage for anything important.
- There is a single vantage point, so a network problem near the monitor shows up as outages everywhere.
- There is no UI for managing monitors (API and YAML file only), and no maintenance windows or per-monitor alert routing.
- Checks are at most once per interval, so a replica crash can skip one sample per claimed monitor.
- Alerts are at least once, so a crash at exactly the wrong moment can send a duplicate.
- HTTP checks do not report TLS certificate expiry.

## What has been verified

- `go vet`, golangci-lint and the full test suite pass with `-race`, including the integration tests against Postgres 16.
- The binary was run end to end against Postgres: monitors file sync, checks, an incident opening, the status page, the JSON API and `/metrics`.
- `tofu fmt -check` and `tofu validate` (OpenTofu) pass for `deploy/terraform`. It has never been applied.
- Not yet run: `docker build`, `docker compose up`, and real Slack or SMTP delivery (tested against fakes only).
