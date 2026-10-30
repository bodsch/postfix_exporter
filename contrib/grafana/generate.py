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
         legend="", text_mode="value", graph="none", decimals=None, description="", no_value=None):
    defaults = {"unit": unit, "mappings": mappings or [],
                "color": {"mode": "thresholds" if th else "fixed", "fixedColor": "text"},
                "thresholds": th or thresholds(("text", None))}
    if decimals is not None:
        defaults["decimals"] = decimals
    if no_value is not None:
        defaults["noValue"] = no_value
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


def total(sel):
    """Events in the selected time range; 0 instead of "No data" before the first one."""
    return f"(sum(increase({sel}[$__range])) or vector(0))"


def scoped(expr):
    """Limits process_* and go_* to postfix_exporter: every Go exporter exports them, and
    the instance variable "All" matches any target."""
    return f"{expr} and on (job, instance) postfix_exporter_build_info{{{I}}}"


def quantile(q, metric, by, sel=""):
    return f"histogram_quantile({q}, sum by (le, {by}) (rate({metric}_bucket{{{I}{sel}}}[$__rate_interval])))"


OTHER = " Values beyond the label limits appear as other (README: Bounded label values)."
UP_MAP = value_map((1, "UP", "green"), (0, "DOWN", "red"))
OK_TH = thresholds(("red", None), ("green", 1))
BAD_TH = thresholds(("green", None), ("red", 1))
CPM = "cpm"

# --- Overview ---------------------------------------------------------------------------
y = 0
stat("Postfix", f"min(postfix_showq_up{{{I}}})", 0, y, mappings=UP_MAP, th=OK_TH,
     description="Whether the queue can be read via showq. DOWN usually means: Postfix is not running.")
stat("Log source", f"min(postfix_exporter_log_source_up{{{I}}})", 3, y, mappings=UP_MAP, th=OK_TH,
     description="Whether the exporter is reading the mail log or journal.")
stat("Active / Incoming", f'max(postfix_queue_messages{{{I}, queue=~"active|incoming|maildrop"}})', 6, y,
     graph="area", th=thresholds(("green", None), ("orange", 250), ("red", 500)),
     description="The fullest of the active, incoming and maildrop queues, as PostfixActiveQueueHigh checks them: "
                 "per queue and server, not summed. Red from 500: that is where the alert fires (after 10 "
                 "minutes). deferred and hold have tiles of their own.")
stat("Deferred", f'sum(postfix_queue_messages{{{I}, queue="deferred"}})', 9, y, graph="area",
     th=thresholds(("green", None), ("orange", 50), ("red", 100)),
     description="Messages whose delivery failed temporarily and will be retried. "
                 "Red from 100: that is where PostfixDeferredQueueHigh fires (after 15 minutes).")
stat("Hold", f'sum(postfix_queue_messages{{{I}, queue="hold"}})', 12, y,
     th=thresholds(("green", None), ("orange", 1)),
     description="Messages on hold (HOLD action, postsuper -h). They need manual intervention.")
stat("Oldest deferred", f'max(postfix_queue_oldest_message_age_seconds{{{I}, queue="deferred"}})', 15, y,
     unit="s", th=thresholds(("green", None), ("orange", 4 * 3600), ("red", 86400)),
     description="Age of the oldest message in the deferred queue.")
stat("Delivery rate",
     f'sum(increase(postfix_delivery_status_total{{{I}, status="sent"}}[$__range]))'
     f' / sum(increase(postfix_delivery_status_total{{{I}, status=~"sent|bounced|deferred|expired"}}[$__range]))',
     18, y, unit="percentunit", decimals=1, th=thresholds(("red", None), ("orange", 0.8), ("green", 0.95)),
     no_value="no deliveries",
     description="Share of successful delivery attempts (sent) in all attempts in the time range.")
stat("Fatal / Panic", total(f'postfix_log_messages_total{{{I}, severity=~"fatal|panic"}}'),
     21, y, decimals=0, th=BAD_TH, description="Fatal errors of Postfix services in the time range.")
y += 4
stat("Accepted", total(f"postfix_smtpd_messages_accepted_total{{{I}}}"), 0, y, decimals=0,
     graph="area", description="Messages accepted by smtpd in the time range.")
