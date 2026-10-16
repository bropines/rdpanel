# Monitoring

How to scrape RD Panel with Prometheus, which metrics and alerts exist, and the ready-made
Grafana dashboard. For operators running Prometheus/Grafana on a docker host or with
kube-prometheus-stack.

- [Quick start](#quick-start) · [Access to /metrics](#access-to-metrics)
- [Metrics](#metrics) · [Alerts](#alerts) · [Dashboard](#dashboard)
- [Prometheus + Grafana with docker compose](#prometheus--grafana-with-docker-compose) ·
  [Kubernetes (Helm)](#kubernetes-helm)

## Quick start

- **Docker host:** run the bundled stack —
  [Prometheus + Grafana with docker compose](#prometheus--grafana-with-docker-compose).
- **Kubernetes:** enable `serviceMonitor`, `prometheusRule` and `grafanaDashboard` in the
  Helm values — [Kubernetes (Helm)](#kubernetes-helm).
- **Existing Prometheus:** allow its address in `RDPANEL_METRICS_ALLOW` and scrape
  `GET /metrics` on the panel listener (`RDPANEL_LISTEN`); import the dashboard and the
  alert rules below.

Metrics are served on `GET /metrics`, on the same listener as the panel (`RDPANEL_LISTEN`).
What the repository ships:

| file | what |
|---|---|
| [`deploy/monitoring/grafana/rustdesk-panel.json`](../deploy/monitoring/grafana/rustdesk-panel.json) | Grafana dashboard (data source is a template variable; import it or provision it) |
| [`deploy/monitoring/prometheus/alerts.yaml`](../deploy/monitoring/prometheus/alerts.yaml) | Prometheus alert rules (+ `alerts_test.yaml` for `promtool test rules`) |
| [`deploy/monitoring/compose.yml`](../deploy/monitoring/compose.yml) | Prometheus + Grafana for a single docker host, provisioned with both |
| Helm `prometheusRule.*`, `grafanaDashboard.*` | the same rules and dashboard for kube-prometheus-stack |

## Access to /metrics

`/metrics` answers only when the client IP, and every address in forwarding
headers, is in `RDPANEL_METRICS_ALLOW` (default loopback only). Everything else
gets 403.

| where Prometheus runs | what to do |
|---|---|
| same host, host networking (the compose files in this repo) | scrape `127.0.0.1:21114` from a host-network Prometheus; nothing to change |
| elsewhere | add its address, e.g. `RDPANEL_METRICS_ALLOW=127.0.0.0/8,::1/128,10.8.0.5/32`, and scrape the panel port directly |
| Kubernetes | add the Prometheus pod CIDR to `panel.metricsAllow` |

Notes:

- Scrape the panel port directly: a reverse proxy in front adds `X-Forwarded-For` and is
  refused on purpose.
- Keep `location = /metrics { return 404; }` on public vhosts.

## Metrics

All series are prefixed `rdpanel_`. Labels come from small fixed sets: no user
names, user ids, IPs or device ids, except the opt-in per-device gauge.

| metric | type | labels | meaning |
|---|---|---|---|
| `build_info` | gauge (1) | `version`, `commit`, `go_version`, `hbb_ref` | build metadata; `hbb_ref` is the hbbs/hbbr source ref (`RDPANEL_HBB_REF`, set by the image) |
| `http_requests_total` | counter | `method`, `route`, `code` | requests by chi route pattern (never the raw path) |
| `http_request_duration_seconds` | histogram | `method`, `route` | latency (`/api/v1/events` is SSE: its "latency" is the stream length) |
| `hbbs_up` | gauge | | 1 if the last presence poll (OnlineRequest to the hbbs rendezvous port) worked |
| `hbbs_rtt_seconds` | gauge | | RTT of the last successful poll |
| `hbbs_poll_duration_seconds` | histogram | | full poll cycle |
| `hbbs_poll_errors_total` | counter | | failed poll cycles |
| `presence_online_devices` / `presence_known_devices` | gauge | | devices online / tracked |
| `presence_device_online` | gauge | `id` | **opt-in** (`RDPANEL_METRICS_PER_DEVICE=true`): 1/0 per device |
| `hbb_admin_up` | gauge | `target` (`hbbs`, `hbbr`) | the loopback admin channel answered the last probe. hbbr's admin channel shares the relay port, so `target="hbbr"` is effectively "hbbr is up" |
| `hbb_admin_reconcile_total` | counter | `target`, `result` (`ok`, `error`) | full re-applications of the desired server settings (start, restart detection, settings change) |
| `hbb_admin_drift_corrections_total` | counter | `target`, `key` | settings corrected after a read-back showed drift |
| `hbbr_relay_sessions` | gauge | | active relays reported by hbbr `usage` |
| `hbbr_relay_bandwidth_bits_per_second` | gauge | | sum of the relays' current speed |
| `hbbr_relay_active_bytes` | gauge | | bytes moved so far by the relays that are still open |
| `hbbr_usage_up` / `hbbr_usage_errors_total` | gauge / counter | | `usage` poll health |
| `auth_login_attempts_total` | counter | `surface` (`panel`, `client`), `method` (`password`, `totp`, `email`, `oidc`), `result` (`success`, `failure`, `rate_limited`) | login attempts. `method` is the step that decided the attempt: a wrong TOTP code is `totp/failure`, a login finished with a TOTP code is `totp/success` (e-mailed codes likewise count as `email`); the password step that only leads to the TOTP prompt is not counted |
| `auth_client_tokens_issued_total` | counter | `method` | RustDesk client tokens minted (`/api/login`, OIDC auth-query) |
| `auth_api_token_requests_total` | counter | `result` (`success`, `failure`, `rate_limited`) | admin API / SSE requests presenting a personal API token ([`api.md`](api.md)); `failure` = unknown, expired or revoked token or disabled owner |
| `audit_events_total` | counter | `type` (`conn`, `file`, `alarm`), `result` (`stored`, `rejected`, `error`) | client audit records (`/api/audit/*`); `rejected` = unknown device / uuid mismatch / bad nonce |
| `events_published_total`, `events_dropped_total`, `events_subscribers` | | `type` | in-process event bus behind the SSE stream |
| `logs_lines_total` / `logs_dropped_total` | counter | `source` (`hbbs`, `hbbr`, `panel`, `other`) | lines captured for Server → Logs / lines a too-slow live log viewer skipped ([deploy.md → Server logs](deploy.md#server-logs)) |

Plus the standard `go_*` and `process_*` collectors.

### hbbr relay statistics

- Source: hbbr's admin command `usage`, polled every `RDPANEL_HBBS_RECONCILE_INTERVAL`
  (15 s) over the loopback admin channel; skipped while the reconciler sees hbbr's admin
  channel down.
- Only aggregates are exported; the per-relay `ip:port` lines are never used as labels.

hbbr's numbers are coarse:

- a relay shows up only after its first second;
- "current" speed is refreshed only while packets flow, so an idle relay keeps
  its last value;
- a relay's line disappears when it closes, so there is no exact running total
  of relayed traffic (`relay_active_bytes` drops when sessions end).

> The whole hbbs/hbbr integration (presence, admin channel, relay stats) needs
> `RDPANEL_HBBS_ENABLED=true` (default) and loopback addresses (same container or
> shared network namespace, see [hbbs-integration.md](research/hbbs-integration.md)).

### Cardinality

Without the per-device gauge a panel exports a few hundred series, mostly
`http_*` (method x route x code). `RDPANEL_METRICS_PER_DEVICE=true` adds one
series per device; leave it off for large fleets and use the panel's device list
instead.

## Alerts

Rules: [`alerts.yaml`](../deploy/monitoring/prometheus/alerts.yaml) — group
`rustdesk-panel`; all selectors use `job="rustdesk-panel"`.

> **Why the old name?** The job, group and `RustDeskPanel*` alert names, and the dashboard
> uid below, predate the rename to RD Panel. They are kept so existing scrape configs,
> Alertmanager routes, silences and dashboard links keep working.


| alert | severity | fires when |
|---|---|---|
| `RustDeskPanelDown` | critical | the scrape fails for 2 min |
| `RustDeskPanelAbsent` | warning | no `rustdesk-panel` target for 10 min |
| `RustDeskPanelHTTP5xxRatio` | warning | > 5 % of requests are 5xx for 10 min (with some traffic) |
| `RustDeskHBBSDown` | critical | presence poll of hbbs fails for 2 min |
| `RustDeskHBBSAdminChannelDown` | warning | hbbs answers clients but not its admin channel for 5 min |
| `RustDeskHBBRDown` | critical | hbbr admin channel (= relay port) down for 2 min |
| `RustDeskReconcilerFailing` | warning | settings re-application errors in 15 min, for 15 min |
| `RustDeskReconcilerDriftLoop` | warning | one setting corrected > 10 times in 30 min |
| `RustDeskOnlineDevicesDrop` | warning | online devices < 50 % of 15 min ago (from at least 5) for 5 min |
| `RustDeskLoginFailureSpike` | warning | > 10 failed or rate-limited logins/min per surface for 5 min |
| `RustDeskAuditStoreErrors` | warning | audit records fail to store (5xx to clients) |

Thresholds are starting points. Check changes with:

```sh
cd deploy/monitoring/prometheus
docker run --rm -v "$PWD":/w -w /w --entrypoint promtool prom/prometheus:v3.5.0 check rules alerts.yaml
docker run --rm -v "$PWD":/w -w /w --entrypoint promtool prom/prometheus:v3.5.0 test rules alerts_test.yaml
```

## Dashboard

[`rustdesk-panel.json`](../deploy/monitoring/grafana/rustdesk-panel.json)
(uid `rustdesk-panel`) has variables `datasource`, `job` and `instance` and these rows:

- **Overview**: panel / hbbs / hbbs admin / hbbr up, online and known devices,
  relay sessions, hbbs RTT, build info table (version, commit, hbbs/hbbr ref);
- **Presence**: online vs known devices, hbbs probe latency and poll errors;
- **Logins and audit**: attempts by surface/method/result, failures, client
  tokens issued, client audit records;
- **HTTP**: request rate, p95 latency (SSE excluded) and 5xx by route;
- **Relay (hbbr)**: sessions, bandwidth, bytes of active relays;
- **Server settings reconciler**: admin channels, re-applications, drift corrections;
- **Process**: memory, goroutines, SSE subscribers, event bus.

To install it, either:

- import it in Grafana (Dashboards > New > Import > upload JSON) and pick the Prometheus
  data source, or
- provision it from a file, as the compose example does.

## Prometheus + Grafana with docker compose

For the single-host deployment from [deploy.md](deploy.md) (panel on host
networking, listening on `127.0.0.1:21114`):

```sh
cd deploy/monitoring
echo 'GRAFANA_ADMIN_PASSWORD=<something long>' > .env && chmod 600 .env
docker compose up -d
ssh -L 3000:127.0.0.1:3000 <host>   # then http://127.0.0.1:3000, user admin
```

| service | address | what it gets |
|---|---|---|
| Prometheus | `127.0.0.1:9090` | 30 days retention, loads `alerts.yaml` |
| Grafana | `127.0.0.1:3000` | the Prometheus data source and the dashboard in the `RustDesk` folder |

- Both listen on loopback only.
- If the panel listens on another port (for example `RDPANEL_LISTEN=127.0.0.1:31114`),
  edit the target in `prometheus/prometheus.yml`.
- There is no Alertmanager in the example: add an `alerting:` block to `prometheus.yml`
  to route alerts.

## Kubernetes (Helm)

With kube-prometheus-stack:

```yaml
panel:
  metricsAllow: [127.0.0.0/8, 10.244.0.0/16]   # + the Prometheus pod CIDR
serviceMonitor:
  enabled: true
  labels: { release: kube-prometheus-stack }
prometheusRule:
  enabled: true
  labels: { release: kube-prometheus-stack }    # match the ruleSelector
  # disabledAlerts: [RustDeskLoginFailureSpike]
  # additionalRuleLabels: { team: infra }
grafanaDashboard:
  enabled: true                                 # ConfigMap labelled grafana_dashboard: "1"
  # folder: RustDesk                            # grafana_folder annotation
```

- The PrometheusRule carries the rules of `alerts.yaml`. The chart keeps a copy in
  `files/alerts.yaml`; a Go test keeps both copies and the metric names in sync.
- `job="rustdesk-panel"` is replaced by the panel Service name, which is the job label the
  chart's ServiceMonitor produces. If you scrape the panel differently, set
  `prometheusRule.job`.
