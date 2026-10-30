#!/usr/bin/env python3
"""Generates the Grafana dashboard for postfix_exporter.

Usage: python3 contrib/grafana/generate.py contrib/grafana/postfix.json
"""
import json
import sys

DS = {"type": "prometheus", "uid": "${datasource}"}
I = 'instance=~"$instance"'

panels = []
_id = [0]

def nid():
    _id[0] += 1
    return _id[0]


def pos(x, y, w, h):
    return {"x": x, "y": y, "w": w, "h": h}


def target(expr, legend="", ref="A", instant=False, table=False):
    t = {"datasource": DS, "refId": ref, "expr": expr, "legendFormat": legend or "__auto"}
    if instant or table:
        t.update({"instant": True, "range": False})
    if table:
        t["format"] = "table"
    return t


def thresholds(*steps):
    """steps: (color, value) with the first value None."""
    return {"mode": "absolute", "steps": [{"color": c, "value": v} for c, v in steps]}


def row(title, y):
    panels.append({"id": nid(), "type": "row", "title": title, "collapsed": False,
                   "gridPos": pos(0, y, 24, 1), "panels": []})


def stat(title, expr, x, y, w=3, h=4, unit="none", th=None, mappings=None, color_mode="background",
         legend="", text_mode="value", graph="none", decimals=None, description=""):
    defaults = {"unit": unit, "mappings": mappings or [],
                "color": {"mode": "thresholds" if th else "fixed", "fixedColor": "text"},
                "thresholds": th or thresholds(("text", None))}
    if decimals is not None:
        defaults["decimals"] = decimals
    panels.append({
        "id": nid(), "type": "stat", "title": title, "description": description, "datasource": DS,
        "gridPos": pos(x, y, w, h),
        "targets": [target(expr, legend, instant=graph == "none")],
        "fieldConfig": {"defaults": defaults, "overrides": []},
        "options": {"reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
                    "colorMode": color_mode if th else "none", "graphMode": graph,
                    "textMode": text_mode, "justifyMode": "center", "orientation": "auto",
                    "wideLayout": True, "showPercentChange": False},
    })


def timeseries(title, targets, x, y, w=12, h=8, unit="none", min_=0, max_=None, stack=False,
               fill=0, description="", legend_calcs=("mean", "max", "lastNotNull")):
    custom = {"lineWidth": 2, "fillOpacity": fill, "showPoints": "never", "spanNulls": False,
              "drawStyle": "line", "lineInterpolation": "linear", "axisBorderShow": False,
              "gradientMode": "none", "axisSoftMin": min_,
              "stacking": {"mode": "normal" if stack else "none", "group": "A"}}
    defaults = {"unit": unit, "custom": custom, "color": {"mode": "palette-classic-by-name"}}
    if max_ is not None:
        defaults["max"] = max_
    panels.append({
        "id": nid(), "type": "timeseries", "title": title, "description": description,
        "datasource": DS, "gridPos": pos(x, y, w, h), "targets": targets,
        "fieldConfig": {"defaults": defaults, "overrides": []},
        "options": {"legend": {"displayMode": "table", "placement": "bottom", "showLegend": True,
                               "calcs": list(legend_calcs), "sortBy": "Max", "sortDesc": True},
                    "tooltip": {"mode": "multi", "sort": "desc"}},
    })


def state_timeline(title, expr, legend, mappings, x, y, w=12, h=8, description=""):
    panels.append({
        "id": nid(), "type": "state-timeline", "title": title, "description": description,
        "datasource": DS, "gridPos": pos(x, y, w, h),
        "targets": [target(expr, legend)],
        "fieldConfig": {"defaults": {"mappings": mappings, "color": {"mode": "thresholds"},
                                     "thresholds": OK_TH,
                                     "custom": {"lineWidth": 0, "fillOpacity": 80}},
                        "overrides": []},
        "options": {"showValue": "never", "mergeValues": True, "alignValue": "left",
                    "rowHeight": 0.85, "legend": {"showLegend": False},
                    "tooltip": {"mode": "single", "sort": "none"}},
    })