stat("Delivered", total(f'postfix_delivery_status_total{{{I}, status="sent"}}'), 3, y,
     decimals=0, graph="area", description="Recipients delivered successfully in the time range (all delivery services).")
stat("Bounced", total(f'postfix_delivery_status_total{{{I}, status="bounced"}}'), 6, y,
     decimals=0, th=thresholds(("text", None), ("orange", 10)))
stat("Rejects", total(f"postfix_smtpd_rejects_total{{{I}}}"), 9, y, decimals=0,
     description="Attempts rejected by smtpd restrictions in the time range.")
stat("postscreen blocked",
     total(f"postfix_postscreen_rejects_total{{{I}}}") + " + " + total(f"postfix_postscreen_tests_failed_total{{{I}}}"),
     12, y, decimals=0, description="Clients rejected by postscreen and failed tests.")
stat("SASL failures", total(f"postfix_smtpd_sasl_authentication_failures_total{{{I}}}"), 15, y,
     decimals=0, th=thresholds(("text", None), ("orange", 50), ("red", 500)),
     description="Failed logins in the time range. Many of them point to password guessing. "
                 "The alert PostfixSASLAuthenticationFailures fires from 50 in 10 minutes; the tile counts "
                 "over the whole time range.")
stat("Process failures", total(f"postfix_master_process_failures_total{{{I}}}"), 18, y,
     decimals=0, th=BAD_TH, description="Crashed or throttled Postfix processes (master).")
stat("Unrecognised lines", total(f'postfix_exporter_log_lines_total{{{I}, result="unmatched"}}'),
     21, y, decimals=0, th=thresholds(("text", None), ("orange", 1)),
     description="Postfix lines the exporter does not recognise. With --log.level=debug they are logged.")
y += 4

# --- Mail queue ------------------------------------------------------------------------
row("Mail queue", y)
y += 1
timeseries("Messages by queue", [target(f"sum by (queue) (postfix_queue_messages{{{I}}})", "{{queue}}")],
           0, y, legend_calcs=("min", "max", "lastNotNull"))
timeseries("Oldest message by queue",
           [target(f"max by (queue) (postfix_queue_oldest_message_age_seconds{{{I}}})", "{{queue}}")],
           12, y, unit="s", legend_calcs=("max", "lastNotNull"))
y += 8
timeseries("Queue size", [target(f"sum by (queue) (postfix_queue_size_bytes{{{I}}})", "{{queue}}")],
           0, y, unit="bytes", legend_calcs=("max", "lastNotNull"))
timeseries("Recipients in the queue", [target(f"sum by (queue) (postfix_queue_recipients{{{I}}})", "{{queue}}")],
           12, y, legend_calcs=("max", "lastNotNull"))
y += 8

# --- Delivery --------------------------------------------------------------------------
row("Delivery", y)
y += 1
timeseries("Delivery attempts by status",
           [target(per_min(f"postfix_delivery_status_total{{{I}}}", "status"), "{{status}}")],
           0, y, unit=CPM, description="Attempts per recipient and minute, all delivery services.")
timeseries("Delivery attempts by service",
           [target(per_min(f"postfix_delivery_status_total{{{I}}}", "service, status"), "{{service}} {{status}}")],
           12, y, unit=CPM, description="smtp, lmtp, local, virtual, pipe ... as named in master.cf.")
y += 8
timeseries("Delay until delivery (p50 / p95)",
           [target(quantile(0.5, "postfix_delivery_delay_seconds", "service", ', status="sent"'),
                   "p50 {{service}}", ref="A"),
            target(quantile(0.95, "postfix_delivery_delay_seconds", "service", ', status="sent"'),
                   "p95 {{service}}", ref="B")],
           0, y, unit="s", description="Time from arrival to successful delivery (delay=).")
timeseries("Delay by stage (p95)",
           [target(quantile(0.95, "postfix_delivery_stage_delay_seconds", "stage"), "{{stage}}")],
           12, y, unit="s",
           description="delays=a/b/c/d: before the queue manager, in the queue manager, connection setup, transmission.")
