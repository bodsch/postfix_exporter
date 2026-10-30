package logsource

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
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

// TestJournalLongEntry checks that an entry beyond the scanner limit ends
// a following Run with an error, so that the source is restarted, instead
// of blocking on a journalctl that never exits.
func TestJournalLongEntry(t *testing.T) {
	dir := t.TempDir()
	long := filepath.Join(dir, "long.json")
	if err := os.WriteFile(long, append(bytes.Repeat([]byte("a"), 5<<20), '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(dir, "journalctl")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\ncat "+long+"\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- (&Journal{Command: fake}).Run(context.Background(), func(Record, error) {}) }()
	select {
	case err := <-done:
		if !errors.Is(err, bufio.ErrTooLong) {
			t.Errorf("err = %v, want %v", err, bufio.ErrTooLong)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run still blocked after the scanner failed")
	}
}

// TestJournalStderr checks that only the end of the error output of
// journalctl is kept and reported.
func TestJournalStderr(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "journalctl")
	script := "#!/bin/sh\n" +
		"i=0; while [ $i -lt 2000 ]; do echo 'warning: something' >&2; i=$((i+1)); done\n" +
		"echo 'last words' >&2\nexit 1\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	err := (&Journal{Command: fake, Replay: true}).Run(context.Background(), func(Record, error) {})
	if err == nil || !strings.HasSuffix(err.Error(), "last words") {
		t.Fatalf("err = %v, want the end of stderr", err)
	}
	if len(err.Error()) > maxStderr+200 {
		t.Errorf("error message has %d bytes, want at most about %d", len(err.Error()), maxStderr)
	}
}

func TestTailBuffer(t *testing.T) {
	b := &tailBuffer{max: 5}
	for _, tt := range []struct{ write, want string }{
		{"abc", "abc"},
		{"de", "abcde"},
		{"fg", "cdefg"},
		{"0123456789", "56789"},
		{"", "56789"},
	} {
		if n, err := b.Write([]byte(tt.write)); n != len(tt.write) || err != nil {
			t.Errorf("Write(%q) = %d, %v", tt.write, n, err)
		}
		if got := b.String(); got != tt.want {
			t.Errorf("after Write(%q): %q, want %q", tt.write, got, tt.want)
		}
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

// followFile runs a following file source on path and returns the texts
// of the records it reads.
func followFile(t *testing.T, path string, poll bool) (<-chan string, context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	lines := make(chan string, 100)
	done := make(chan error, 1)
	f := &File{Path: path, Poll: poll}
	go func() {
		done <- f.Run(ctx, func(r Record, err error) {
			if err == nil {
				lines <- r.Text
			} else {
				lines <- "invalid"
			}
		})
	}()
	return lines, cancel, done
}

// appendUntil appends line to path until want is read, so that the
// asynchronous start of the tail does not matter.
func appendUntil(t *testing.T, lines <-chan string, path, text string) {
	t.Helper()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case l := <-lines:
			if l == text {
				return
			}
		case <-tick.C:
			fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = fh.WriteString("Jan  1 00:00:01 mx postfix/smtpd[1]: " + text + "\n")
			fh.Close()
		case <-deadline:
			t.Fatalf("%q not read; after a log rotation the exporter would silently stop counting", text)
		}
	}
}

// TestFileRotation checks the promise of File: it follows the log across
// rotation by rename (logrotate's default) and by truncation
// (copytruncate), like "tail -F". Both with inotify and with polling.
func TestFileRotation(t *testing.T) {
	for _, poll := range []bool{false, true} {
		t.Run(map[bool]string{false: "inotify", true: "poll"}[poll], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "mail.log")
			if err := os.WriteFile(path, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			lines, cancel, done := followFile(t, path, poll)
			defer func() {
				cancel()
				if err := <-done; err != nil {
					t.Error(err)
				}
			}()
			appendUntil(t, lines, path, "before rotation")

			if err := os.Rename(path, path+".1"); err != nil {
				t.Fatal(err)
			}
			appendUntil(t, lines, path, "after rename")

			if err := os.Truncate(path, 0); err != nil {
				t.Fatal(err)
			}
			appendUntil(t, lines, path, "after truncate")
		})
	}
}

// TestFileLongLine checks that one overlong line neither ends the source
// nor holds the lines after it: the file source must keep reading.
func TestFileLongLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mail.log")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	lines, cancel, done := followFile(t, path, true)
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("source ended with %v after a long line", err)
		}
	}()
	appendUntil(t, lines, path, "first")

	fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fh.Write(append(bytes.Repeat([]byte("x"), 3*maxLineSize), '\n'))
	fh.Close()
	appendUntil(t, lines, path, "after the long line")
}

// TestJournalCancel checks that a shutdown stops a following journalctl
// and is not reported as an error; otherwise every exporter stop would
// leave a journalctl behind or log a failure.
func TestJournalCancel(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "journalctl")
	pidFile := filepath.Join(dir, "pid")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho $$ > "+pidFile+"\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- (&Journal{Command: fake}).Run(ctx, func(Record, error) {}) }()

	var pid int
	for deadline := time.Now().Add(5 * time.Second); pid == 0; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("fake journalctl did not start")
		}
		data, _ := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("err = %v, want nil on shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel; the exporter would not stop")
	}
	if err := syscall.Kill(pid, 0); err == nil {
		t.Errorf("journalctl (pid %d) still running after shutdown", pid)
	}
}
