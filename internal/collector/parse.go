package collector

import (
	"path"
	"regexp"
	"strconv"
	"strings"

	"bodsch.me/postfix_exporter/internal/logsource"
)

// The patterns combine those of Hsn723/postfix_exporter (Apache-2.0) and
// sergeymakinen/postfix_exporter (BSD-3-Clause), see NOTICE, with formats
// taken from the Postfix sources. They match the message text without the
// severity prefix.

const qid = `[0-9A-Za-z]+` // queue ID, short or long format

var (
	// smtpd
	reConnect    = regexp.MustCompile(`^connect from \S+$`)
	reDisconnect = regexp.MustCompile(`^disconnect from \S+((?: [a-z]+=\d+(?:/\d+)?)*)$`)
	reAborted    = regexp.MustCompile(`^(?:` + qid + `: )?(lost connection|timeout|too many errors) after ([A-Za-z-]+)`)
	reAccepted   = regexp.MustCompile(`^` + qid + `: client=\S+?(, sasl_method=\S+)?(?:,|$)`)
	// NOQUEUE: reject: RCPT from host[addr]: 554 5.7.1 <x>: Relay access denied; from=<a> to=<b> proto=ESMTP helo=<h>
	// 1A2B3C: milter-reject: END-OF-MESSAGE from host[addr]: 5.7.1 Spam; from=<a> ...
	// 1A2B3C: proxy-reject: END-OF-MESSAGE: 554 5.7.1 Rejected; from=<a> ...
	reAction   = regexp.MustCompile(`^(?:NOQUEUE|` + qid + `): ([a-z]+(?:[-_][a-z]+)?): ([A-Z][A-Z-]*)(?: from \S+?)?: (.*)$`)
	reSASLFail = regexp.MustCompile(`^\S+: SASL (\S+) authentication failed`)
	reTLSIn    = regexp.MustCompile(`^(\S+) TLS connection established from \S+: (\S+) with cipher (\S+) `)
	reTLSOut   = regexp.MustCompile(`^(\S+) TLS connection established to \S+: (\S+) with cipher (\S+) `)
	reNotRes   = regexp.MustCompile(`^hostname \S+ does not resolve to address `)
	reNotList  = regexp.MustCompile(`^\S+: address not listed for hostname `)
	reVerify   = regexp.MustCompile(`^\S+: hostname \S+ verification failed`)
	reLimit    = regexp.MustCompile(`^(Connection rate|Connection concurrency|Message delivery request rate|Recipient address rate|New TLS session rate|AUTH command rate) limit exceeded`)

	// postscreen
	rePsPass   = regexp.MustCompile(`^PASS (NEW|OLD) `)
	rePsDNSBL  = regexp.MustCompile(`^DNSBL rank (-?\d+) for `)
	rePsTest   = regexp.MustCompile(`^(PREGREET|NON-SMTP COMMAND|COMMAND PIPELINING|COMMAND TIME LIMIT|COMMAND COUNT LIMIT|COMMAND LENGTH LIMIT|BARE NEWLINE) `)
	rePsReject = regexp.MustCompile(`^NOQUEUE: reject: (CONNECT|RCPT) from `)
	rePsList   = regexp.MustCompile(`^(DENYLISTED|BLACKLISTED|ALLOWLISTED|WHITELISTED|ALLOWLIST VETO|WHITELIST VETO) `)

	// cleanup
	reCheck = regexp.MustCompile(`^` + qid + `: ([a-z]+): (header|body) `)

	// qmgr
	reQmgrInsert = regexp.MustCompile(`^` + qid + `: from=<.*>, size=(\d+), nrcpt=(\d+) `)
	reQmgrExpire = regexp.MustCompile(`^` + qid + `: from=<.*>, status=(expired|force-expired), returned to sender`)

	// delivery agents, and qmgr for deferrals it decides itself
	reStatus = regexp.MustCompile(`, delay=([\d.]+), delays=([\d.]+)/([\d.]+)/([\d.]+)/([\d.]+), dsn=(\d\.\d{1,3}\.\d{1,3}), status=([a-z-]+)(?: \((.*)\))?$`)

	// smtp
	reConnectTo = regexp.MustCompile(`^connect to \S+: (.+)$`)
	reHostSaid  = regexp.MustCompile(`^(?:` + qid + `: )?host \S+ said: (.+) \(in reply to [^)]+\)$`)

	reBounce = regexp.MustCompile(`^` + qid + `: (sender|postmaster) (non-delivery|delivery status|delay) notification: `)

	// master
	reProcFail = regexp.MustCompile(`^process (\S+) pid \d+ (exit status|killed by signal) \d+`)
	reThrottle = regexp.MustCompile(`^(\S+): bad command startup -- throttling`)
)