y += 8
table("Delivery attempts by DSN",
      [target(f"sum by (service, status, dsn) (increase(postfix_delivery_status_total{{{I}}}[$__range])) > 0",
              ref="A", table=True)],
      0, y, 12, 8,
      rename={"service": "Service", "status": "Status", "dsn": "DSN", "Value": "Count"},
      order=["Service", "Status", "DSN", "Count"], sort=("Count", True),
      overrides=[override("Count", decimals=0)],
      description="DSN codes in the time range: 4.x.x temporary, 5.x.x permanent (RFC 3463). "
                  "More than 200 distinct codes appear as other.")
bargauge("Reply classes", in_range(f'postfix_delivery_replies_total{{{I}, status!="sent"}}', "status, text"),
         "{{status}}: {{text}}", 12, y, color="orange",
         description="Classified replies of the remote side (replies.delivery in the configuration).")
y += 8
timeseries("Outbound connection failures",
           [target(per_min(f"postfix_smtp_connection_failures_total{{{I}}}", "reason"), "{{reason}}")],
           0, y, unit=CPM)
timeseries("Bounce notifications",
           [target(per_min(f"postfix_bounce_notifications_total{{{I}}}", "recipient, type"), "{{recipient}} {{type}}")],
           12, y, unit=CPM, description="Notifications generated by the bounce service (NDR, delay, DSN).")
y += 8
timeseries("Queue manager",
           [target(per_min(f"postfix_qmgr_messages_removed_total{{{I}}}", "instance"), "removed", ref="A"),
            target(per_min(f"postfix_qmgr_messages_expired_total{{{I}}}", "type"), "{{type}}", ref="B")],
           0, y, unit=CPM,
           description="removed: message finished (delivered, bounced or expired). expired: returned to the "
                       "sender after maximal_queue_lifetime; force-expired: via postsuper -e.")
bargauge("Replies without status", in_range(f"postfix_smtp_replies_total{{{I}}}", "code, text"),
         "{{code}} {{text}}", 12, y, color="orange",
         description="Replies of the remote side without a delivery status, e.g. greylisting on RCPT TO "
                     "(replies.smtp in the configuration).")
y += 8

# --- Inbound ---------------------------------------------------------------------------
row("Inbound (smtpd)", y)
y += 1
timeseries("Connections",
           [target(per_min(f"postfix_smtpd_connections_total{{{I}}}", "service"), "{{service}}", ref="A"),
            target(per_min(f"postfix_smtpd_disconnections_total{{{I}}}", "service"), "{{service}} ended regularly",
                   ref="B")],
           0, y, unit=CPM,
           description="Connections and regular disconnections with command statistics; the difference is "
                       "aborted sessions." + OTHER)
timeseries("Accepted messages",
           [target(per_min(f"postfix_smtpd_messages_accepted_total{{{I}}}", "service, authenticated"),
                   "{{service}} (auth: {{authenticated}})")],
           12, y, unit=CPM)
y += 8
timeseries("Rejects by reason",
           [target(per_min(f"postfix_smtpd_rejects_total{{{I}}}", "reason"), "{{reason}}")],
           0, y, unit=CPM, stack=True, fill=30,
           description="Classes from the reject text: dnsbl, relay_denied, unknown_recipient, client, helo, sender ...")
bargauge("Reject texts in the time range", in_range(f"postfix_smtpd_reject_replies_total{{{I}}}", "text"), "{{text}}",
         12, y, color="red", description="Classified by replies.reject in the configuration.")
y += 8
timeseries("Aborted sessions",
           [target(per_min(f"postfix_smtpd_aborted_connections_total{{{I}}}", "reason, after"),
                   "{{reason}} after {{after}}")],
           0, y, unit=CPM, description="lost connection, timeout and too many errors, by the last command.")
timeseries("Failed SMTP commands",
           [target(per_min(f'postfix_smtpd_commands_total{{{I}, result="failed"}}', "command"), "{{command}}")],
           12, y, unit=CPM, description="From the command statistics at the end of a session, e.g. rcpt=0/1.")
y += 8
timeseries("SASL failures",
           [target(per_min(f"postfix_smtpd_sasl_authentication_failures_total{{{I}}}", "service, method"),
                   "{{service}} {{method}}")],
           0, y, unit=CPM,
           description="method other: a mechanism the client made up, often a scanner.")
timeseries("Limits and reverse DNS",
           [target(per_min(f"postfix_smtpd_limits_exceeded_total{{{I}}}", "limit"), "Limit {{limit}}", ref="A"),
            target(per_min(f"postfix_smtpd_reverse_dns_failures_total{{{I}}}", "type"), "rDNS {{type}}", ref="B")],
           12, y, unit=CPM, description="Client limits enforced by anvil and broken reverse DNS entries.")
