package logsource

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestParseLine(t *testing.T) {
	tests := []struct {
		line string
		want Record
	}{
		{"Jan  1 00:00:00 mx postfix/smtpd[123]: connect from x[192.0.2.1]",
			Record{Program: "postfix", Service: "smtpd", PID: 123, Severity: SeverityInfo, Text: "connect from x[192.0.2.1]"}},
		{"Jan 12 00:00:00 mx postfix/submission/smtpd[123]: warning: x: SASL LOGIN authentication failed: y",
			Record{Program: "postfix", Service: "submission/smtpd", PID: 123, Severity: SeverityWarning, Text: "x: SASL LOGIN authentication failed: y"}},
		{"2026-10-29T10:00:00.123456+01:00 mx postfix/qmgr[7]: ABC: removed",
			Record{Program: "postfix", Service: "qmgr", PID: 7, Severity: SeverityInfo, Text: "ABC: removed"}},
		{"Jan  1 00:00:00 mx postfix/master[1]: fatal: bind 0.0.0.0 port 25: Address already in use",
			Record{Program: "postfix", Service: "master", PID: 1, Severity: SeverityFatal, Text: "bind 0.0.0.0 port 25: Address already in use"}},
		// A queue ID is not a severity.
		{"Jan  1 00:00:00 mx postfix/cleanup[2]: ABC: warning: header Subject: x",
			Record{Program: "postfix", Service: "cleanup", PID: 2, Severity: SeverityInfo, Text: "ABC: warning: header Subject: x"}},
		{"Jan  1 00:00:00 mx dovecot[3]: imap-login: Login",
			Record{Program: "dovecot", PID: 3, Severity: SeverityInfo, Text: "imap-login: Login"}},
	}
	for _, tt := range tests {
		got, err := ParseLine(tt.line)
		if err != nil {
			t.Errorf("ParseLine(%q): %v", tt.line, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseLine(%q) = %+v, want %+v", tt.line, got, tt.want)
		}
	}

	for _, line := range []string{
		"", "Jan", "Jan  1 00:00:00", "Jan  1 00:00:00 mx",
		"Jan: 1 00:00:00 mx postfix/smtpd[1]: x",
		"Jan  1 00:00:00 mx postfix/smtpd[abc]: x",
		"Jan  1 00:00:00 mx postfix/smtpd[1",
		"2026-13-01T00:00:00Z mx postfix/smtpd[1]: x",
	} {
		if _, err := ParseLine(line); err == nil {
			t.Errorf("ParseLine(%q): no error", line)
		}
	}
}

func TestParseJournalEntry(t *testing.T) {
	r, ok, err := parseJournalEntry([]byte(`{"MESSAGE":"warning: hostname x does not resolve to address 192.0.2.1","SYSLOG_IDENTIFIER":"postfix/smtpd","SYSLOG_PID":"42","_PID":"42"}`))
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	want := Record{Program: "postfix", Service: "smtpd", PID: 42, Severity: SeverityWarning, Text: "hostname x does not resolve to address 192.0.2.1"}
	if r != want {
		t.Errorf("got %+v, want %+v", r, want)
	}

	// journalctl writes non-UTF-8 values as byte arrays.
	r, ok, err = parseJournalEntry([]byte(`{"MESSAGE":[65,66,255],"SYSLOG_IDENTIFIER":"postfix/qmgr","_PID":"7"}`))
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if r.Text != "AB�" || r.PID != 7 {
		t.Errorf("got %+v", r)
	}

	// Entries without a syslog identifier, e.g. from systemd, are skipped.
	if _, ok, err := parseJournalEntry([]byte(`{"MESSAGE":"Started postfix.service"}`)); ok || err != nil {
		t.Errorf("ok=%v err=%v, want skipped", ok, err)
	}
	if _, _, err := parseJournalEntry([]byte(`{`)); err == nil {
		t.Error("no error for invalid JSON")
	}
}

func TestJournalArgs(t *testing.T) {
	j := &Journal{Units: []string{"postfix.service"}, Directory: "/var/log/journal/x"}
	got := j.args()
	for _, want := range []string{"--output=json", "--follow", "--lines=0", "--unit=postfix.service", "--directory=/var/log/journal/x"} {
		if !slices.Contains(got, want) {
			t.Errorf("args %v lack %s", got, want)
		}
	}
	j = &Journal{Replay: true, Since: "24h"}
	got = j.args()
	if slices.Contains(got, "--follow") || !slices.Contains(got, "--since=-24h") {
		t.Errorf("replay args = %v", got)
	}
}

// TestJournalRun uses a fake journalctl that prints two entries.
func TestJournalRun(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "journalctl")
	script := "#!/bin/sh\n" +
		`echo '{"MESSAGE":"connect from x[192.0.2.1]","SYSLOG_IDENTIFIER":"postfix/smtpd","_PID":"1"}'` + "\n" +
		`echo '{"MESSAGE":"Started.","_PID":"1"}'` + "\n" +
		`echo '{"MESSAGE":"ABC: removed","SYSLOG_IDENTIFIER":"postfix/qmgr","_PID":"2"}'` + "\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	var got []string
	j := &Journal{Command: fake, Replay: true}
	err := j.Run(context.Background(), func(r Record, err error) {
		if err != nil {
			t.Error(err)
			return
		}
		got = append(got, r.Service+": "+r.Text)
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"smtpd: connect from x[192.0.2.1]", "qmgr: ABC: removed"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}

	// Following, an exiting journalctl is an error, so that it is restarted.
	j.Replay = false
	if err := j.Run(context.Background(), func(Record, error) {}); err == nil {
		t.Error("no error when journalctl exits while following")
	}
}

// TestFileFollow checks that the file source starts at the end and
// follows appended lines.
func TestFileFollow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mail.log")
	if err := os.WriteFile(path, []byte("Jan  1 00:00:00 mx postfix/smtpd[1]: old line\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	lines := make(chan string, 10)
	done := make(chan error, 1)
	f := &File{Path: path, Poll: true}
	go func() {
		done <- f.Run(ctx, func(r Record, err error) {
			if err == nil {
				lines <- r.Text
			}
		})
	}()

	// The tail starts asynchronously; keep appending until a line arrives.
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case l := <-lines:
			if l != "new line" {
				t.Fatalf("got %q, want only new lines", l)
			}
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			return
		case <-tick.C:
			fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = fh.WriteString("Jan  1 00:00:01 mx postfix/smtpd[1]: new line\n")
			fh.Close()
		case <-ctx.Done():
			t.Fatal("no line read")
		}
	}
}

func TestFileMissing(t *testing.T) {
	f := &File{Path: filepath.Join(t.TempDir(), "missing.log")}
	if err := f.Run(context.Background(), func(Record, error) {}); err == nil {
		t.Error("no error for a missing file")
	}
}
