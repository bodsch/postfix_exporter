// Package showq exports the Postfix mail queue as read from the showq
// service.
//
// The protocol handling follows Hsn723/postfix_exporter (Apache-2.0, see
// NOTICE): showq sends attribute name/value pairs, each terminated by a
// NUL byte; an empty name ends a record (one queued message).
package showq

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const ns = "postfix"

// Queues are always exported, with 0 when they are empty, so that alerts
// on them have a value to compare against.
var Queues = []string{"maildrop", "incoming", "active", "deferred", "hold"}

var (
	// 1m .. 1w, aligned with maximal_queue_lifetime (5d) and the usual
	// alert thresholds of hours.
	ageBuckets = []float64{60, 300, 900, 3600, 4 * 3600, 12 * 3600, 86400, 2 * 86400, 5 * 86400, 7 * 86400}
	// 1 kB .. 100 MB; message_size_limit defaults to 10 MB.
	sizeBuckets = []float64{1e3, 1e4, 1e5, 1e6, 5e6, 1e7, 2.5e7, 5e7, 1e8}
)

var (
	lQueue = []string{"queue"}

	descUp         = prometheus.NewDesc(ns+"_showq_up", "Whether the queue could be read from showq (1) or not (0).", nil, nil)
	descDuration   = prometheus.NewDesc(ns+"_showq_scrape_duration_seconds", "Time it took to read the queue from showq.", nil, nil)
	descMessages   = prometheus.NewDesc(ns+"_queue_messages", "Number of messages in the queue.", lQueue, nil)
	descRecipients = prometheus.NewDesc(ns+"_queue_recipients", "Number of recipients of the messages in the queue.", lQueue, nil)
	descBytes      = prometheus.NewDesc(ns+"_queue_size_bytes", "Total size of the messages in the queue.", lQueue, nil)
	descOldest     = prometheus.NewDesc(ns+"_queue_oldest_message_age_seconds", "Age of the oldest message in the queue; 0 if the queue is empty.", lQueue, nil)
	descAge        = prometheus.NewDesc(ns+"_queue_message_age_seconds", "Age of the messages in the queue.", lQueue, nil)
	descSize       = prometheus.NewDesc(ns+"_queue_message_size_bytes", "Size of the messages in the queue.", lQueue, nil)
)

// Collector implements prometheus.Collector. Every scrape reads the whole
// queue, so the values are always current.
type Collector struct {
	network, address string
	timeout          time.Duration
	logger           *slog.Logger
	now              func() time.Time

	// showq serves one client at a time per process; serializing the
	// scrapes avoids starting extra showq processes for nothing.
	mu sync.Mutex
}

// New returns a collector reading from showq at address on network
// ("unix" or "tcp").
func New(network, address string, timeout time.Duration, logger *slog.Logger) *Collector {
	return &Collector{network: network, address: address, timeout: timeout, logger: logger, now: time.Now}
}

// Describe implements prometheus.Collector.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{descUp, descDuration, descMessages, descRecipients, descBytes, descOldest, descAge, descSize} {
		ch <- d
	}
}

// Collect implements prometheus.Collector.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()

	start := time.Now()
	stats, err := c.read()
	ch <- prometheus.MustNewConstMetric(descDuration, prometheus.GaugeValue, time.Since(start).Seconds())
	if err != nil {
		c.logger.Error("reading the queue from showq failed", "address", c.address, "err", err)
		ch <- prometheus.MustNewConstMetric(descUp, prometheus.GaugeValue, 0)
		return
	}
	ch <- prometheus.MustNewConstMetric(descUp, prometheus.GaugeValue, 1)
	for _, q := range stats.queueNames() {
		s := stats[q]
		ch <- prometheus.MustNewConstMetric(descMessages, prometheus.GaugeValue, float64(s.messages), q)
		ch <- prometheus.MustNewConstMetric(descRecipients, prometheus.GaugeValue, float64(s.recipients), q)
		ch <- prometheus.MustNewConstMetric(descBytes, prometheus.GaugeValue, s.size.sum, q)
		ch <- prometheus.MustNewConstMetric(descOldest, prometheus.GaugeValue, s.oldest, q)
		ch <- s.age.metric(descAge, q)
		ch <- s.size.metric(descSize, q)
	}
}