y += 8
timeseries("Logged-only rejects",
           [target(per_min(f"postfix_smtpd_reject_warnings_total{{{I}}}", "reason"), "{{reason}}")],
           0, y, unit=CPM,
           description="Rejects that warn_if_reject only logs. Shows, before a restriction is enforced, "
                       "what it would reject.")
timeseries("Access actions",
           [target(per_min(f"postfix_smtpd_access_actions_total{{{I}}}", "action, command"),
                   "{{action}} at {{command}}")],
           12, y, unit=CPM, description="Actions from access tables other than reject: discard, hold, filter, "
                                        "redirect, warn, bcc.")
y += 8

# --- TLS -------------------------------------------------------------------------------
row("TLS", y)
y += 1
timeseries("Inbound by protocol",
           [target(per_min(f"postfix_smtpd_tls_connections_total{{{I}}}", "protocol"), "{{protocol}}", ref="A"),
            target(per_min(f"postfix_smtpd_tls_errors_total{{{I}}}", "service"), "errors {{service}}", ref="B")],
           0, y, unit=CPM)
timeseries("Outbound by trust level",
           [target(per_min(f"postfix_smtp_tls_connections_total{{{I}}}", "trust"), "{{trust}}")],
           12, y, unit=CPM,
           description="Anonymous/Untrusted: encrypted without a verified certificate; Trusted/Verified: verified.")
y += 8

# --- postscreen ------------------------------------------------------------------------
row("postscreen", y)
y += 1
timeseries("Connections and results",
           [target(per_min(f"postfix_postscreen_connections_total{{{I}}}", "instance"), "connections", ref="A"),
            target(per_min(f"postfix_postscreen_passes_total{{{I}}}", "type"), "PASS {{type}}", ref="B"),
            target(per_min(f"postfix_postscreen_rejects_total{{{I}}}", "command"), "Reject {{command}}", ref="C"),
            target(per_min(f"postfix_postscreen_hangups_total{{{I}}}", "instance"), "Hangup", ref="D")],
           0, y, unit=CPM)
timeseries("Failed tests",
           [target(per_min(f"postfix_postscreen_tests_failed_total{{{I}}}", "test"), "{{test}}", ref="A"),
            target(per_min(f"postfix_postscreen_access_list_matches_total{{{I}}}", "list"), "list {{list}}", ref="B")],
           12, y, unit=CPM)
y += 8
timeseries("DNSBL score of listed clients (p50 / p95)",
           [target(quantile(0.5, "postfix_postscreen_dnsbl_rank", "instance"), "p50", ref="A"),
            target(quantile(0.95, "postfix_postscreen_dnsbl_rank", "instance"), "p95", ref="B")],
           0, y, w=24, h=6,
           description="Summed score from postscreen_dnsbl_sites for clients listed by at least one list. "
                       "If p95 sits just below postscreen_dnsbl_threshold, the weights are worth a look.")
y += 6

# --- Filter ----------------------------------------------------------------------------
row("Filters and local submission", y)
y += 1
timeseries("Milter actions",
           [target(per_min(f"postfix_milter_actions_total{{{I}}}", "service, action"), "{{service}} {{action}}")],
           0, y, unit=CPM, description="Actions requested by milters (rspamd, opendkim, ...).")
timeseries("header/body_checks and pickup",
           [target(per_min(f"postfix_cleanup_check_actions_total{{{I}}}", "check, action"), "{{check}} {{action}}",
                   ref="A"),
            target(per_min(f"postfix_pickup_messages_total{{{I}}}", "instance"), "submitted locally", ref="B"),
            target(per_min(f"postfix_cleanup_messages_total{{{I}}}", "service"), "{{service}} processed", ref="C")],
           12, y, unit=CPM,
           description="cleanup processes every message, whether from smtpd or pickup.")
y += 8

# --- Operations ------------------------------------------------------------------------
row("Operations", y)
y += 1
timeseries("Warnings and errors",
           [target(per_min(f'postfix_log_messages_total{{{I}, severity!="info"}}', "service, severity"),
                   "{{service}} {{severity}}")],
           0, y, unit=CPM)