def table(title, targets, x, y, w, h, rename, order, overrides, exclude=(), sort=None, description=""):
    back = {v: k for k, v in rename.items()}
    excl = {"Time": True, "instance": True, "job": True, "__name__": True}
    excl.update({k: True for k in exclude})
    opts = {"showHeader": True, "cellHeight": "sm", "footer": {"show": False}}
    if sort:
        opts["sortBy"] = [{"displayName": sort[0], "desc": sort[1]}]
    panels.append({
        "id": nid(), "type": "table", "title": title, "description": description, "datasource": DS,
        "gridPos": pos(x, y, w, h), "targets": targets,
        "transformations": [
            {"id": "merge", "options": {}},
            {"id": "organize", "options": {"excludeByName": excl, "renameByName": rename,
                                           "indexByName": {back.get(n, n): i for i, n in enumerate(order)}}},
        ],
        "fieldConfig": {"defaults": {"custom": {"align": "auto", "cellOptions": {"type": "auto"},
                                                "filterable": True, "minWidth": 70},
                                     "mappings": [], "thresholds": thresholds(("text", None))},
                        "overrides": overrides},
        "options": opts,
    })


def override(name, **props):
    return {"matcher": {"id": "byName", "options": name},
            "properties": [{"id": k.replace("__", "."), "value": v} for k, v in props.items()]}


def value_map(*entries):
    """entries: (value, text, color)"""
    return [{"type": "value", "options": {str(v): {"text": t, "color": c, "index": i}
                                          for i, (v, t, c) in enumerate(entries)}}]




