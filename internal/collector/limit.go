package collector

import "log/slog"

// labelOther replaces label values beyond a limit.
const labelOther = "other"

// limiter bounds the distinct values of a label. Log lines can be written
// by local users and carry values chosen by remote clients and servers;
// without a limit they could create series without end.
type limiter struct {
	max    int
	seen   map[string]bool
	logger *slog.Logger
	msg    string
	attrs  []any
	warned bool
}

// newLimiter returns a limiter that logs msg with attrs once the limit
// is reached.
func newLimiter(maxValues int, logger *slog.Logger, msg string, attrs ...any) *limiter {
	return &limiter{max: maxValues, seen: make(map[string]bool), logger: logger, msg: msg, attrs: attrs}
}

// value returns v, or "other" once max distinct values were seen. Only
// known values are cheap; it must not be called concurrently.
func (l *limiter) value(v string) string {
	if l.seen[v] {
		return v
	}
	if len(l.seen) >= l.max {
		if !l.warned {
			l.warned = true
			l.logger.Warn(l.msg, l.attrs...)
		}
		return labelOther
	}
	l.seen[v] = true
	return v
}