// ignoredTypes are daemons and tools without metrics of their own.
var ignoredTypes = map[string]bool{
	"anvil": true, "scache": true, "tlsmgr": true, "tlsproxy": true, "verify": true,
	"trivial-rewrite": true, "proxymap": true, "postfix-script": true, "postlog": true,
	"postlogd": true, "postsuper": true, "postqueue": true, "postdrop": true,
	"sendmail": true, "showq": true, "dnsblog": true, "flush": true, "spawn": true,
	"postmap": true, "postalias": true, "postmulti": true, "postconf": true, "oqmgr": true,
}

// ignoredLines are known lines without a metric of their own, so that they
// do not show up as unmatched.
var ignoredLines = map[string]*regexp.Regexp{
	"smtpd": regexp.MustCompile(`^(?:improper command pipelining after |non-SMTP command from |NOQUEUE: client=|` +
		qid + `: (?:client=|replace: |prepend: |info: )|connect to |proxy-accept: )`),
	"postscreen": regexp.MustCompile(`^(?:DISCONNECT |(?:DATA|BDAT) without valid RCPT|cache \S+ full cleanup|` +
		`NOQUEUE: reject: (?:DATA|BDAT|MAIL) )`),
	"cleanup": regexp.MustCompile(`^` + qid + `: (?:resent-message-id=|milter-header-)`),
	"qmgr":    regexp.MustCompile(`^` + qid + `: skipped, still being delivered`),
	"master":  regexp.MustCompile(`^(?:daemon started|reload|terminating on signal|master exit time)`),
	"smtp": regexp.MustCompile(`^(?:` + qid + `: (?:lost connection with |conversation with |enabling PIX |Cannot start TLS|` +
		`host \S+ refused to talk to me|breaking line|SMTPUTF8 is required)|SSL_connect error to |` +
		`(?:Anonymous|Untrusted|Trusted|Verified) TLS connection established to \S+$)`),
}

// parse dispatches a Postfix record by daemon type.
func (c *Collector) parse(typ string, r logsource.Record) string {
	if ignoredTypes[typ] {
		return resultIgnored
	}
	var result string
	switch typ {
	case "smtpd":
		result = c.parseSMTPD(r)
	case "postscreen":
		result = c.parsePostscreen(r)
	case "cleanup":
		result = c.parseCleanup(r)
	case "pickup":
		result = c.parsePickup(r)
	case "qmgr":
		result = c.parseQmgr(r)
	case "bounce":
		result = c.parseBounce(r)
	case "master":
		result = c.parseMaster(r)
	case "smtp", "lmtp":
		result = c.parseSMTP(r)
	case "local", "virtual", "pipe", "error", "retry", "discard":
		result = c.parseDelivery(r)
	default:
		return resultUnmatched
	}
	if result == resultUnmatched {
		if re := ignoredLines[typ]; re != nil && re.MatchString(r.Text) {
			return resultIgnored
		}
		// lmtp shares the smtp client and its ignorable lines.
		if typ == "lmtp" && ignoredLines["smtp"].MatchString(r.Text) {
			return resultIgnored
		}
	}
	return result
}

