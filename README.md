# postfix_exporter

Prometheus-Exporter für einen Postfix-Mailserver. Ein einzelnes, statisches Binary liefert:

- **Mail-Queue** – Anzahl, Empfänger, Größe und Alter der Nachrichten pro Queue, live über den `showq`-Dienst
- **Zustellung** – Status (sent, deferred, bounced, …) pro Zustelldienst und DSN-Code, Laufzeiten als Histogramm
  inklusive der vier Phasen aus `delays=a/b/c/d`, für smtp, lmtp, local, virtual, pipe und error
- **Eingang** – Verbindungen, angenommene Nachrichten, Rejects mit automatischer Grund-Klasse, abgebrochene
  Sitzungen, fehlgeschlagene SMTP-Befehle, SASL-Fehler, TLS, anvil-Limits, Reverse-DNS
- **postscreen**, **Milter**, **header/body_checks**, **bounce**, **pickup**
- **Betrieb** – Log-Meldungen nach Dienst und Schweregrad, abgestürzte und gedrosselte Prozesse
- optional: **Klassifizierung der Antworttexte** über reguläre Ausdrücke

Die Log-Daten kommen aus einer Syslog-Datei (auch Postfix' eigenes `maillog_file`) oder aus dem
systemd-Journal. Ein Grafana-Dashboard und Alert-Regeln liegen unter [`contrib/`](contrib/).

Der Exporter vereint den Funktionsumfang von
[Hsn723/postfix_exporter](https://github.com/Hsn723/postfix_exporter) (showq, viele Log-Metriken) und
[sergeymakinen/postfix_exporter](https://github.com/sergeymakinen/postfix_exporter) (robuster Zeilen-Parser,
Antwort-Klassifizierung, Milter, Schweregrade, Replay-Modus); siehe [NOTICE](NOTICE). Docker und Kubernetes
werden bewusst nicht unterstützt, und es wird genau eine Postfix-Instanz überwacht.

## Bauen

```sh
make            # = make build, ./bin/postfix_exporter (statisch, CGO_ENABLED=0)
make test       # Tests mit Race-Detector
make ci         # fmt, vet, lint, vuln, test, build
make dist       # Release-Archive für linux/amd64 und linux/arm64
```

## Rechte

| Zugriff                              | nötig für                                       | Lösung                                                       |
|--------------------------------------|-------------------------------------------------|--------------------------------------------------------------|
| `/var/spool/postfix/public/showq`    | Queue-Metriken                                  | Gruppe `postdrop` (`public/` ist `0710 postfix:postdrop`)    |
| Systemd-Journal                      | `log.source: journal`                           | Gruppe `systemd-journal`                                     |
| `/var/log/mail.log`                  | `log.source: file`                              | Gruppe `adm` (Debian/Ubuntu) bzw. `log` (Arch)               |

Die systemd-Unit in [`contrib/postfix_exporter.service`](contrib/postfix_exporter.service) setzt die Gruppen
über `SupplementaryGroups=` und ist stark gehärtet.

## Konfiguration

Ohne `--config.file` gelten Defaults: Log aus `/var/log/mail.log`, Queue über
`unix:///var/spool/postfix/public/showq`. Alle Optionen mit Erklärung stehen in
[`config.example.yml`](config.example.yml). Die häufigsten Anpassungen:

```yaml
log:
  source: journal
  journal:
    units: [postfix.service]      # Debian/Ubuntu: postfix@-.service

services:
  smtp: [encrypt]                 # master.cf-Dienste, deren Name nicht auf den Daemon-Typ endet
```

Dienste wie `submission/smtpd` oder `relay/smtp` (über `syslog_name`) erkennt der Exporter selbst. Sie
erscheinen im Label `service` getrennt, so lassen sich z. B. SASL-Fehler auf Port 587 von denen auf Port 25
unterscheiden.

```sh
postfix_exporter --config.file=/etc/postfix_exporter/config.yml --config.check
```

### Flags

| Flag                   | Default    | Bedeutung                                                                       |
|------------------------|------------|---------------------------------------------------------------------------------|
| `--config.file`        | –          | Konfigurationsdatei                                                             |
| `--config.check`       | `false`    | Konfiguration prüfen und beenden                                                |
| `--web.listen-address` | `:9154`    | HTTP-Adresse                                                                    |
| `--web.telemetry-path` | `/metrics` | Pfad der Metriken                                                               |
| `--web.config.file`    | –          | TLS und Basic Auth ([Format](https://github.com/prometheus/exporter-toolkit/blob/master/docs/web-configuration.md)) |
| `--web.max-requests`   | `3`        | parallele Scrapes, 0 = unbegrenzt                                               |
| `--log.level`          | `info`     | `debug` loggt jede Postfix-Zeile, die der Exporter nicht kennt                  |
| `--replay`             | `false`    | vorhandenes Log einlesen, Metriken ausgeben, beenden                            |
| `--replay.since`       | `24h`      | Zeitraum für `--replay` mit dem Journal                                         |

### Antwort-Klassifizierung testen

Regeln unter `replies:` lassen sich gegen das echte Log entwickeln, ohne den laufenden Exporter anzufassen:

```sh
postfix_exporter --config.file=./config.yml --replay | grep -E '_replies_total|_rejects_total'
```

## Metriken

Alle Log-Metriken sind Zähler seit dem Start des Exporters. Er liest das Log ab dem Startzeitpunkt, wie
`tail -F`, auch über Logrotation hinweg.

### Queue (live über showq)

| Metrik                                       | Labels  | Bedeutung                                    |
|----------------------------------------------|---------|----------------------------------------------|
| `postfix_showq_up`                           |         | showq erreichbar (1) oder nicht (0)          |
| `postfix_showq_scrape_duration_seconds`      |         | Dauer des Queue-Abrufs                       |
| `postfix_queue_messages`                     | `queue` | Nachrichten (maildrop, incoming, active, deferred, hold) |
| `postfix_queue_recipients`                   | `queue` | Empfänger                                    |
| `postfix_queue_size_bytes`                   | `queue` | Gesamtgröße                                  |
| `postfix_queue_oldest_message_age_seconds`   | `queue` | Alter der ältesten Nachricht                 |
| `postfix_queue_message_age_seconds`          | `queue` | Histogramm: Alter                            |
| `postfix_queue_message_size_bytes`           | `queue` | Histogramm: Größe                            |

### Zustellung

| Metrik                                    | Labels                                         | Bedeutung                                      |
|-------------------------------------------|------------------------------------------------|------------------------------------------------|
| `postfix_delivery_status_total`           | `service`, `status`, `dsn`                     | Zustellversuche pro Empfänger                  |
| `postfix_delivery_delay_seconds`          | `service`, `status`                            | Histogramm: Annahme bis Zustellversuch         |
| `postfix_delivery_stage_delay_seconds`    | `service`, `stage`                             | Histogramm der Phasen erfolgreicher Zustellungen |
| `postfix_delivery_replies_total`          | `service`, `status`, `code`, `enhanced_code`, `text` | klassifizierte Antworten (`replies.delivery`) |
| `postfix_smtp_connection_failures_total`  | `service`, `reason`                            | timeout, refused, no_route, unreachable, other |
| `postfix_smtp_tls_connections_total`      | `service`, `trust`, `protocol`, `cipher`       | ausgehende TLS-Verbindungen                    |
| `postfix_smtp_replies_total`              | `service`, `code`, `enhanced_code`, `text`     | Antworten ohne Status, z. B. Greylisting (`replies.smtp`) |
| `postfix_qmgr_message_size_bytes`         |                                                | Histogramm: Größe beim Eintritt in die active-Queue |
| `postfix_qmgr_message_recipients`         |                                                | Histogramm: Empfänger pro Nachricht            |
| `postfix_qmgr_messages_removed_total`     |                                                | aus der Queue entfernte Nachrichten            |
| `postfix_qmgr_messages_expired_total`     | `type`                                         | expired, force-expired                         |
| `postfix_bounce_notifications_total`      | `recipient`, `type`                            | non_delivery, delivery_status, delay           |

`service` ist der Dienstname aus master.cf, z. B. `smtp`, `relay/smtp`, `lmtp`, `local`. Vom Queue-Manager
selbst entschiedene Verschiebungen („delivery temporarily suspended“) erscheinen mit `service="qmgr"`.

### Eingang

| Metrik                                       | Labels                                      | Bedeutung                                        |
|----------------------------------------------|---------------------------------------------|--------------------------------------------------|
| `postfix_smtpd_connections_total`            | `service`                                   | Verbindungen                                     |
| `postfix_smtpd_disconnections_total`         | `service`                                   | reguläre Verbindungsenden                        |
| `postfix_smtpd_commands_total`               | `service`, `command`, `result`              | SMTP-Befehle (ok/failed) aus der Sitzungsstatistik |
| `postfix_smtpd_aborted_connections_total`    | `service`, `reason`, `after`                | lost, timeout, too_many_errors nach Befehl       |
| `postfix_smtpd_messages_accepted_total`      | `service`, `authenticated`                  | angenommene Nachrichten                          |
| `postfix_smtpd_rejects_total`                | `service`, `command`, `code`, `reason`      | Rejects mit Grund-Klasse (s. u.)                 |
| `postfix_smtpd_reject_warnings_total`        | `service`, `command`, `reason`              | nur geloggte Rejects (`warn_if_reject`)          |
| `postfix_smtpd_reject_replies_total`         | `service`, `command`, `code`, `enhanced_code`, `text` | klassifizierte Rejects (`replies.reject`) |
| `postfix_smtpd_access_actions_total`         | `service`, `action`, `command`              | discard, hold, filter, redirect, warn, bcc       |
| `postfix_smtpd_sasl_authentication_failures_total` | `service`, `method`                   | fehlgeschlagene Anmeldungen                      |
| `postfix_smtpd_tls_connections_total`        | `service`, `trust`, `protocol`, `cipher`    | eingehende TLS-Verbindungen                      |
| `postfix_smtpd_tls_errors_total`             | `service`                                   | fehlgeschlagene TLS-Handshakes                   |
| `postfix_smtpd_reverse_dns_failures_total`   | `service`, `type`                           | not_resolve, not_listed, verification_failed     |
| `postfix_smtpd_limits_exceeded_total`        | `service`, `limit`                          | connection_rate, connection_concurrency, message_rate, recipient_rate, newtls_rate, auth_rate |
| `postfix_milter_actions_total`               | `service`, `action`                         | von Miltern verlangte Aktionen (smtpd, cleanup)  |
| `postfix_cleanup_messages_total`             | `service`                                   | von cleanup verarbeitete Nachrichten             |
| `postfix_cleanup_check_actions_total`        | `service`, `check`, `action`                | Aktionen von header_checks und body_checks       |
| `postfix_pickup_messages_total`              |                                             | lokal eingelieferte Nachrichten (sendmail)       |

Die Grund-Klasse `reason` der Rejects ergibt sich ohne Konfiguration aus den festen Textbausteinen von
Postfix: `dnsbl`, `relay_denied`, `unknown_recipient`, `recipient`, `sender`, `client`, `helo`, `data`,
`size`, `pipelining`, `too_many_recipients`, `tls_required`, `storage`, `protocol`, `other`.

### postscreen

| Metrik                                        | Labels    | Bedeutung                                      |
|-----------------------------------------------|-----------|------------------------------------------------|
| `postfix_postscreen_connections_total`        |           | Verbindungen                                   |
| `postfix_postscreen_passes_total`             | `type`    | new (nach Tests), old (aus dem Cache)          |
| `postfix_postscreen_tests_failed_total`       | `test`    | pregreet, non_smtp_command, command_pipelining, bare_newline, command_*_limit |
| `postfix_postscreen_dnsbl_rank`               |           | Histogramm: DNSBL-Score gelisteter Clients     |
| `postfix_postscreen_hangups_total`            |           | Clients, die während der Tests auflegen        |
| `postfix_postscreen_rejects_total`            | `command` | CONNECT, RCPT                                  |
| `postfix_postscreen_access_list_matches_total`| `list`    | allowlist, denylist, allowlist_veto            |

### Betrieb

| Metrik                                         | Labels                | Bedeutung                                    |
|------------------------------------------------|-----------------------|----------------------------------------------|
| `postfix_log_messages_total`                   | `service`, `severity` | alle Postfix-Meldungen (info, warning, error, fatal, panic) |
| `postfix_master_process_failures_total`        | `program`, `reason`   | exit, signal, throttling                     |
| `postfix_exporter_log_lines_total`             | `result`              | matched, ignored, unmatched, foreign, invalid |
| `postfix_exporter_log_source_up`               |                       | Log-Quelle wird gelesen (1) oder neu gestartet (0) |
| `postfix_exporter_log_source_restarts_total`   |                       | Neustarts der Log-Quelle                     |
| `postfix_exporter_last_log_line_timestamp_seconds` |                   | Zeit der letzten Postfix-Zeile               |
| `postfix_exporter_build_info`                  | `version`, …          | Build-Informationen                          |

`unmatched` zählt Postfix-Zeilen, für die der Exporter kein Muster kennt. Mit `--log.level=debug` werden sie
geloggt. Nach einem Postfix-Update ist das der erste Ort, an dem man neue Logformate sieht.

### Abfragen

```promql
# Zustellquote der letzten Stunde
sum(increase(postfix_delivery_status_total{status="sent"}[1h]))
  / sum(increase(postfix_delivery_status_total[1h]))

# 95. Perzentil der Laufzeit bis zur Zustellung, pro Dienst
histogram_quantile(0.95, sum by (le, service) (rate(postfix_delivery_delay_seconds_bucket{status="sent"}[5m])))

# Rejects pro Minute nach Grund
sum by (reason) (rate(postfix_smtpd_rejects_total[5m])) * 60
```

## Grafana-Dashboard und Alerts

- [`contrib/grafana/postfix.json`](contrib/grafana/postfix.json) – Dashboard (Grafana ≥ 10), Variablen für
  Datenquelle und Server. Es wird aus [`generate.py`](contrib/grafana/generate.py) erzeugt; Änderungen dort
  machen und `python3 contrib/grafana/generate.py contrib/grafana/postfix.json` ausführen.
- [`contrib/alerts.yml`](contrib/alerts.yml) – Prometheus-Regeln: Postfix down, Log-Quelle, fatale Fehler,
  gedrosselte Dienste, Queue-Größe und -Alter, Hold-Queue, Bounce- und Defer-Quote, SASL-Brute-Force,
  ausgefallene lokale Zustellung, unbekannte Logzeilen.

## Lizenz

AGPL-3.0, siehe [LICENSE](LICENSE). Teile stammen aus Projekten unter Apache-2.0 und BSD-3-Clause, siehe
[NOTICE](NOTICE).