func (c *Collector) read() (queueStats, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, c.network, c.address)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(c.timeout)); err != nil {
		return nil, err
	}
	return parse(conn, c.now())
}

// queueStats holds the statistics per queue name.
type queueStats map[string]*queue

type queue struct {
	messages, recipients int
	oldest               float64
	age, size            histogram
}

func newQueueStats() queueStats {
	s := make(queueStats)
	for _, q := range Queues {
		s.get(q)
	}
	return s
}

func (s queueStats) get(name string) *queue {
	q, ok := s[name]
	if !ok {
		q = &queue{age: newHistogram(ageBuckets), size: newHistogram(sizeBuckets)}
		s[name] = q
	}
	return q
}

// queueNames returns the known queues first, then any others showq reports.
func (s queueStats) queueNames() []string {
	names := slices.Clone(Queues)
	var extra []string
	for n := range s {
		if !slices.Contains(Queues, n) {
			extra = append(extra, n)
		}
	}
	slices.Sort(extra)
	return append(names, extra...)
}

// message collects the attributes of one record before it is counted:
// showq sends queue_name first, but the order is not documented.
type message struct {
	queue      string
	time, size float64
	hasTime    bool
	hasSize    bool
	recipients int
}

func (s queueStats) add(m *message, now time.Time) {
	if m.queue == "" {
		return // e.g. the protocol announcement before the first message
	}
	q := s.get(m.queue)
	q.messages++
	q.recipients += m.recipients
	if m.hasSize {
		q.size.observe(m.size)
	}
	if m.hasTime {
		age := math.Max(0, float64(now.UnixNano())/1e9-m.time)
		q.age.observe(age)
		q.oldest = math.Max(q.oldest, age)
	}
}

// parse reads the showq stream until EOF.
func parse(r io.Reader, now time.Time) (queueStats, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	sc.Split(scanNUL)

	stats := newQueueStats()
	var m message
	for sc.Scan() {
		key := sc.Text()
		if key == "" {
			stats.add(&m, now)
			m = message{}
			continue
		}
		if !sc.Scan() {
			if err := sc.Err(); err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("showq: attribute %q without value", key)
		}
		value := sc.Text()
		var err error
		switch key {
		case "queue_name":
			m.queue = value
		case "time":
			m.time, err = strconv.ParseFloat(value, 64)
			m.hasTime = true
		case "size":
			m.size, err = strconv.ParseFloat(value, 64)
			m.hasSize = true
		case "recipient":
			m.recipients++
		}
		if err != nil {
			return nil, fmt.Errorf("showq: attribute %s: %w", key, err)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	stats.add(&m, now) // a stream that ends without a final separator
	return stats, nil
}

func scanNUL(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexByte(data, 0); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return 0, nil, errors.New("showq: data after the last NUL terminator")
	}
	return 0, nil, nil
}

// histogram accumulates observations for a const histogram.
type histogram struct {
	bounds []float64
	counts []uint64 // per bucket, not cumulative
	count  uint64
	sum    float64
}

func newHistogram(bounds []float64) histogram {
	return histogram{bounds: bounds, counts: make([]uint64, len(bounds))}
}

func (h *histogram) observe(v float64) {
	h.count++
	h.sum += v
	if i, _ := slices.BinarySearch(h.bounds, v); i < len(h.bounds) {
		h.counts[i]++
	}
}

func (h *histogram) metric(d *prometheus.Desc, labels ...string) prometheus.Metric {
	buckets := make(map[float64]uint64, len(h.bounds))
	var cum uint64
	for i, b := range h.bounds {
		cum += h.counts[i]
		buckets[b] = cum
	}
	return prometheus.MustNewConstHistogram(d, h.count, h.sum, buckets, labels...)
}
