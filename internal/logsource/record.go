// Package logsource reads Postfix log records from a syslog file or the
// systemd journal.
package logsource

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

// Severities of a Postfix log message. Postfix writes them as a prefix of
// the message text, e.g. "warning: ...", in syslog files and the journal
// alike, so they are taken from the text and not from the syslog priority.
const (
	SeverityInfo    = "info"
	SeverityWarning = "warning"
	SeverityError   = "error"
	SeverityFatal   = "fatal"
	SeverityPanic   = "panic"
)

// Severities lists all severities, for initializing metrics.
var Severities = []string{SeverityInfo, SeverityWarning, SeverityError, SeverityFatal, SeverityPanic}

// Record is one log message.
type Record struct {
	// Program is the syslog tag up to the first slash, "postfix" for
	// Postfix with the default syslog_name.
	Program string
	// Service is the rest of the tag, the master.cf service name such
	// as "smtpd" or "submission/smtpd". Empty for tags without a slash.
	Service string
	PID     int64
	// Severity is one of the Severity constants.
	Severity string
	// Text is the message without the severity prefix.
	Text string
}

// newRecord builds a record from a syslog tag ("postfix/smtpd") and the
// message, which may start with a severity prefix.
func newRecord(tag string, pid int64, msg string) Record {
	r := Record{PID: pid, Severity: SeverityInfo, Text: msg}
	r.Program, r.Service, _ = strings.Cut(tag, "/")
	if prefix, rest, ok := strings.Cut(msg, ": "); ok {
		switch prefix {
		case SeverityWarning, SeverityError, SeverityFatal, SeverityPanic:
			r.Severity, r.Text = prefix, rest
		}
	}
	return r
}

// The line format is documented in the Postfix MAILLOG_README and is the
// same for syslog files and Postfix's own maillog_file:
//
//	Jan  2 15:04:05 host postfix/smtpd[123]: text
//	2006-01-02T15:04:05.000000+00:00 host postfix/smtpd[123]: text
//
// The parser follows the one of sergeymakinen/postfix_exporter
// (BSD-3-Clause, see NOTICE).

var errMalformed = errors.New("malformed log line")

const bsdTime = "Jan _2 15:04:05"

// ParseLine parses a syslog line. Only the tag, PID and message are kept;
// the timestamp is validated but not used.
func ParseLine(line string) (Record, error) {
	s := line
	field := func() (string, bool) {
		s = strings.TrimLeft(s, " ")
		f, rest, ok := strings.Cut(s, " ")
		s = rest
		return f, ok && f != ""
	}

	ts, ok := field()
	if !ok {
		return Record{}, errMalformed
	}
	if strings.Contains(ts, "T") {
		if _, err := time.Parse(time.RFC3339Nano, ts); err != nil {
			return Record{}, errMalformed
		}
	} else {
		day, ok1 := field()
		clock, ok2 := field()
		if !ok1 || !ok2 {
			return Record{}, errMalformed
		}
		if _, err := time.Parse(bsdTime, ts+" "+day+" "+clock); err != nil {
			return Record{}, errMalformed
		}
	}
	if _, ok := field(); !ok { // hostname
		return Record{}, errMalformed
	}

	tag, rest, ok := strings.Cut(s, "[")
	if !ok || tag == "" || strings.Contains(tag, " ") {
		return Record{}, errMalformed
	}
	pidStr, msg, ok := strings.Cut(rest, "]: ")
	if !ok {
		return Record{}, errMalformed
	}
	pid, err := strconv.ParseInt(pidStr, 10, 64)
	if err != nil {
		return Record{}, errMalformed
	}
	return newRecord(tag, pid, msg), nil
}