func (c *Collector) parseSMTPD(r logsource.Record) string {
	m, s, t := c.m, r.Service, r.Text
	switch {
	case reConnect.MatchString(t):
		m.smtpdConnects.WithLabelValues(s).Inc()
	case strings.HasPrefix(t, "disconnect from "):
		mm := reDisconnect.FindStringSubmatch(t)
		if mm == nil {
			return resultUnmatched
		}
		m.smtpdDisconnects.WithLabelValues(s).Inc()
		c.countCommands(s, mm[1])
	case reAborted.MatchString(t):
		mm := reAborted.FindStringSubmatch(t)
		m.smtpdAborted.WithLabelValues(s, abortReason(mm[1]), mm[2]).Inc()
	case reAccepted.MatchString(t):
		mm := reAccepted.FindStringSubmatch(t)
		m.smtpdAccepted.WithLabelValues(s, yesNo(mm[1] != "")).Inc()
	case reAction.MatchString(t):
		return c.parseAction(r, reAction.FindStringSubmatch(t))
	case strings.HasPrefix(t, "SSL_accept error from "):
		m.smtpdTLSErrors.WithLabelValues(s).Inc()
	case reTLSIn.MatchString(t):
		mm := reTLSIn.FindStringSubmatch(t)
		m.smtpdTLS.WithLabelValues(s, mm[1], mm[2], mm[3]).Inc()
	case reSASLFail.MatchString(t):
		m.smtpdSASLFailures.WithLabelValues(s, saslMechanism(reSASLFail.FindStringSubmatch(t)[1])).Inc()
	case reNotRes.MatchString(t):
		m.smtpdReverseDNS.WithLabelValues(s, "not_resolve").Inc()
	case reNotList.MatchString(t):
		m.smtpdReverseDNS.WithLabelValues(s, "not_listed").Inc()
	case reVerify.MatchString(t):
		m.smtpdReverseDNS.WithLabelValues(s, "verification_failed").Inc()
	case reLimit.MatchString(t):
		m.smtpdLimits.WithLabelValues(s, limitName(reLimit.FindStringSubmatch(t)[1])).Inc()
	default:
		return resultUnmatched
	}
	return resultMatched
}

// smtpdCommands are the commands smtpd reports in its disconnect
// statistics; anything else is not counted to keep the label bounded.
var smtpdCommands = map[string]bool{
	"helo": true, "ehlo": true, "starttls": true, "auth": true, "mail": true, "rcpt": true,
	"data": true, "bdat": true, "rset": true, "noop": true, "vrfy": true, "etrn": true,
	"quit": true, "unknown": true, "xclient": true, "xforward": true,
}

// countCommands counts " ehlo=1 auth=0/1 mail=1 rcpt=1 data=1 quit=1
// commands=5/6": n means n successful, ok/total means total-ok failed.
func (c *Collector) countCommands(service, stats string) {
	for _, f := range strings.Fields(stats) {
		name, val, _ := strings.Cut(f, "=")
		if !smtpdCommands[name] {
			continue
		}
		okStr, totalStr, hasTotal := strings.Cut(val, "/")
		ok, err := strconv.ParseFloat(okStr, 64)
		if err != nil {
			continue
		}
		total := ok
		if hasTotal {
			if total, err = strconv.ParseFloat(totalStr, 64); err != nil {
				continue
			}
		}
		c.m.smtpdCommands.WithLabelValues(service, name, "ok").Add(ok)
		if total > ok {
			c.m.smtpdCommands.WithLabelValues(service, name, "failed").Add(total - ok)
		}
	}
}

