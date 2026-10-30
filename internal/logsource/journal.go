package logsource

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// Journal reads the systemd journal through journalctl.
//
// Reading the journal files directly needs libsystemd and thus cgo, which
// would rule out static binaries. journalctl is present wherever there is
// a journal, and its JSON output carries the syslog tag and message as
// separate fields, so nothing has to be parsed from a text line.
type Journal struct {
	Command   string
	Units     []string
	Directory string
	// Replay reads existing entries (since Since) and returns at the end.
	Replay bool
	Since  string
}

func (j *Journal) String() string {
	if len(j.Units) == 0 {
		return "journal"
	}
	return "journal (" + strings.Join(j.Units, ", ") + ")"
}

func (j *Journal) args() []string {
	args := []string{"--output=json", "--no-pager", "--quiet"}
	if j.Replay {
		if j.Since != "" {
			args = append(args, "--since=-"+strings.TrimPrefix(j.Since, "-"))
		}
	} else {
		// Only new entries, like the file source starting at the end.
		args = append(args, "--follow", "--lines=0")
	}
	if j.Directory != "" {
		args = append(args, "--directory="+j.Directory)
	}
	for _, u := range j.Units {
		args = append(args, "--unit="+u)
	}
	return args
}

// Run implements Source.
func (j *Journal) Run(ctx context.Context, h Handler) error {
	cmd := exec.CommandContext(ctx, j.Command, j.args()...)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		r, ok, err := parseJournalEntry(sc.Bytes())
		if !ok && err == nil {
			continue // not a syslog message, e.g. from systemd itself
		}
		h(r, err)
	}
	scanErr := sc.Err()
	// Drain the pipe so that Wait does not block on a full pipe after a
	// scanner error.
	_, _ = io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()

	if ctx.Err() != nil {
		return nil
	}
	if scanErr != nil {
		return scanErr
	}
	if waitErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return fmt.Errorf("%s: %w: %s", j.Command, waitErr, msg)
		}
		return fmt.Errorf("%s: %w", j.Command, waitErr)
	}
	if !j.Replay {
		return errors.New(j.Command + " exited")
	}
	return nil
}

// journalEntry holds the fields used from journalctl's JSON output. Values
// that are not valid UTF-8 are written as arrays of bytes instead of
// strings, hence json.RawMessage.
type journalEntry struct {
	Message    json.RawMessage `json:"MESSAGE"`
	Identifier json.RawMessage `json:"SYSLOG_IDENTIFIER"`
	SyslogPID  json.RawMessage `json:"SYSLOG_PID"`
	PID        json.RawMessage `json:"_PID"`
}

// parseJournalEntry returns ok=false without an error for entries that
// are not syslog messages.
func parseJournalEntry(line []byte) (Record, bool, error) {
	var e journalEntry
	if err := json.Unmarshal(line, &e); err != nil {
		return Record{}, false, fmt.Errorf("decoding journal entry: %w", err)
	}
	tag := journalString(e.Identifier)
	if tag == "" {
		return Record{}, false, nil
	}
	pidStr := journalString(e.SyslogPID)
	if pidStr == "" {
		pidStr = journalString(e.PID)
	}
	pid, _ := strconv.ParseInt(pidStr, 10, 64)
	return newRecord(tag, pid, journalString(e.Message)), true, nil
}

func journalString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var ints []int
	if json.Unmarshal(raw, &ints) == nil {
		b := make([]byte, len(ints))
		for i, v := range ints {
			b[i] = byte(v)
		}
		return strings.ToValidUTF8(string(b), "�")
	}
	return ""
}
