// Package collector turns Postfix log records into Prometheus metrics.
package collector

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"bodsch.me/postfix_exporter/internal/config"
	"bodsch.me/postfix_exporter/internal/logsource"
)

// Collector implements prometheus.Collector. Its metrics are counters
// that Handle updates from the log; a scrape only reads them.
type Collector struct {
	cfg    *config.Config
	m      *metrics
	logger *slog.Logger
	now    func() time.Time

	// Only touched by Handle, which runs in a single goroutine.
	types map[string]string // service name -> daemon type

	deliveryReplies *classifier
	smtpReplies     *classifier
	rejectReplies   *classifier
}

// New returns a collector for the given configuration.
func New(cfg *config.Config, logger *slog.Logger) *Collector {
	return &Collector{
		cfg:             cfg,
		m:               newMetrics(),
		logger:          logger,
		now:             time.Now,
		types:           make(map[string]string),
		deliveryReplies: newClassifier("delivery", cfg.Replies.Delivery, cfg.Replies.MaxTexts, logger),
		smtpReplies:     newClassifier("smtp", cfg.Replies.SMTP, cfg.Replies.MaxTexts, logger),
		rejectReplies:   newClassifier("reject", cfg.Replies.Reject, cfg.Replies.MaxTexts, logger),
	}
}

// Describe implements prometheus.Collector.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	for _, m := range c.m.collectors() {
		m.Describe(ch)
	}
}

// Collect implements prometheus.Collector.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	for _, m := range c.m.collectors() {
		m.Collect(ch)
	}
}

// Run reads src until ctx is cancelled. A failing source is restarted
// after retryDelay, so that a journalctl that was killed or a log file
// that is briefly missing does not end the exporter.
func (c *Collector) Run(ctx context.Context, src logsource.Source, retryDelay time.Duration) {
	for {
		c.m.sourceUp.Set(1)
		err := src.Run(ctx, c.Handle)
		if ctx.Err() != nil {
			return
		}
		c.m.sourceUp.Set(0)
		c.m.sourceRestarts.Inc()
		c.logger.Error("reading the log failed, retrying", "source", src.String(), "err", err, "retry_in", retryDelay)
		select {
		case <-ctx.Done():
			return
		case <-time.After(retryDelay):
		}
	}
}

// Replay reads src once until its end; for the replay mode.
func (c *Collector) Replay(ctx context.Context, src logsource.Source) error {
	c.m.sourceUp.Set(1)
	return src.Run(ctx, c.Handle)
}

// Handle processes one record. It implements logsource.Handler and must
// not be called concurrently.
func (c *Collector) Handle(r logsource.Record, err error) {
	if err != nil {
		c.m.logLines.WithLabelValues(resultInvalid).Inc()
		c.logger.Debug("invalid log line", "err", err)
		return
	}
	if r.Program != c.cfg.Log.SyslogName || r.Service == "" {
		c.m.logLines.WithLabelValues(resultForeign).Inc()
		return
	}
	c.m.lastLine.Set(float64(c.now().UnixNano()) / 1e9)

	typ, ok := c.types[r.Service]
	if !ok {
		typ = c.cfg.TypeOf(r.Service)
		c.types[r.Service] = typ
		c.m.initService(r.Service, typ)
	}
	c.m.logMessages.WithLabelValues(r.Service, r.Severity).Inc()

	result := c.parse(typ, r)
	if result == resultUnmatched && r.Severity != logsource.SeverityInfo {
		// Counted by severity; that is all a generic warning needs.
		result = resultIgnored
	}
	if result == resultUnmatched {
		c.logger.Debug("unmatched log line", "service", r.Service, "text", r.Text)
	}
	c.m.logLines.WithLabelValues(result).Inc()
}