// parseAction handles the "<queue id>: <action>: <command> from ...: ..."
// lines of smtpd: restriction rejects, access actions and milter actions.
func (c *Collector) parseAction(r logsource.Record, mm []string) string {
	m, s := c.m, r.Service
	action, command, rest := mm[1], mm[2], mm[3]
	if milter, ok := strings.CutPrefix(action, "milter-"); ok {
		m.milterActions.WithLabelValues(s, milter).Inc()
		return resultMatched
	}
	// The envelope is appended after the reply; it is not part of it.
	if i := strings.LastIndex(rest, "; from=<"); i >= 0 {
		rest = rest[:i]
	}
	switch action {
	case "reject":
		reply := parseReply(rest, "")
		reason := rejectReason(reply.Text)
		m.smtpdRejects.WithLabelValues(s, command, c.codes.value(reply.Code), reason).Inc()
		if text, ok := c.rejectReplies.classify(reply, ""); ok {
			m.smtpdRejectReplies.WithLabelValues(s, command, c.codes.value(reply.Code), c.codes.value(reply.EnhancedCode), text).Inc()
		}
	case "reject_warning":
		reply := parseReply(rest, "")
		m.smtpdRejectWarnings.WithLabelValues(s, command, rejectReason(reply.Text)).Inc()
	case "discard", "hold", "filter", "redirect", "warn", "bcc":
		m.smtpdAccessActions.WithLabelValues(s, action, command).Inc()
	case "proxy-reject":
		// A before-queue content filter rejected the message.
		m.milterActions.WithLabelValues(s, "proxy-reject").Inc()
	default:
		return resultUnmatched
	}
	return resultMatched
}

func (c *Collector) parsePostscreen(r logsource.Record) string {
	m, t := c.m, r.Text
	switch {
	case strings.HasPrefix(t, "CONNECT from "):
		m.postscreenConnects.Inc()
	case rePsPass.MatchString(t):
		m.postscreenPasses.WithLabelValues(strings.ToLower(rePsPass.FindStringSubmatch(t)[1])).Inc()
	case rePsDNSBL.MatchString(t):
		rank, _ := strconv.ParseFloat(rePsDNSBL.FindStringSubmatch(t)[1], 64)
		m.postscreenDNSBLRank.Observe(rank)
	case rePsTest.MatchString(t):
		test := rePsTest.FindStringSubmatch(t)[1]
		test = strings.ToLower(strings.NewReplacer(" ", "_", "-", "_").Replace(test))
		m.postscreenTests.WithLabelValues(test).Inc()
	case strings.HasPrefix(t, "HANGUP after "):
		m.postscreenHangups.Inc()
	case rePsReject.MatchString(t):
		m.postscreenRejects.WithLabelValues(rePsReject.FindStringSubmatch(t)[1]).Inc()
	case rePsList.MatchString(t):
		m.postscreenLists.WithLabelValues(listName(rePsList.FindStringSubmatch(t)[1])).Inc()
	default:
		return resultUnmatched
	}
	return resultMatched
}

func (c *Collector) parseCleanup(r logsource.Record) string {
	m, s, t := c.m, r.Service, r.Text
	switch {
	case strings.Contains(t, ": message-id="):
		m.cleanupMessages.WithLabelValues(s).Inc()
	case reCheck.MatchString(t):
		mm := reCheck.FindStringSubmatch(t)
		m.cleanupChecks.WithLabelValues(s, mm[2], mm[1]).Inc()
	case reAction.MatchString(t):
		mm := reAction.FindStringSubmatch(t)
		milter, ok := strings.CutPrefix(mm[1], "milter-")
		if !ok {
			return resultUnmatched
		}
		m.milterActions.WithLabelValues(s, milter).Inc()
	default:
		return resultUnmatched
	}
	return resultMatched
}

func (c *Collector) parsePickup(r logsource.Record) string {
	if strings.Contains(r.Text, ": uid=") {
		c.m.pickupMessages.Inc()
		return resultMatched
	}
	return resultUnmatched
}

