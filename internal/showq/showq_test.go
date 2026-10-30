package showq

import (
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// record builds a showq record from name/value pairs.
func record(kv ...string) string {
	var b strings.Builder
	for _, s := range kv {
		b.WriteString(s)
		b.WriteByte(0)
	}
	b.WriteByte(0)
	return b.String()
}

var now = time.Unix(1700000000, 0)

func stream() string {
	return record("protocol", "showq_protocol") +
		record("queue_name", "deferred", "queue_id", "ABC", "time", "1699996400", "size", "2048",
			"forced_expire", "0", "sender", "a@example.org",
			"recipient", "b@example.net", "reason", "connect to x: Connection timed out",
			"recipient", "c@example.net", "reason", "connect to x: Connection timed out") +
		record("queue_name", "deferred", "queue_id", "DEF", "time", "1699999940", "size", "20000000",
			"sender", "", "recipient", "d@example.net", "reason", "x") +
		record("queue_name", "active", "queue_id", "GHI", "time", "1700000000", "size", "500",
			"sender", "e@example.org", "recipient", "f@example.net")
}

func TestParse(t *testing.T) {
	stats, err := parse(strings.NewReader(stream()), now)
	if err != nil {
		t.Fatal(err)
	}
	d := stats["deferred"]
	if d.messages != 2 || d.recipients != 3 || d.size.sum != 20002048 || d.oldest != 3600 {
		t.Errorf("deferred = %+v", d)
	}
	if a := stats["active"]; a.messages != 1 || a.recipients != 1 || a.oldest != 0 {
		t.Errorf("active = %+v", a)
	}
	for _, q := range Queues {
		if stats[q] == nil {
			t.Errorf("queue %s missing", q)
		}
	}
	if len(stats) != len(Queues) {
		t.Errorf("unexpected queues: %v", stats.queueNames())
	}
}

func TestParseErrors(t *testing.T) {
	for name, in := range map[string]string{
		"no NUL":       "queue_name",
		"no value":     "queue_name\x00",
		"invalid size": record("queue_name", "active", "size", "x"),
		"invalid time": record("queue_name", "active", "time", "x"),
	} {
		if _, err := parse(strings.NewReader(in), now); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestHistogram(t *testing.T) {
	h := newHistogram([]float64{1, 10})
	for _, v := range []float64{0.5, 1, 5, 100} {
		h.observe(v)
	}
	if h.counts[0] != 2 || h.counts[1] != 1 || h.count != 4 || h.sum != 106.5 {
		t.Errorf("histogram = %+v", h)
	}
}

// TestCollect serves the stream on a unix socket like showq does.
func TestCollect(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "showq")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			_, _ = io.WriteString(conn, stream())
			conn.Close()
		}
	}()

	c := New("unix", sock, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.now = func() time.Time { return now }

	expected := `
# HELP postfix_queue_messages Number of messages in the queue.
# TYPE postfix_queue_messages gauge
postfix_queue_messages{queue="active"} 1
postfix_queue_messages{queue="deferred"} 2
postfix_queue_messages{queue="hold"} 0
postfix_queue_messages{queue="incoming"} 0
postfix_queue_messages{queue="maildrop"} 0
# HELP postfix_queue_oldest_message_age_seconds Age of the oldest message in the queue; 0 if the queue is empty.
# TYPE postfix_queue_oldest_message_age_seconds gauge
postfix_queue_oldest_message_age_seconds{queue="active"} 0
postfix_queue_oldest_message_age_seconds{queue="deferred"} 3600
postfix_queue_oldest_message_age_seconds{queue="hold"} 0
postfix_queue_oldest_message_age_seconds{queue="incoming"} 0
postfix_queue_oldest_message_age_seconds{queue="maildrop"} 0
# HELP postfix_showq_up Whether the queue could be read from showq (1) or not (0).
# TYPE postfix_showq_up gauge
postfix_showq_up 1
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expected),
		"postfix_queue_messages", "postfix_queue_oldest_message_age_seconds", "postfix_showq_up"); err != nil {
		t.Error(err)
	}
	if n := testutil.CollectAndCount(c, "postfix_queue_message_age_seconds"); n != len(Queues) {
		t.Errorf("age histograms = %d, want %d", n, len(Queues))
	}
}

func TestCollectDown(t *testing.T) {
	c := New("unix", filepath.Join(t.TempDir(), "missing"), time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	expected := `
# HELP postfix_showq_up Whether the queue could be read from showq (1) or not (0).
# TYPE postfix_showq_up gauge
postfix_showq_up 0
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expected), "postfix_showq_up"); err != nil {
		t.Error(err)
	}
	if n := testutil.CollectAndCount(c, "postfix_queue_messages"); n != 0 {
		t.Errorf("queue metrics exported while showq is down: %d", n)
	}
	var _ prometheus.Collector = c
}

// TestCollectTimeout checks showq.timeout against a showq that accepts
// the connection and never answers: the scrape must give up with up=0
// instead of hanging until Prometheus' own scrape timeout, which would
// lose all other metrics of the scrape as well.
func TestCollectTimeout(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "showq")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			defer conn.Close() // held open, never written to
		}
	}()

	c := New("unix", sock, 200*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		done <- testutil.CollectAndCompare(c, strings.NewReader(`
# HELP postfix_showq_up Whether the queue could be read from showq (1) or not (0).
# TYPE postfix_showq_up gauge
postfix_showq_up 0
`), "postfix_showq_up")
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("collect took %v with a timeout of 200ms", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("collect hangs on a showq that does not answer")
	}
}
