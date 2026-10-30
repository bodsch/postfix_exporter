package collector

import (
	"bytes"
	"context"
	"flag"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/prometheus/common/expfmt"

	"bodsch.me/postfix_exporter/internal/config"
	"bodsch.me/postfix_exporter/internal/logsource"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// replayTestLog runs testdata/mail.log through a collector configured by
// testdata/config.yml.
func replayTestLog(t *testing.T) *Collector {
	t.Helper()
	cfg, err := config.Load(filepath.Join("testdata", "config.yml"))
	if err != nil {
		t.Fatal(err)
	}
	c := New(cfg, discardLogger())
	c.now = func() time.Time { return time.Unix(1700000000, 0) }
	src := &logsource.File{Path: filepath.Join("testdata", "mail.log"), Replay: true}
	if err := c.Replay(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestLogLineResults(t *testing.T) {
	c := replayTestLog(t)
	want := map[string]float64{
		resultInvalid:   4,
		resultForeign:   2,
		resultUnmatched: 2,
		resultIgnored:   10,
		resultMatched:   72,
	}
	for result, n := range want {
		if got := testutil.ToFloat64(c.m.logLines.WithLabelValues(result)); got != n {
			t.Errorf("log lines %s = %v, want %v", result, got, n)
		}
	}
}

func TestMetrics(t *testing.T) {
	c := replayTestLog(t)
	tests := []struct {
		name string
		c    prometheus.Collector
		want float64
	}{
		{"smtpd connects", c.m.smtpdConnects.WithLabelValues("smtpd"), 3},
		{"submission connects", c.m.smtpdConnects.WithLabelValues("submission/smtpd"), 1},
		{"rcpt failed", c.m.smtpdCommands.WithLabelValues("smtpd", "rcpt", "failed"), 5},
		{"rcpt ok", c.m.smtpdCommands.WithLabelValues("smtpd", "rcpt", "ok"), 2},
		{"auth failed", c.m.smtpdCommands.WithLabelValues("submission/smtpd", "auth", "failed"), 1},
		{"accepted unauthenticated", c.m.smtpdAccepted.WithLabelValues("smtpd", "no"), 1},
		{"accepted authenticated", c.m.smtpdAccepted.WithLabelValues("submission/smtpd", "yes"), 1},
		{"reject relay", c.m.smtpdRejects.WithLabelValues("smtpd", "RCPT", "554", "relay_denied"), 1},
		{"reject unknown", c.m.smtpdRejects.WithLabelValues("smtpd", "RCPT", "550", "unknown_recipient"), 1},
		{"reject client", c.m.smtpdRejects.WithLabelValues("smtpd", "RCPT", "450", "client"), 1},
		{"reject dnsbl", c.m.smtpdRejects.WithLabelValues("smtpd", "RCPT", "554", "dnsbl"), 1},
		{"reject warning", c.m.smtpdRejectWarnings.WithLabelValues("smtpd", "RCPT", "helo"), 1},
		{"reject reply dnsbl", c.m.smtpdRejectReplies.WithLabelValues("smtpd", "RCPT", "554", "5.7.1", "dnsbl bl.spamcop.net"), 1},
		{"reject reply relay", c.m.smtpdRejectReplies.WithLabelValues("smtpd", "RCPT", "554", "5.7.1", "Relay access denied"), 1},
		{"hold", c.m.smtpdAccessActions.WithLabelValues("smtpd", "hold", "RCPT"), 1},
		{"milter smtpd", c.m.milterActions.WithLabelValues("smtpd", "reject"), 1},
		{"milter cleanup", c.m.milterActions.WithLabelValues("cleanup", "reject"), 1},
		{"sasl", c.m.smtpdSASLFailures.WithLabelValues("submission/smtpd", "LOGIN"), 1},
		{"tls in", c.m.smtpdTLS.WithLabelValues("submission/smtpd", "Trusted", "TLSv1.2", "ECDHE-RSA-AES256-GCM-SHA384"), 1},
		{"tls error", c.m.smtpdTLSErrors.WithLabelValues("smtpd"), 1},
		{"rdns", c.m.smtpdReverseDNS.WithLabelValues("smtpd", "not_resolve"), 1},
		{"limit", c.m.smtpdLimits.WithLabelValues("smtpd", "connection_rate"), 1},
		{"aborted lost", c.m.smtpdAborted.WithLabelValues("smtpd", "lost", "RCPT"), 1},
		{"aborted timeout", c.m.smtpdAborted.WithLabelValues("smtpd", "timeout", "DATA"), 1},
		{"aborted errors", c.m.smtpdAborted.WithLabelValues("smtpd", "too_many_errors", "AUTH"), 1},
		{"postscreen connects", c.m.postscreenConnects, 3},
		{"postscreen pass new", c.m.postscreenPasses.WithLabelValues("new"), 1},
		{"postscreen pregreet", c.m.postscreenTests.WithLabelValues("pregreet"), 1},
		{"postscreen non-smtp", c.m.postscreenTests.WithLabelValues("non_smtp_command"), 1},
		{"postscreen reject rcpt", c.m.postscreenRejects.WithLabelValues("RCPT"), 1},
		{"postscreen denylist", c.m.postscreenLists.WithLabelValues("denylist"), 1},
		{"postscreen allowlist", c.m.postscreenLists.WithLabelValues("allowlist"), 1},
		{"postscreen veto", c.m.postscreenLists.WithLabelValues("allowlist_veto"), 1},
		{"cleanup messages", c.m.cleanupMessages.WithLabelValues("cleanup"), 1},
		{"cleanup header reject", c.m.cleanupChecks.WithLabelValues("cleanup", "header", "reject"), 1},
		{"cleanup body warning", c.m.cleanupChecks.WithLabelValues("cleanup", "body", "warning"), 1},
		{"pickup", c.m.pickupMessages, 1},
		{"qmgr removed", c.m.qmgrRemoved, 1},
		{"qmgr expired", c.m.qmgrExpired.WithLabelValues("expired"), 1},
		{"lmtp sent", c.m.deliveryStatus.WithLabelValues("lmtp", "sent", "2.0.0"), 1},
		{"lmtp bounced", c.m.deliveryStatus.WithLabelValues("lmtp", "bounced", "5.2.2"), 1},
		{"smtp deferred", c.m.deliveryStatus.WithLabelValues("smtp", "deferred", "4.4.1"), 1},
		{"relay/smtp sent", c.m.deliveryStatus.WithLabelValues("relay/smtp", "sent", "2.0.0"), 1},
		{"encrypt sent", c.m.deliveryStatus.WithLabelValues("encrypt", "sent", "2.0.0"), 1},
		{"local sent", c.m.deliveryStatus.WithLabelValues("local", "sent", "2.0.0"), 1},
		{"virtual sent", c.m.deliveryStatus.WithLabelValues("virtual", "sent", "2.0.0"), 1},
		{"pipe sent", c.m.deliveryStatus.WithLabelValues("pipe", "sent", "2.0.0"), 1},
		{"error bounced", c.m.deliveryStatus.WithLabelValues("error", "bounced", "5.0.0"), 1},
		{"qmgr deferred", c.m.deliveryStatus.WithLabelValues("qmgr", "deferred", "4.4.1"), 1},
		{"reply mailbox full", c.m.deliveryReplies.WithLabelValues("lmtp", "bounced", "552", "5.2.2", "mailbox_full"), 1},
		{"reply user unknown", c.m.deliveryReplies.WithLabelValues("smtp", "bounced", "550", "5.1.1", "user_unknown"), 1},
		{"reply sent", c.m.deliveryReplies.WithLabelValues("smtp", "sent", "250", "2.0.0", "ok"), 1},
		{"smtp reply greylisting", c.m.smtpReplies.WithLabelValues("smtp", "450", "4.2.0", "greylisting"), 1},
		{"connect timeout", c.m.smtpConnectFailures.WithLabelValues("smtp", "timeout"), 1},
		{"connect refused", c.m.smtpConnectFailures.WithLabelValues("smtp", "refused"), 1},
		{"tls out", c.m.smtpTLS.WithLabelValues("smtp", "Verified", "TLSv1.3", "TLS_AES_256_GCM_SHA384"), 1},
		{"bounce ndr", c.m.bounceNotifications.WithLabelValues("sender", "non_delivery"), 1},
		{"bounce delay", c.m.bounceNotifications.WithLabelValues("sender", "delay"), 1},
		{"master exit", c.m.masterProcessFailures.WithLabelValues("smtpd", "exit"), 1},
		{"master throttling", c.m.masterProcessFailures.WithLabelValues("smtpd", "throttling"), 1},
		{"fatal", c.m.logMessages.WithLabelValues("smtpd", "fatal"), 1},
		{"warnings smtpd", c.m.logMessages.WithLabelValues("smtpd", "warning"), 5},
	}
	for _, tt := range tests {
		if got := testutil.ToFloat64(tt.c); got != tt.want {
			t.Errorf("%s = %v, want %v", tt.name, got, tt.want)
		}
	}

	if n := testutil.CollectAndCount(c.m.deliveryStageDelay); n != 28 {
		t.Errorf("stage delay series = %d, want 28 (4 stages for each of the 7 services with a sent status)", n)
	}
}

// TestGolden compares the complete output with testdata/metrics.golden,
// to catch unintended changes of names, labels and help texts.
// Run "go test ./internal/collector -update" after intended ones.
func TestGolden(t *testing.T) {
	c := replayTestLog(t)
	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(c)
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	enc := expfmt.NewEncoder(&buf, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, mf := range mfs {
		if err := enc.Encode(mf); err != nil {
			t.Fatal(err)
		}
	}

	golden := filepath.Join("testdata", "metrics.golden")
	if *update {
		if err := os.WriteFile(golden, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != string(want) {
		gotLines, wantLines := strings.Split(got, "\n"), strings.Split(string(want), "\n")
		for i := 0; i < len(gotLines) || i < len(wantLines); i++ {
			var g, w string
			if i < len(gotLines) {
				g = gotLines[i]
			}
			if i < len(wantLines) {
				w = wantLines[i]
			}
			if g != w {
				t.Fatalf("output differs from %s at line %d:\n got: %s\nwant: %s\n(run with -update after intended changes)", golden, i+1, g, w)
			}
		}
	}
}

func TestReplyLimit(t *testing.T) {
	cfg, err := config.Parse([]byte(`
replies:
  max_texts: 2
  reject:
    - regexp: '(.+)'
      text: '$1'
`))
	if err != nil {
		t.Fatal(err)
	}
	c := New(cfg, discardLogger())
	for _, text := range []string{"a", "b", "c", "a", "d"} {
		c.Handle(logsource.Record{Program: "postfix", Service: "smtpd", Severity: "info",
			Text: "NOQUEUE: reject: RCPT from x[192.0.2.1]: 554 5.7.1 " + text + "; from=<a> to=<b> proto=ESMTP helo=<x>"}, nil)
	}
	for text, want := range map[string]float64{"a": 2, "b": 1, "other": 2} {
		if got := testutil.ToFloat64(c.m.smtpdRejectReplies.WithLabelValues("smtpd", "RCPT", "554", "5.7.1", text)); got != want {
			t.Errorf("text %q = %v, want %v", text, got, want)
		}
	}
}

func TestParseReply(t *testing.T) {
	tests := []struct {
		in, dsn string
		want    reply
	}{
		{"550 5.1.1 No such user", "", reply{"550", "5.1.1", "No such user"}},
		{"550-5.1.1 No such user", "", reply{"550", "5.1.1", "No such user"}},
		{"550 #5.1.1 No such user", "", reply{"550", "5.1.1", "No such user"}},
		{"554 <x@example.net>: Relay access denied", "", reply{"554", "", "Relay access denied"}},
		{"450 Greylisted", "4.7.1", reply{"450", "4.7.1", "Greylisted"}},
		{"delivered to mailbox", "2.0.0", reply{"", "2.0.0", "delivered to mailbox"}},
	}
	for _, tt := range tests {
		if got := parseReply(tt.in, tt.dsn); got != tt.want {
			t.Errorf("parseReply(%q) = %+v, want %+v", tt.in, got, tt.want)
		}
	}
}
