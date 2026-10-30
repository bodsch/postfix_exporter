package collector

import (
	"github.com/prometheus/client_golang/prometheus"

	"bodsch.me/postfix_exporter/internal/logsource"
)

const ns = "postfix"

// Results of processing a log line, the "result" label of
// postfix_exporter_log_lines_total.
const (
	// resultMatched: a parser turned the line into a metric.
	resultMatched = "matched"
	// resultIgnored: a known line without a metric of its own, e.g.
	// statistics, or a warning only counted by severity.
	resultIgnored = "ignored"
	// resultUnmatched: a Postfix line no parser knows. A rising rate
	// points to log formats the exporter should learn.
	resultUnmatched = "unmatched"
	// resultForeign: a line of another program.
	resultForeign = "foreign"
	// resultInvalid: not a syslog line at all.
	resultInvalid = "invalid"
)

var results = []string{resultMatched, resultIgnored, resultUnmatched, resultForeign, resultInvalid}

var (
	// Delivery takes from milliseconds (local) to days (deferred mail
	// up to maximal_queue_lifetime, 5d).
	delayBuckets = []float64{0.1, 0.5, 1, 2.5, 5, 10, 30, 60, 300, 900, 3600, 4 * 3600, 86400, 5 * 86400}
	// The stages of a successful delivery are mostly far below a second.
	stageBuckets = []float64{0.001, 0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 300}
)

// metrics holds all metrics derived from log lines.
type metrics struct {
	logLines       *prometheus.CounterVec
	logMessages    *prometheus.CounterVec
	sourceUp       prometheus.Gauge
	lastLine       prometheus.Gauge
	sourceRestarts prometheus.Counter

	smtpdConnects         *prometheus.CounterVec
	smtpdDisconnects      *prometheus.CounterVec
	smtpdCommands         *prometheus.CounterVec
	smtpdAborted          *prometheus.CounterVec
	smtpdAccepted         *prometheus.CounterVec
	smtpdRejects          *prometheus.CounterVec
	smtpdRejectWarnings   *prometheus.CounterVec
	smtpdAccessActions    *prometheus.CounterVec
	smtpdSASLFailures     *prometheus.CounterVec
	smtpdTLS              *prometheus.CounterVec
	smtpdTLSErrors        *prometheus.CounterVec
	smtpdReverseDNS       *prometheus.CounterVec
	smtpdLimits           *prometheus.CounterVec
	smtpdRejectReplies    *prometheus.CounterVec
	milterActions         *prometheus.CounterVec
	postscreenConnects    prometheus.Counter
	postscreenPasses      *prometheus.CounterVec
	postscreenDNSBLRank   prometheus.Histogram
	postscreenTests       *prometheus.CounterVec
	postscreenHangups     prometheus.Counter
	postscreenRejects     *prometheus.CounterVec
	postscreenLists       *prometheus.CounterVec
	cleanupMessages       *prometheus.CounterVec
	cleanupChecks         *prometheus.CounterVec
	pickupMessages        prometheus.Counter
	qmgrRecipients        prometheus.Histogram
	qmgrSize              prometheus.Histogram
	qmgrRemoved           prometheus.Counter
	qmgrExpired           *prometheus.CounterVec
	deliveryStatus        *prometheus.CounterVec
	deliveryDelay         *prometheus.HistogramVec
	deliveryStageDelay    *prometheus.HistogramVec
	deliveryReplies       *prometheus.CounterVec
	smtpConnectFailures   *prometheus.CounterVec
	smtpTLS               *prometheus.CounterVec
	smtpReplies           *prometheus.CounterVec
	bounceNotifications   *prometheus.CounterVec
	masterProcessFailures *prometheus.CounterVec
}

func counter(name, help string) prometheus.Counter {
	return prometheus.NewCounter(prometheus.CounterOpts{Namespace: ns, Name: name, Help: help})
}

func counterVec(name, help string, labels ...string) *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: ns, Name: name, Help: help}, labels)
}

