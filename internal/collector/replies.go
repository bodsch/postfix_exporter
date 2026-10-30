package collector

import (
	"log/slog"
	"regexp"
	"slices"

	"bodsch.me/postfix_exporter/internal/config"
)

// The reply parsing and the rule semantics follow
// sergeymakinen/postfix_exporter (BSD-3-Clause, see NOTICE).

// reply is an SMTP reply split into its parts.
type reply struct {
	Code         string // "550", empty for Postfix-internal texts
	EnhancedCode string // "5.1.1", may be empty
	Text         string
}

func (r reply) value(match string) string {
	switch match {
	case config.MatchCode:
		return r.Code
	case config.MatchEnhancedCode:
		return r.EnhancedCode
	}
	return r.Text
}

// reReply splits "550 5.1.1 text", "550-5.1.1 text", "550 #5.1.1 text"
// and "550 text".
var reReply = regexp.MustCompile(`^(\d{3})(.{1,3}(\d\.\d{1,3}\.\d{1,3})|[^ ]+|) (.+)$`)

// reAddrPrefix is the "<user@example.com>: " that restriction replies
// start with.
var reAddrPrefix = regexp.MustCompile(`^<[^>]*>: `)

// parseReply splits s; enhancedCode is used when the reply has none, e.g.
// the dsn= of a status line.
func parseReply(s, enhancedCode string) reply {
	var r reply
	if m := reReply.FindStringSubmatch(s); m != nil {
		r.Code, r.EnhancedCode, r.Text = m[1], m[3], m[4]
	} else {
		r.Text = s
	}
	// Restriction replies name the rejected address first; it would make
	// every text unique.
	r.Text = reAddrPrefix.ReplaceAllString(r.Text, "")
	if r.EnhancedCode == "" {
		r.EnhancedCode = enhancedCode
	}
	return r
}

// classifier maps replies to label texts by configured rules.
type classifier struct {
	name   string
	rules  []config.ReplyRule
	max    int
	seen   map[string]bool
	logger *slog.Logger
	warned bool
}

func newClassifier(name string, rules []config.ReplyRule, maxTexts int, logger *slog.Logger) *classifier {
	return &classifier{name: name, rules: rules, max: maxTexts, seen: make(map[string]bool), logger: logger}
}

func (c *classifier) enabled() bool { return len(c.rules) > 0 }

// classify returns the label text of the first matching rule. status is
// the delivery status, empty for non-delivery replies.
func (c *classifier) classify(r reply, status string) (string, bool) {
	for _, rule := range c.rules {
		if len(rule.Statuses) > 0 && !slices.Contains(rule.Statuses, status) {
			continue
		}
		if slices.Contains(rule.NotStatuses, status) {
			continue
		}
		v := r.value(rule.Match)
		m := rule.Regexp.FindStringSubmatchIndex(v)
		if m == nil {
			continue
		}
		return c.limit(string(rule.Regexp.ExpandString(nil, rule.Text, v, m))), true
	}
	return "", false
}

// limit returns text, or "other" once max distinct texts were seen.
func (c *classifier) limit(text string) string {
	if c.seen[text] {
		return text
	}
	if len(c.seen) >= c.max {
		if !c.warned {
			c.warned = true
			c.logger.Warn("too many distinct reply texts, counting further ones as \"other\"; check the replies rules",
				"rules", c.name, "max_texts", c.max)
		}
		return "other"
	}
	c.seen[text] = true
	return text
}