timeseries("Process failures",
           [target(per_min(f"postfix_master_process_failures_total{{{I}}}", "program, reason"),
                   "{{program}} {{reason}}")],
           12, y, unit=CPM, description="exit: process ended with an error; signal: crashed; throttling: "
                                        "master delays starting the service after repeated failures.")
y += 8
timeseries("Log lines of the exporter",
           [target(per_min(f"postfix_exporter_log_lines_total{{{I}}}", "result"), "{{result}}")],
           0, y, unit=CPM,
           description="matched: turned into metrics; ignored: known, without a metric of its own; unmatched: "
                       "unrecognised; foreign: other programs; invalid: not syslog format, or part of an overlong line.")
timeseries("Message size (p95)",
           [target(quantile(0.95, "postfix_qmgr_message_size_bytes", "instance"), "size p95", ref="A")],
           12, y, unit="bytes", description="Size of messages on entering the active queue.")
y += 8

# --- Exporter --------------------------------------------------------------------------
row("Exporter", y)
y += 1
stat("Version", f"max by (version) (postfix_exporter_build_info{{{I}}})", 0, y, w=4, legend="{{version}}",
     text_mode="name")
stat("Uptime", "time() - max(" + scoped("process_start_time_seconds") + ")", 4, y, w=4, unit="s",
     no_value="no scrape")
stat("Last log line", f"time() - max(postfix_exporter_last_log_line_timestamp_seconds{{{I}}} > 0)", 8, y,
     w=4, unit="s", th=thresholds(("green", None), ("orange", 1800), ("red", 3600)), no_value="none yet",
     description="Age of the last Postfix line read. Red from 1 hour: that is where PostfixNoLogLines fires "
                 "(after 10 minutes). On a quiet server this can be normal.")
stat("Log source restarts", total(f"postfix_exporter_log_source_restarts_total{{{I}}}"), 12, y, w=4,
     decimals=0, th=thresholds(("text", None), ("orange", 1)),
     description="How often the log or journalctl failed and was restarted in the time range.")
stat("showq read", f"max(postfix_showq_scrape_duration_seconds{{{I}}})", 16, y, w=4, unit="s",
     th=thresholds(("green", None), ("orange", 2), ("red", 8)),
     description="Time taken to read the queue from showq. Red close to showq.timeout (default 10s): beyond "
                 "it postfix_showq_up is 0, and with a large queue every scrape takes that long.")
stat("Scrape", f"min(up{{{I}}} and on (job, instance) postfix_exporter_build_info{{{I}}})", 20, y, w=4,
     mappings=UP_MAP, th=OK_TH, no_value="no scrape")
y += 4
timeseries("CPU", [target(scoped("rate(process_cpu_seconds_total[$__rate_interval])"), "{{instance}}")],
           0, y, w=8, unit="percentunit", description="1 = one core.")
timeseries("Memory", [target(scoped("process_resident_memory_bytes"), "{{instance}}")],
           8, y, w=8, unit="bytes",
           description="If it grows without end, the time series are growing: see the label limits in the README.")
timeseries("showq read", [target(f"postfix_showq_scrape_duration_seconds{{{I}}}", "{{instance}}")],
           16, y, w=8, unit="s")
y += 8


def var_query(name, label, query, include_all=True):
    return {"type": "query", "name": name, "label": label, "datasource": DS,
            "query": {"query": query, "refId": f"var-{name}", "qryType": 1},
            "definition": query, "refresh": 2, "multi": True, "includeAll": include_all,
            "allValue": ".*", "current": {"text": ["All"], "value": ["$__all"]}, "sort": 1}


dashboard = {
    "uid": "postfix-exporter",
    "title": "Postfix",
    "description": "Queue, delivery, inbound, TLS, postscreen and operations (postfix_exporter).",
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
        {"type": "datasource", "name": "datasource", "label": "Data source", "query": "prometheus",
         "current": {}, "hide": 0, "refresh": 1, "regex": ""},
        var_query("instance", "Server", "label_values(postfix_exporter_build_info, instance)"),
    ]},
    "panels": panels,
}

json.dump(dashboard, open(sys.argv[1], "w"), indent=2, ensure_ascii=False)
open(sys.argv[1], "a").write("\n")
print(f"{len(panels)} panels")