func newMetrics() *metrics {
	m := &metrics{
		logLines: counterVec("exporter_log_lines_total",
			"Log lines read, by how they were processed (matched, ignored, unmatched, foreign, invalid).", "result"),
		logMessages: counterVec("log_messages_total",
			"Postfix log messages by service and severity.", "service", "severity"),
		sourceUp: prometheus.NewGauge(prometheus.GaugeOpts{Namespace: ns, Name: "exporter_log_source_up",
			Help: "Whether the log source is being read (1) or failed and is retried (0)."}),
		lastLine: prometheus.NewGauge(prometheus.GaugeOpts{Namespace: ns, Name: "exporter_last_log_line_timestamp_seconds",
			Help: "Time the last Postfix log line was read; 0 before the first one."}),
		sourceRestarts: counter("exporter_log_source_restarts_total",
			"Times the log source failed and was restarted."),

		smtpdConnects: counterVec("smtpd_connections_total",
			"Connections to smtpd.", "service"),
		smtpdDisconnects: counterVec("smtpd_disconnections_total",
			"Regular disconnections from smtpd (with command statistics).", "service"),
		smtpdCommands: counterVec("smtpd_commands_total",
			"SMTP commands of finished smtpd sessions, by result (from the disconnect statistics).", "service", "command", "result"),
		smtpdAborted: counterVec("smtpd_aborted_connections_total",
			"smtpd sessions ended by lost connection, timeout or too many errors, by the last command.", "service", "reason", "after"),
		smtpdAccepted: counterVec("smtpd_messages_accepted_total",
			"Messages accepted by smtpd (queue file created), by whether the client authenticated.", "service", "authenticated"),
		smtpdRejects: counterVec("smtpd_rejects_total",
			"Rejects by smtpd restrictions, by SMTP command, reply code and reason class.", "service", "command", "code", "reason"),
		smtpdRejectWarnings: counterVec("smtpd_reject_warnings_total",
			"Rejects that were only logged (warn_if_reject), by SMTP command and reason class.", "service", "command", "reason"),
		smtpdAccessActions: counterVec("smtpd_access_actions_total",
			"Non-reject access table actions taken by smtpd (discard, hold, filter, redirect, warn, ...).", "service", "action", "command"),
		smtpdSASLFailures: counterVec("smtpd_sasl_authentication_failures_total",
			"Failed SASL authentications, by mechanism.", "service", "method"),
		smtpdTLS: counterVec("smtpd_tls_connections_total",
			"Incoming TLS connections, by certificate trust, protocol and cipher.", "service", "trust", "protocol", "cipher"),
		smtpdTLSErrors: counterVec("smtpd_tls_errors_total",
			"Failed incoming TLS handshakes.", "service"),
		smtpdReverseDNS: counterVec("smtpd_reverse_dns_failures_total",
			"Clients whose reverse DNS does not match (not_resolve, not_listed, verification_failed).", "service", "type"),
		smtpdLimits: counterVec("smtpd_limits_exceeded_total",
			"Client limits enforced by anvil (connection_rate, connection_concurrency, message_rate, ...).", "service", "limit"),
		smtpdRejectReplies: counterVec("smtpd_reject_replies_total",
			"NOQUEUE rejects classified by replies.reject rules.", "service", "command", "code", "enhanced_code", "text"),
		milterActions: counterVec("milter_actions_total",
			"Actions requested by milters (reject, discard, hold, ...).", "service", "action"),

		postscreenConnects: counter("postscreen_connections_total",
			"Connections handled by postscreen."),
		postscreenPasses: counterVec("postscreen_passes_total",
			"Clients passed to smtpd by postscreen (new: after tests, old: cached).", "type"),
		postscreenDNSBLRank: prometheus.NewHistogram(prometheus.HistogramOpts{Namespace: ns, Name: "postscreen_dnsbl_rank",
			Help:    "Combined DNSBL score of clients listed by at least one DNSBL.",
			Buckets: []float64{1, 2, 3, 4, 5, 6, 8, 10, 15, 20}}),
		postscreenTests: counterVec("postscreen_tests_failed_total",
			"Failed postscreen tests (PREGREET, NON-SMTP COMMAND, BARE NEWLINE, ...).", "test"),
		postscreenHangups: counter("postscreen_hangups_total",
			"Clients that hung up during postscreen tests."),
		postscreenRejects: counterVec("postscreen_rejects_total",
			"Rejects by postscreen, by SMTP stage (CONNECT, RCPT).", "command"),
		postscreenLists: counterVec("postscreen_access_list_matches_total",
			"Clients matched by postscreen_access_list or the DNSWL (allowlist, denylist, allowlist_veto).", "list"),

		cleanupMessages: counterVec("cleanup_messages_total",
			"Messages processed by cleanup.", "service"),
		cleanupChecks: counterVec("cleanup_check_actions_total",
			"Actions of header_checks and body_checks (reject, discard, hold, warning, ...).", "service", "check", "action"),
		pickupMessages: counter("pickup_messages_total",
			"Messages submitted locally (sendmail, postdrop) and picked up."),

		qmgrRecipients: prometheus.NewHistogram(prometheus.HistogramOpts{Namespace: ns, Name: "qmgr_message_recipients",
			Help:    "Recipients per message entering the active queue.",
			Buckets: []float64{1, 2, 5, 10, 20, 50, 100, 500, 1000}}),
		qmgrSize: prometheus.NewHistogram(prometheus.HistogramOpts{Namespace: ns, Name: "qmgr_message_size_bytes",
			Help:    "Size of messages entering the active queue.",
			Buckets: []float64{1e3, 1e4, 1e5, 1e6, 5e6, 1e7, 2.5e7, 5e7, 1e8}}),
		qmgrRemoved: counter("qmgr_messages_removed_total",
			"Messages removed from the queue after delivery, bounce or expiry."),
		qmgrExpired: counterVec("qmgr_messages_expired_total",
			"Messages returned to the sender after maximal_queue_lifetime (expired) or postsuper -e (force-expired).", "type"),

		deliveryStatus: counterVec("delivery_status_total",
			"Delivery attempts per recipient by delivery agent service, status and DSN code.", "service", "status", "dsn"),
		deliveryDelay: prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: ns, Name: "delivery_delay_seconds",
			Help:    "Time from message arrival to the delivery attempt (delay=), by service and status.",
			Buckets: delayBuckets}, []string{"service", "status"}),
		deliveryStageDelay: prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: ns, Name: "delivery_stage_delay_seconds",
			Help:    "Time spent per stage of successful deliveries (delays=a/b/c/d): before_queue_manager, in_queue_manager, connection_setup, transmission.",
			Buckets: stageBuckets}, []string{"service", "stage"}),
		deliveryReplies: counterVec("delivery_replies_total",
			"Delivery replies classified by replies.delivery rules.", "service", "status", "code", "enhanced_code", "text"),

		smtpConnectFailures: counterVec("smtp_connection_failures_total",
			"Failed outgoing connections, by reason (timeout, refused, no_route, unreachable, other).", "service", "reason"),
		smtpTLS: counterVec("smtp_tls_connections_total",
			"Outgoing TLS connections, by certificate trust, protocol and cipher.", "service", "trust", "protocol", "cipher"),
		smtpReplies: counterVec("smtp_replies_total",
			"Remote server replies without delivery status classified by replies.smtp rules.", "service", "code", "enhanced_code", "text"),

		bounceNotifications: counterVec("bounce_notifications_total",
			"Notifications sent by the bounce service, by recipient (sender, postmaster) and type (non_delivery, delivery_status, delay).", "recipient", "type"),
		masterProcessFailures: counterVec("master_process_failures_total",
			"Postfix daemon processes that exited with an error or were killed, by program.", "program", "reason"),
	}

	// Initialize the label values that are known up front, so that
	// rate() and increase() work from the first increment on.
	for _, r := range results {
		m.logLines.WithLabelValues(r)
	}
	for _, t := range []string{"new", "old"} {
		m.postscreenPasses.WithLabelValues(t)
	}
	for _, t := range []string{"expired", "force-expired"} {
		m.qmgrExpired.WithLabelValues(t)
	}
	return m
}