func (c *Collector) parseQmgr(r logsource.Record) string {
	m, t := c.m, r.Text
	switch {
	case strings.HasSuffix(t, ": removed"):
		m.qmgrRemoved.Inc()
	case reQmgrInsert.MatchString(t):
		mm := reQmgrInsert.FindStringSubmatch(t)
		size, _ := strconv.ParseFloat(mm[1], 64)
		nrcpt, _ := strconv.ParseFloat(mm[2], 64)
		m.qmgrSize.Observe(size)
		m.qmgrRecipients.Observe(nrcpt)
	case reQmgrExpire.MatchString(t):
		m.qmgrExpired.WithLabelValues(reQmgrExpire.FindStringSubmatch(t)[1]).Inc()
	default:
		// qmgr logs deferrals it decides itself ("delivery temporarily
		// suspended") in the format of the delivery agents.
		return c.parseDelivery(r)
	}
	return resultMatched
}

func (c *Collector) parseBounce(r logsource.Record) string {
	mm := reBounce.FindStringSubmatch(r.Text)
	if mm == nil {
		return resultUnmatched
	}
	typ := strings.ReplaceAll(strings.ReplaceAll(mm[2], "-", "_"), " ", "_")
	c.m.bounceNotifications.WithLabelValues(mm[1], typ).Inc()
	return resultMatched
}

func (c *Collector) parseMaster(r logsource.Record) string {
	t := r.Text
	if mm := reProcFail.FindStringSubmatch(t); mm != nil {
		reason := "exit"
		if mm[2] == "killed by signal" {
			reason = "signal"
		}
		c.m.masterProcessFailures.WithLabelValues(path.Base(mm[1]), reason).Inc()
		return resultMatched
	}
	if mm := reThrottle.FindStringSubmatch(t); mm != nil {
		c.m.masterProcessFailures.WithLabelValues(path.Base(mm[1]), "throttling").Inc()
		return resultMatched
	}
	return resultUnmatched
}

func (c *Collector) parseSMTP(r logsource.Record) string {
	m, s, t := c.m, r.Service, r.Text
	switch {
	case strings.Contains(t, ", status="):
		return c.parseDelivery(r)
	case reConnectTo.MatchString(t):
		m.smtpConnectFailures.WithLabelValues(s, connectFailure(reConnectTo.FindStringSubmatch(t)[1])).Inc()
	case reTLSOut.MatchString(t):
		mm := reTLSOut.FindStringSubmatch(t)
		m.smtpTLS.WithLabelValues(s, mm[1], mm[2], mm[3]).Inc()
	case reHostSaid.MatchString(t):
		reply := parseReply(reHostSaid.FindStringSubmatch(t)[1], "")
		if text, ok := c.smtpReplies.classify(reply, ""); ok {
			m.smtpReplies.WithLabelValues(s, c.codes.value(reply.Code), c.codes.value(reply.EnhancedCode), text).Inc()
		}
	default:
		return resultUnmatched
	}
	return resultMatched
}

// stages are the parts of "delays=a/b/c/d", see the Postfix
// postconf(5) documentation of delay_logging_resolution_limit.
var stages = []string{"before_queue_manager", "in_queue_manager", "connection_setup", "transmission"}

func (c *Collector) parseDelivery(r logsource.Record) string {
	mm := reStatus.FindStringSubmatch(r.Text)
	if mm == nil {
		return resultUnmatched
	}
	m, s := c.m, r.Service
	dsn, status := mm[6], mm[7]
	m.deliveryStatus.WithLabelValues(s, status, c.codes.value(dsn)).Inc()
	if delay, err := strconv.ParseFloat(mm[1], 64); err == nil {
		m.deliveryDelay.WithLabelValues(s, status).Observe(delay)
	}
	if status == "sent" {
		for i, stage := range stages {
			if v, err := strconv.ParseFloat(mm[2+i], 64); err == nil {
				m.deliveryStageDelay.WithLabelValues(s, stage).Observe(v)
			}
		}
	}
	if c.deliveryReplies.enabled() {
		text := mm[8]
		if said := reHostSaidInline.FindStringSubmatch(text); said != nil {
			text = said[1]
		}
		reply := parseReply(text, dsn)
		if label, ok := c.deliveryReplies.classify(reply, status); ok {
			m.deliveryReplies.WithLabelValues(s, status, c.codes.value(reply.Code), c.codes.value(reply.EnhancedCode), label).Inc()
		}
	}
	return resultMatched
}