def bargauge(title, expr, legend, x, y, w=12, h=8, unit="short", color="blue", description=""):
    panels.append({
        "id": nid(), "type": "bargauge", "title": title, "description": description,
        "datasource": DS, "gridPos": pos(x, y, w, h),
        "targets": [target(expr, legend, instant=True)],
        "fieldConfig": {"defaults": {"unit": unit, "min": 0, "decimals": 0,
                                     "color": {"mode": "fixed", "fixedColor": color},
                                     "thresholds": thresholds((color, None))}, "overrides": []},
        "options": {"orientation": "horizontal", "displayMode": "basic", "showUnfilled": True,
                    "valueMode": "text", "namePlacement": "left", "sizing": "auto", "minVizHeight": 16,
                    "maxVizHeight": 24,
                    "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False}},
    })


def per_min(expr, by):
    """Events per minute, summed by the given labels."""
    return f"sum by ({by}) (rate({expr}[$__rate_interval])) * 60"


def in_range(expr, by):
    """Events in the selected time range, largest first, zeros dropped."""
    return f"sort_desc(sum by ({by}) (increase({expr}[$__range])) > 0)"


def quantile(q, metric, by, sel=""):
    return f"histogram_quantile({q}, sum by (le, {by}) (rate({metric}_bucket{{{I}{sel}}}[$__rate_interval])))"


UP_MAP = value_map((1, "UP", "green"), (0, "DOWN", "red"))
OK_TH = thresholds(("red", None), ("green", 1))
BAD_TH = thresholds(("green", None), ("red", 1))
CPM = "cpm"

# --- Übersicht -------------------------------------------------------------------------
y = 0
stat("Postfix", f"min(postfix_showq_up{{{I}}})", 0, y, mappings=UP_MAP, th=OK_TH,
     description="Ob die Queue über showq gelesen werden kann. DOWN heißt meist: Postfix läuft nicht.")
stat("Log-Quelle", f"min(postfix_exporter_log_source_up{{{I}}})", 3, y, mappings=UP_MAP, th=OK_TH,
     description="Ob der Exporter das Mail-Log bzw. Journal liest.")
stat("Queue gesamt", f"sum(postfix_queue_messages{{{I}}})", 6, y, graph="area",
     th=thresholds(("text", None), ("orange", 500), ("red", 2000)))
stat("Deferred", f'sum(postfix_queue_messages{{{I}, queue="deferred"}})', 9, y, graph="area",
     th=thresholds(("green", None), ("orange", 50), ("red", 200)),
     description="Nachrichten, deren Zustellung vorübergehend fehlgeschlagen ist und wiederholt wird.")
stat("Hold", f'sum(postfix_queue_messages{{{I}, queue="hold"}})', 12, y,
     th=thresholds(("green", None), ("orange", 1)),
     description="Angehaltene Nachrichten (HOLD-Aktion, postsuper -h). Brauchen einen manuellen Eingriff.")
stat("Älteste deferred", f'max(postfix_queue_oldest_message_age_seconds{{{I}, queue="deferred"}})', 15, y,
     unit="s", th=thresholds(("green", None), ("orange", 4 * 3600), ("red", 86400)),
     description="Alter der ältesten Nachricht in der deferred-Queue.")
stat("Zustellquote",
     f'sum(increase(postfix_delivery_status_total{{{I}, status="sent"}}[$__range]))'
     f' / sum(increase(postfix_delivery_status_total{{{I}, status=~"sent|bounced|deferred|expired"}}[$__range]))',
     18, y, unit="percentunit", decimals=1, th=thresholds(("red", None), ("orange", 0.8), ("green", 0.95)),
     description="Anteil erfolgreicher Zustellversuche (sent) an allen Versuchen im Zeitraum.")
stat("Fatal / Panic", f'sum(increase(postfix_log_messages_total{{{I}, severity=~"fatal|panic"}}[$__range]))',
     21, y, decimals=0, th=BAD_TH, description="Fatale Fehler von Postfix-Diensten im Zeitraum.")
y += 4
stat("Angenommen", f"sum(increase(postfix_smtpd_messages_accepted_total{{{I}}}[$__range]))", 0, y, decimals=0,
     graph="area", description="Von smtpd angenommene Nachrichten im Zeitraum.")
stat("Zugestellt", f'sum(increase(postfix_delivery_status_total{{{I}, status="sent"}}[$__range]))', 3, y,
     decimals=0, graph="area", description="Erfolgreich zugestellte Empfänger im Zeitraum (alle Zustelldienste).")
stat("Bounced", f'sum(increase(postfix_delivery_status_total{{{I}, status="bounced"}}[$__range]))', 6, y,
     decimals=0, th=thresholds(("text", None), ("orange", 10)))
stat("Rejects", f"sum(increase(postfix_smtpd_rejects_total{{{I}}}[$__range]))", 9, y, decimals=0,
     description="Von smtpd-Restriktionen abgewiesene Versuche im Zeitraum.")
stat("postscreen blockiert",
     f"sum(increase(postfix_postscreen_rejects_total{{{I}}}[$__range]))"
     f" + sum(increase(postfix_postscreen_tests_failed_total{{{I}}}[$__range]))",
     12, y, decimals=0, description="Von postscreen abgewiesene Clients und fehlgeschlagene Tests.")
stat("SASL-Fehler", f"sum(increase(postfix_smtpd_sasl_authentication_failures_total{{{I}}}[$__range]))", 15, y,
     decimals=0, th=thresholds(("text", None), ("orange", 50), ("red", 500)),
     description="Fehlgeschlagene Anmeldungen im Zeitraum. Viele davon deuten auf Passwort-Raten hin.")
stat("Prozessfehler", f"sum(increase(postfix_master_process_failures_total{{{I}}}[$__range]))", 18, y,
     decimals=0, th=BAD_TH, description="Abgestürzte oder gedrosselte Postfix-Prozesse (master).")
stat("Unbekannte Zeilen", f'sum(increase(postfix_exporter_log_lines_total{{{I}, result="unmatched"}}[$__range]))',
     21, y, decimals=0, th=thresholds(("text", None), ("orange", 1)),
     description="Postfix-Zeilen, die der Exporter nicht kennt. Mit --log.level=debug werden sie geloggt.")
y += 4

# --- Mail-Queue ------------------------------------------------------------------------
row("Mail-Queue", y)
y += 1
timeseries("Nachrichten nach Queue", [target(f"sum by (queue) (postfix_queue_messages{{{I}}})", "{{queue}}")],
           0, y, legend_calcs=("min", "max", "lastNotNull"))
timeseries("Älteste Nachricht nach Queue",
           [target(f"max by (queue) (postfix_queue_oldest_message_age_seconds{{{I}}})", "{{queue}}")],
           12, y, unit="s", legend_calcs=("max", "lastNotNull"))
y += 8
timeseries("Queue-Größe", [target(f"sum by (queue) (postfix_queue_size_bytes{{{I}}})", "{{queue}}")],
           0, y, unit="bytes", legend_calcs=("max", "lastNotNull"))
timeseries("Empfänger in der Queue", [target(f"sum by (queue) (postfix_queue_recipients{{{I}}})", "{{queue}}")],
           12, y, legend_calcs=("max", "lastNotNull"))
y += 8

# --- Zustellung ------------------------------------------------------------------------
row("Zustellung", y)
y += 1
timeseries("Zustellversuche nach Status",
           [target(per_min(f"postfix_delivery_status_total{{{I}}}", "status"), "{{status}}")],
           0, y, unit=CPM, description="Versuche pro Empfänger und Minute, alle Zustelldienste.")
timeseries("Zustellversuche nach Dienst",
           [target(per_min(f"postfix_delivery_status_total{{{I}}}", "service, status"), "{{service}} {{status}}")],
           12, y, unit=CPM, description="smtp, lmtp, local, virtual, pipe ... wie in master.cf benannt.")
y += 8
timeseries("Laufzeit bis Zustellung (p50 / p95)",
           [target(quantile(0.5, "postfix_delivery_delay_seconds", "service", ', status="sent"'),
                   "p50 {{service}}", ref="A"),
            target(quantile(0.95, "postfix_delivery_delay_seconds", "service", ', status="sent"'),
                   "p95 {{service}}", ref="B")],
           0, y, unit="s", description="Zeit von der Annahme bis zur erfolgreichen Zustellung (delay=).")
timeseries("Laufzeit nach Phase (p95)",
           [target(quantile(0.95, "postfix_delivery_stage_delay_seconds", "stage"), "{{stage}}")],
           12, y, unit="s",
           description="delays=a/b/c/d: vor dem Queue-Manager, im Queue-Manager, Verbindungsaufbau, Übertragung.")
y += 8
table("Zustellversuche nach DSN",
      [target(f"sum by (service, status, dsn) (increase(postfix_delivery_status_total{{{I}}}[$__range])) > 0",
              ref="A", table=True)],
      0, y, 12, 8,
      rename={"service": "Dienst", "status": "Status", "dsn": "DSN", "Value": "Anzahl"},
      order=["Dienst", "Status", "DSN", "Anzahl"], sort=("Anzahl", True),
      overrides=[override("Anzahl", decimals=0)],
      description="DSN-Codes im Zeitraum: 4.x.x vorübergehend, 5.x.x dauerhaft (RFC 3463).")
bargauge("Antwort-Klassen", in_range(f'postfix_delivery_replies_total{{{I}, status!="sent"}}', "status, text"),
         "{{status}}: {{text}}", 12, y, color="orange",
         description="Klassifizierte Antworten der Gegenstelle (replies.delivery in der Konfiguration).")
y += 8
timeseries("Ausgehende Verbindungsfehler",
           [target(per_min(f"postfix_smtp_connection_failures_total{{{I}}}", "reason"), "{{reason}}")],
           0, y, unit=CPM)
timeseries("Bounce-Benachrichtigungen",
           [target(per_min(f"postfix_bounce_notifications_total{{{I}}}", "recipient, type"), "{{recipient}} {{type}}")],
           12, y, unit=CPM, description="Vom bounce-Dienst erzeugte Benachrichtigungen (NDR, Verzögerung, DSN).")
y += 8

# --- Eingang ---------------------------------------------------------------------------
row("Eingang (smtpd)", y)
y += 1
timeseries("Verbindungen", [target(per_min(f"postfix_smtpd_connections_total{{{I}}}", "service"), "{{service}}")],
           0, y, unit=CPM)
timeseries("Angenommene Nachrichten",
           [target(per_min(f"postfix_smtpd_messages_accepted_total{{{I}}}", "service, authenticated"),
                   "{{service}} (auth: {{authenticated}})")],
           12, y, unit=CPM)
y += 8
timeseries("Rejects nach Grund",
           [target(per_min(f"postfix_smtpd_rejects_total{{{I}}}", "reason"), "{{reason}}")],
           0, y, unit=CPM, stack=True, fill=30,
           description="Klassen aus dem Reject-Text: dnsbl, relay_denied, unknown_recipient, client, helo, sender ...")
bargauge("Reject-Texte im Zeitraum", in_range(f"postfix_smtpd_reject_replies_total{{{I}}}", "text"), "{{text}}",
         12, y, color="red", description="Klassifiziert nach replies.reject in der Konfiguration.")
y += 8
timeseries("Abgebrochene Sitzungen",
           [target(per_min(f"postfix_smtpd_aborted_connections_total{{{I}}}", "reason, after"),
                   "{{reason}} nach {{after}}")],
           0, y, unit=CPM, description="lost connection, timeout und too many errors, nach letztem Befehl.")
timeseries("Fehlgeschlagene SMTP-Befehle",
           [target(per_min(f'postfix_smtpd_commands_total{{{I}, result="failed"}}', "command"), "{{command}}")],
           12, y, unit=CPM, description="Aus der Befehlsstatistik am Sitzungsende, z.B. rcpt=0/1.")
y += 8
timeseries("SASL-Fehler",
           [target(per_min(f"postfix_smtpd_sasl_authentication_failures_total{{{I}}}", "service, method"),
                   "{{service}} {{method}}")],
           0, y, unit=CPM)
timeseries("Limits und Reverse-DNS",
           [target(per_min(f"postfix_smtpd_limits_exceeded_total{{{I}}}", "limit"), "Limit {{limit}}", ref="A"),
            target(per_min(f"postfix_smtpd_reverse_dns_failures_total{{{I}}}", "type"), "rDNS {{type}}", ref="B")],
           12, y, unit=CPM, description="Von anvil durchgesetzte Client-Limits und fehlerhafte Reverse-DNS-Einträge.")
y += 8

# --- TLS -------------------------------------------------------------------------------
row("TLS", y)
y += 1
timeseries("Eingehend nach Protokoll",
           [target(per_min(f"postfix_smtpd_tls_connections_total{{{I}}}", "protocol"), "{{protocol}}", ref="A"),
            target(per_min(f"postfix_smtpd_tls_errors_total{{{I}}}", "service"), "Fehler {{service}}", ref="B")],
           0, y, unit=CPM)
timeseries("Ausgehend nach Vertrauensstufe",
           [target(per_min(f"postfix_smtp_tls_connections_total{{{I}}}", "trust"), "{{trust}}")],
           12, y, unit=CPM,
           description="Anonymous/Untrusted: verschlüsselt ohne geprüftes Zertifikat; Trusted/Verified: geprüft.")
y += 8

# --- postscreen ------------------------------------------------------------------------
row("postscreen", y)
y += 1
timeseries("Verbindungen und Ergebnisse",
           [target(per_min(f"postfix_postscreen_connections_total{{{I}}}", "instance"), "Verbindungen", ref="A"),
            target(per_min(f"postfix_postscreen_passes_total{{{I}}}", "type"), "PASS {{type}}", ref="B"),
            target(per_min(f"postfix_postscreen_rejects_total{{{I}}}", "command"), "Reject {{command}}", ref="C"),
            target(per_min(f"postfix_postscreen_hangups_total{{{I}}}", "instance"), "Hangup", ref="D")],
           0, y, unit=CPM)
timeseries("Fehlgeschlagene Tests",
           [target(per_min(f"postfix_postscreen_tests_failed_total{{{I}}}", "test"), "{{test}}", ref="A"),
            target(per_min(f"postfix_postscreen_access_list_matches_total{{{I}}}", "list"), "Liste {{list}}", ref="B")],
           12, y, unit=CPM)
y += 8

# --- Filter ----------------------------------------------------------------------------
row("Filter und lokale Einlieferung", y)
y += 1
timeseries("Milter-Aktionen",
           [target(per_min(f"postfix_milter_actions_total{{{I}}}", "service, action"), "{{service}} {{action}}")],
           0, y, unit=CPM, description="Von Miltern (rspamd, opendkim, ...) verlangte Aktionen.")
timeseries("header/body_checks und pickup",
           [target(per_min(f"postfix_cleanup_check_actions_total{{{I}}}", "check, action"), "{{check}} {{action}}",
                   ref="A"),
            target(per_min(f"postfix_pickup_messages_total{{{I}}}", "instance"), "lokal eingeliefert", ref="B")],
           12, y, unit=CPM)
y += 8

# --- Betrieb ---------------------------------------------------------------------------
row("Betrieb", y)
y += 1
timeseries("Warnungen und Fehler",
           [target(per_min(f'postfix_log_messages_total{{{I}, severity!="info"}}', "service, severity"),
                   "{{service}} {{severity}}")],
           0, y, unit=CPM)
timeseries("Prozessfehler",
           [target(per_min(f"postfix_master_process_failures_total{{{I}}}", "program, reason"),
                   "{{program}} {{reason}}")],
           12, y, unit=CPM, description="exit: Prozess mit Fehler beendet; signal: abgestürzt; throttling: "
                                        "master startet den Dienst wegen wiederholter Fehler verzögert.")
y += 8
timeseries("Log-Zeilen des Exporters",
           [target(per_min(f"postfix_exporter_log_lines_total{{{I}}}", "result"), "{{result}}")],
           0, y, unit=CPM,
           description="matched: in Metriken; ignored: bekannt, ohne eigene Metrik; unmatched: unbekannt; "
                       "foreign: andere Programme; invalid: kein Syslog-Format.")
timeseries("Nachrichtengröße (p95)",
           [target(quantile(0.95, "postfix_qmgr_message_size_bytes", "instance"), "Größe p95", ref="A")],
           12, y, unit="bytes", description="Größe der Nachrichten beim Eintritt in die active-Queue.")
y += 8


def var_query(name, label, query, include_all=True):
    return {"type": "query", "name": name, "label": label, "datasource": DS,
            "query": {"query": query, "refId": f"var-{name}", "qryType": 1},
            "definition": query, "refresh": 2, "multi": True, "includeAll": include_all,
            "allValue": ".*", "current": {"text": ["All"], "value": ["$__all"]}, "sort": 1}


dashboard = {
    "uid": "postfix-exporter",
    "title": "Postfix",
    "description": "Queue, Zustellung, Eingang, TLS, postscreen und Betrieb (postfix_exporter).",
    "tags": ["postfix", "mail"],
    "editable": True,
    "graphTooltip": 1,
    "timezone": "browser",
    "time": {"from": "now-6h", "to": "now"},
    "refresh": "1m",
    "schemaVersion": 39,
    "version": 1,
    "links": [],
    "annotations": {"list": [{"builtIn": 1, "datasource": {"type": "grafana", "uid": "-- Grafana --"},
                              "enable": True, "hide": True, "iconColor": "rgba(0, 211, 255, 1)",
                              "name": "Annotations & Alerts", "type": "dashboard"}]},
    "templating": {"list": [
        {"type": "datasource", "name": "datasource", "label": "Datenquelle", "query": "prometheus",
         "current": {}, "hide": 0, "refresh": 1, "regex": ""},
        var_query("instance", "Server", "label_values(postfix_exporter_build_info, instance)"),
    ]},
    "panels": panels,
}

json.dump(dashboard, open(sys.argv[1], "w"), indent=2, ensure_ascii=False)
open(sys.argv[1], "a").write("\n")
print(f"{len(panels)} panels")