func (m *metrics) collectors() []prometheus.Collector {
	return []prometheus.Collector{
		m.logLines, m.logMessages, m.sourceUp, m.lastLine, m.sourceRestarts,
		m.smtpdConnects, m.smtpdDisconnects, m.smtpdCommands, m.smtpdAborted, m.smtpdAccepted,
		m.smtpdRejects, m.smtpdRejectWarnings, m.smtpdAccessActions, m.smtpdSASLFailures,
		m.smtpdTLS, m.smtpdTLSErrors, m.smtpdReverseDNS, m.smtpdLimits, m.smtpdRejectReplies,
		m.milterActions,
		m.postscreenConnects, m.postscreenPasses, m.postscreenDNSBLRank, m.postscreenTests,
		m.postscreenHangups, m.postscreenRejects, m.postscreenLists,
		m.cleanupMessages, m.cleanupChecks, m.pickupMessages,
		m.qmgrRecipients, m.qmgrSize, m.qmgrRemoved, m.qmgrExpired,
		m.deliveryStatus, m.deliveryDelay, m.deliveryStageDelay, m.deliveryReplies,
		m.smtpConnectFailures, m.smtpTLS, m.smtpReplies,
		m.bounceNotifications, m.masterProcessFailures,
	}
}

// initService creates the series of a service that is seen for the first
// time, so that rate() works from its first event on.
func (m *metrics) initService(service, typ string) {
	for _, s := range logsource.Severities {
		m.logMessages.WithLabelValues(service, s)
	}
	switch typ {
	case "smtpd":
		m.smtpdConnects.WithLabelValues(service)
		m.smtpdDisconnects.WithLabelValues(service)
		m.smtpdAccepted.WithLabelValues(service, "yes")
		m.smtpdAccepted.WithLabelValues(service, "no")
	case "cleanup":
		m.cleanupMessages.WithLabelValues(service)
	}
}