// reHostSaidInline extracts the reply from the parenthesized part of a
// status line, which has no queue ID prefix: "host x[1.2.3.4] said: 550
// 5.1.1 No such user (in reply to RCPT TO command)".
var reHostSaidInline = regexp.MustCompile(`host \S+ said: (.+) \(in reply to [^)]+\)`)

func abortReason(s string) string {
	switch s {
	case "lost connection":
		return "lost"
	case "too many errors":
		return "too_many_errors"
	}
	return s // timeout
}

func limitName(s string) string {
	switch s {
	case "Connection rate":
		return "connection_rate"
	case "Connection concurrency":
		return "connection_concurrency"
	case "Message delivery request rate":
		return "message_rate"
	case "Recipient address rate":
		return "recipient_rate"
	case "New TLS session rate":
		return "newtls_rate"
	}
	return "auth_rate"
}

func listName(s string) string {
	switch s {
	case "DENYLISTED", "BLACKLISTED":
		return "denylist"
	case "ALLOWLISTED", "WHITELISTED":
		return "allowlist"
	}
	return "allowlist_veto"
}

func connectFailure(s string) string {
	switch {
	case strings.Contains(s, "Connection timed out"):
		return "timeout"
	case strings.Contains(s, "Connection refused"):
		return "refused"
	case strings.Contains(s, "No route to host"):
		return "no_route"
	case strings.Contains(s, "Network is unreachable"):
		return "unreachable"
	}
	return "other"
}

// rejectReasons classify reject texts by the restriction that produced
// them. Postfix builds these texts from fixed phrases, so this works
// without configuration; the order matters, the first match wins.
var rejectReasons = []struct {
	substr, reason string
}{
	{" blocked using ", "dnsbl"},
	{"Relay access denied", "relay_denied"},
	{"User unknown", "unknown_recipient"},
	{"Recipient address rejected: undeliverable address", "unknown_recipient"},
	{"Recipient address rejected", "recipient"},
	{"Sender address rejected", "sender"},
	{"Client host rejected", "client"},
	{"Helo command rejected", "helo"},
	{"Data command rejected", "data"},
	{"Message size exceeds", "size"},
	{"Improper use of SMTP command pipelining", "pipelining"},
	{"too many recipients", "too_many_recipients"},
	{"Must issue a STARTTLS command first", "tls_required"},
	{"Insufficient system storage", "storage"},
	{"Error: ", "protocol"},
}

func rejectReason(text string) string {
	for _, r := range rejectReasons {
		if strings.Contains(text, r.substr) {
			return r.reason
		}
	}
	return "other"
}

// saslMechanisms are the registered SASL mechanisms Postfix can offer
// through Cyrus SASL or Dovecot. smtpd logs the mechanism the client
// sent, so others are counted as "other".
var saslMechanisms = map[string]bool{
	"PLAIN": true, "LOGIN": true, "CRAM-MD5": true, "DIGEST-MD5": true, "NTLM": true,
	"GSSAPI": true, "GS2-KRB5": true, "EXTERNAL": true, "ANONYMOUS": true, "OTP": true,
	"XOAUTH2": true, "OAUTHBEARER": true, "SCRAM-SHA-1": true, "SCRAM-SHA-1-PLUS": true,
	"SCRAM-SHA-256": true, "SCRAM-SHA-256-PLUS": true,
}

func saslMechanism(s string) string {
	s = strings.ToUpper(s)
	if saslMechanisms[s] {
		return s
	}
	return labelOther
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
