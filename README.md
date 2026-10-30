# postfix_exporter

Prometheus exporter for a Postfix mail server. A single static binary provides:

- **Mail queue** – number, recipients, size and age of the messages per queue, live via the `showq` service
- **Delivery** – status (sent, deferred, bounced, …) per delivery service and DSN code, delays as histograms
  including the four stages of `delays=a/b/c/d`, for smtp, lmtp, local, virtual, pipe and error
- **Inbound** – connections, accepted messages, rejects with an automatic reason class, aborted sessions,
  failed SMTP commands, SASL failures, TLS, anvil limits, reverse DNS
- **postscreen**, **milters**, **header/body_checks**, **bounce**, **pickup**
- **Operations** – log messages by service and severity, crashed and throttled processes
- optional: **classification of reply texts** using regular expressions

The log data comes from a syslog file (including Postfix's own `maillog_file`) or from the systemd journal.
A Grafana dashboard and alerting rules are in [`contrib/`](contrib/).

The exporter combines the features of
[Hsn723/postfix_exporter](https://github.com/Hsn723/postfix_exporter) (showq, many log metrics) and
[sergeymakinen/postfix_exporter](https://github.com/sergeymakinen/postfix_exporter) (robust line parser,
reply classification, milters, severities, replay mode); see [NOTICE](NOTICE). Docker and Kubernetes are
deliberately not supported, and exactly one Postfix instance is monitored.

## Building

```sh
make            # = make build, ./bin/postfix_exporter (static, CGO_ENABLED=0)
make test       # tests with the race detector
make ci         # fmt, vet, lint, vuln, test, build
make dist       # release archives for linux/amd64 and linux/arm64
```

## Permissions

| Access                               | Needed for                                      | Solution                                                     |
|--------------------------------------|-------------------------------------------------|--------------------------------------------------------------|
| `/var/spool/postfix/public/showq`    | queue metrics                                   | group `postdrop` (`public/` is `0710 postfix:postdrop`)      |
| systemd journal                      | `log.source: journal`                           | group `systemd-journal`                                      |
| `/var/log/mail.log`                  | `log.source: file`                              | group `adm` (Debian/Ubuntu) or `log` (Arch)                  |

The systemd unit in [`contrib/postfix_exporter.service`](contrib/postfix_exporter.service) sets the groups
via `SupplementaryGroups=` and is heavily hardened.

## Configuration

Without `--config.file` the defaults apply: log from `/var/log/mail.log`, queue via
`unix:///var/spool/postfix/public/showq`. All options are explained in
[`config.example.yml`](config.example.yml). The most common adjustments:

```yaml
log:
  source: journal
  journal:
    units: [postfix.service]      # Debian/Ubuntu: postfix@-.service

services:
  smtp: [encrypt]                 # master.cf services whose name does not end in the daemon type
```

The exporter recognises services such as `submission/smtpd` or `relay/smtp` (via `syslog_name`) by itself.
They appear separately in the `service` label, so that, for example, SASL failures on port 587 can be told
apart from those on port 25.

### Bounded label values

Local users can forge log lines (`logger -t postfix/x`), and remote clients and servers choose parts of
them. So that this cannot create an unbounded number of time series, the exporter counts values beyond a
limit as `other` and logs a warning once:

| Label                              | Limit                                  |
|------------------------------------|----------------------------------------|
| `service`                          | `max_services` (default 100)           |
| `text` of the reply metrics        | `replies.max_texts` (default 100)      |
| `dsn`, `code`, `enhanced_code`     | 200 distinct codes in total            |
| `method` of SASL failures          | registered SASL mechanisms only        |

With `log.source: journal` and `units`, the exporter only reads lines of the Postfix unit; forged tags from
other users never reach it.

```sh
postfix_exporter --config.file=/etc/postfix_exporter/config.yml --config.check
```

### Flags

| Flag                   | Default    | Meaning                                                                         |
|------------------------|------------|---------------------------------------------------------------------------------|
| `--config.file`        | –          | configuration file                                                              |
| `--config.check`       | `false`    | validate the configuration and exit                                             |
| `--web.listen-address` | `:9154`    | HTTP address                                                                    |
| `--web.telemetry-path` | `/metrics` | path of the metrics                                                             |
| `--web.config.file`    | –          | TLS and basic auth ([format](https://github.com/prometheus/exporter-toolkit/blob/master/docs/web-configuration.md)) |
| `--web.max-requests`   | `3`        | parallel scrapes, 0 = unlimited                                                 |
| `--log.level`          | `info`     | `debug` logs every Postfix line the exporter does not recognise                 |
| `--replay`             | `false`    | read the existing log, print the metrics and exit                               |
| `--replay.since`       | `24h`      | time span for `--replay` with the journal                                       |

### Testing the reply classification

Rules under `replies:` can be developed against the real log without touching the running exporter:

```sh
postfix_exporter --config.file=./config.yml --replay | grep -E '_replies_total|_rejects_total'
```

## Metrics

All log metrics are counters since the exporter started. It reads the log from the time it starts, like
`tail -F`, including across log rotation.

### Queue (live via showq)

| Metric                                       | Labels  | Meaning                                      |
|----------------------------------------------|---------|----------------------------------------------|
| `postfix_showq_up`                           |         | showq reachable (1) or not (0)               |
| `postfix_showq_scrape_duration_seconds`      |         | duration of the queue read                   |
| `postfix_queue_messages`                     | `queue` | messages (maildrop, incoming, active, deferred, hold) |
| `postfix_queue_recipients`                   | `queue` | recipients                                   |
| `postfix_queue_size_bytes`                   | `queue` | total size                                   |
| `postfix_queue_oldest_message_age_seconds`   | `queue` | age of the oldest message                    |
| `postfix_queue_message_age_seconds`          | `queue` | histogram: age                               |
| `postfix_queue_message_size_bytes`           | `queue` | histogram: size                              |

### Delivery

| Metric                                    | Labels                                         | Meaning                                        |
|-------------------------------------------|------------------------------------------------|------------------------------------------------|
| `postfix_delivery_status_total`           | `service`, `status`, `dsn`                     | delivery attempts per recipient                |
| `postfix_delivery_delay_seconds`          | `service`, `status`                            | histogram: arrival to delivery attempt         |
| `postfix_delivery_stage_delay_seconds`    | `service`, `stage`                             | histogram of the stages of successful deliveries |
| `postfix_delivery_replies_total`          | `service`, `status`, `code`, `enhanced_code`, `text` | classified replies (`replies.delivery`) |
| `postfix_smtp_connection_failures_total`  | `service`, `reason`                            | timeout, refused, no_route, unreachable, other |
| `postfix_smtp_tls_connections_total`      | `service`, `trust`, `protocol`, `cipher`       | outbound TLS connections                       |
| `postfix_smtp_replies_total`              | `service`, `code`, `enhanced_code`, `text`     | replies without a status, e.g. greylisting (`replies.smtp`) |
| `postfix_qmgr_message_size_bytes`         |                                                | histogram: size on entering the active queue   |
| `postfix_qmgr_message_recipients`         |                                                | histogram: recipients per message              |
| `postfix_qmgr_messages_removed_total`     |                                                | messages removed from the queue                |
| `postfix_qmgr_messages_expired_total`     | `type`                                         | expired, force-expired                         |
| `postfix_bounce_notifications_total`      | `recipient`, `type`                            | non_delivery, delivery_status, delay           |

`service` is the service name from master.cf, e.g. `smtp`, `relay/smtp`, `lmtp`, `local`. Deferrals decided
by the queue manager itself ("delivery temporarily suspended") appear with `service="qmgr"`.

### Inbound

| Metric                                       | Labels                                      | Meaning                                          |
|----------------------------------------------|---------------------------------------------|--------------------------------------------------|
| `postfix_smtpd_connections_total`            | `service`                                   | connections                                      |
| `postfix_smtpd_disconnections_total`         | `service`                                   | regular disconnections                           |
| `postfix_smtpd_commands_total`               | `service`, `command`, `result`              | SMTP commands (ok/failed) from the session statistics |
| `postfix_smtpd_aborted_connections_total`    | `service`, `reason`, `after`                | lost, timeout, too_many_errors after a command   |
| `postfix_smtpd_messages_accepted_total`      | `service`, `authenticated`                  | accepted messages                                |
| `postfix_smtpd_rejects_total`                | `service`, `command`, `code`, `reason`      | rejects with a reason class (see below)          |
| `postfix_smtpd_reject_warnings_total`        | `service`, `command`, `reason`              | rejects that were only logged (`warn_if_reject`) |
| `postfix_smtpd_reject_replies_total`         | `service`, `command`, `code`, `enhanced_code`, `text` | classified rejects (`replies.reject`)   |
| `postfix_smtpd_access_actions_total`         | `service`, `action`, `command`              | discard, hold, filter, redirect, warn, bcc       |
| `postfix_smtpd_sasl_authentication_failures_total` | `service`, `method`                   | failed logins (PLAIN, LOGIN, …, other)           |
| `postfix_smtpd_tls_connections_total`        | `service`, `trust`, `protocol`, `cipher`    | inbound TLS connections                          |
| `postfix_smtpd_tls_errors_total`             | `service`                                   | failed TLS handshakes                            |
| `postfix_smtpd_reverse_dns_failures_total`   | `service`, `type`                           | not_resolve, not_listed, verification_failed     |
| `postfix_smtpd_limits_exceeded_total`        | `service`, `limit`                          | connection_rate, connection_concurrency, message_rate, recipient_rate, newtls_rate, auth_rate |
| `postfix_milter_actions_total`               | `service`, `action`                         | actions requested by milters (smtpd, cleanup)    |
| `postfix_cleanup_messages_total`             | `service`                                   | messages processed by cleanup                    |
| `postfix_cleanup_check_actions_total`        | `service`, `check`, `action`                | actions of header_checks and body_checks         |
| `postfix_pickup_messages_total`              |                                             | locally submitted messages (sendmail)            |

The reason class `reason` of rejects is derived without any configuration from Postfix's fixed text
phrases: `dnsbl`, `relay_denied`, `unknown_recipient`, `recipient`, `sender`, `client`, `helo`, `data`,
`size`, `pipelining`, `too_many_recipients`, `tls_required`, `storage`, `protocol`, `other`.

### postscreen

| Metric                                        | Labels    | Meaning                                        |
|-----------------------------------------------|-----------|------------------------------------------------|
| `postfix_postscreen_connections_total`        |           | connections                                    |
| `postfix_postscreen_passes_total`             | `type`    | new (after tests), old (from the cache)        |
| `postfix_postscreen_tests_failed_total`       | `test`    | pregreet, non_smtp_command, command_pipelining, bare_newline, command_*_limit |
| `postfix_postscreen_dnsbl_rank`               |           | histogram: DNSBL score of listed clients       |
| `postfix_postscreen_hangups_total`            |           | clients that hang up during the tests          |
| `postfix_postscreen_rejects_total`            | `command` | CONNECT, RCPT                                  |
| `postfix_postscreen_access_list_matches_total`| `list`    | allowlist, denylist, allowlist_veto            |

### Operations

| Metric                                         | Labels                | Meaning                                      |
|------------------------------------------------|-----------------------|----------------------------------------------|
| `postfix_log_messages_total`                   | `service`, `severity` | all Postfix messages (info, warning, error, fatal, panic) |
| `postfix_master_process_failures_total`        | `program`, `reason`   | exit, signal, throttling                     |
| `postfix_exporter_log_lines_total`             | `result`              | matched, ignored, unmatched, foreign, invalid |
| `postfix_exporter_log_source_up`               |                       | log source is being read (1) or restarted (0) |
| `postfix_exporter_log_source_restarts_total`   |                       | restarts of the log source                   |
| `postfix_exporter_last_log_line_timestamp_seconds` |                   | time of the last Postfix line                |
| `postfix_exporter_build_info`                  | `version`, …          | build information                            |

`unmatched` counts Postfix lines for which the exporter knows no pattern. With `--log.level=debug` they are
logged. After a Postfix upgrade this is the first place where new log formats show up.

### Queries

```promql
# Delivery rate over the last hour
sum(increase(postfix_delivery_status_total{status="sent"}[1h]))
  / sum(increase(postfix_delivery_status_total[1h]))

# 95th percentile of the delay until delivery, per service
histogram_quantile(0.95, sum by (le, service) (rate(postfix_delivery_delay_seconds_bucket{status="sent"}[5m])))

# Rejects per minute by reason
sum by (reason) (rate(postfix_smtpd_rejects_total[5m])) * 60
```

## Grafana dashboard and alerts

- [`contrib/grafana/postfix.json`](contrib/grafana/postfix.json) – dashboard (Grafana ≥ 10), variables for
  data source and server. It is generated from [`generate.py`](contrib/grafana/generate.py); do not edit the
  JSON file by hand, make changes there and run `make dashboards`.
- [`contrib/alerts.yml`](contrib/alerts.yml) – Prometheus rules: Postfix down, log source, fatal errors,
  throttled services, queue size and age, hold queue, bounce and defer ratio, SASL brute force, failing
  local delivery, unrecognised log lines.

## Licence

AGPL-3.0, see [LICENSE](LICENSE). Parts come from projects under Apache-2.0 and BSD-3-Clause, see
[NOTICE](NOTICE).
