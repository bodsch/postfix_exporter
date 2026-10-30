// Package config loads and validates the exporter configuration file.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Log source kinds.
const (
	SourceFile    = "file"
	SourceJournal = "journal"
)

// DaemonTypes are the Postfix daemons whose log lines are turned into
// metrics. Everything else is only counted by service and severity.
var DaemonTypes = []string{
	"smtpd", "postscreen", "cleanup", "qmgr", "pickup", "bounce", "master",
	"smtp", "lmtp", "local", "virtual", "pipe", "error", "retry", "discard",
}

// Config is the root of the configuration file.
type Config struct {
	Log   Log   `yaml:"log"`
	Showq Showq `yaml:"showq"`

	// Services maps a daemon type to the service names it runs under in
	// master.cf, for names whose last path element is not the daemon type,
	// e.g. {"smtp": ["encrypt"]} for
	//
	//	encrypt  unix  -  -  n  -  -  smtp
	//
	// "submission/smtpd" or "relay/smtp" need no entry.
	Services map[string][]string `yaml:"services"`
	// MaxServices limits the distinct service names in the "service"
	// label; lines of further services are counted as "other". Any local
	// user can log under the tag "postfix/<anything>".
	MaxServices int `yaml:"max_services"`

	Replies Replies `yaml:"replies"`
}

// Log configures where Postfix log lines are read from.
type Log struct {
	// Source is "file" or "journal".
	Source string `yaml:"source"`

	// File is the syslog file Postfix logs to (Source "file").
	File string `yaml:"file"`
	// Poll makes the file source poll for changes instead of using
	// inotify, e.g. on network file systems.
	Poll bool `yaml:"poll"`

	Journal Journal `yaml:"journal"`

	// SyslogName is Postfix's syslog_name; lines of other programs are
	// counted as foreign.
	SyslogName string `yaml:"syslog_name"`
}

// Journal configures reading from the systemd journal via journalctl.
type Journal struct {
	// Command is the journalctl binary.
	Command string `yaml:"command"`
	// Units restricts the journal to these units, e.g. postfix.service
	// or, on Debian, postfix@-.service. Empty reads the whole journal.
	Units []string `yaml:"units"`
	// Directory reads the journal files from this directory instead of
	// the system journal.
	Directory string `yaml:"directory"`
}

// Showq configures the queue metrics.
type Showq struct {
	// Disabled turns the queue metrics off.
	Disabled bool `yaml:"disabled"`
	// Address is "unix:///path/to/showq" or "tcp://host:port".
	Address string        `yaml:"address"`
	Timeout time.Duration `yaml:"timeout"`

	// Network and Path are Address split up, set by finalize.
	Network string `yaml:"-"`
	Path    string `yaml:"-"`
}

// Replies classifies the text of SMTP replies into labels. Every list is
// tried in order and the first matching rule wins; replies no rule matches
// are not counted. Without rules the reply metrics are not exported.
type Replies struct {
	// Delivery covers the replies of delivery attempts with a status
	// (sent, deferred, bounced, ...) by smtp, lmtp and the local agents.
	Delivery []ReplyRule `yaml:"delivery"`
	// SMTP covers remote server replies the smtp client logs without a
	// status, e.g. greylisting on RCPT TO.
	SMTP []ReplyRule `yaml:"smtp"`
	// Reject covers the replies of NOQUEUE rejects by smtpd.
	Reject []ReplyRule `yaml:"reject"`

	// MaxTexts limits the distinct "text" label values per metric; further
	// texts are counted as "other". It protects Prometheus from rules that
	// copy unbounded parts of a reply into the label.
	MaxTexts int `yaml:"max_texts"`
}

// Match types of a ReplyRule.
const (
	MatchCode         = "code"
	MatchEnhancedCode = "enhanced_code"
	MatchText         = "text"
)

// ReplyRule maps a reply to a label text.
type ReplyRule struct {
	// Statuses and NotStatuses restrict a delivery rule to, or exclude it
	// from, delivery statuses. Only valid in the delivery list.
	Statuses    []string `yaml:"statuses"`
	NotStatuses []string `yaml:"not_statuses"`

	// Match selects what Regexp is applied to: "code" (550),
	// "enhanced_code" (5.7.1) or "text" (the rest, the default).
	Match  string `yaml:"match"`
	Regexp Regexp `yaml:"regexp"`
	// Text is the label value; it may refer to submatches of Regexp
	// ($1, ${name}), see regexp.Regexp.Expand.
	Text string `yaml:"text"`
}

// Regexp is a regular expression that is compiled while decoding.
type Regexp struct {
	*regexp.Regexp
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (r *Regexp) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return err
	}
	re, err := regexp.Compile(s)
	if err != nil {
		return fmt.Errorf("line %d: %w", node.Line, err)
	}
	r.Regexp = re
	return nil
}

// Default returns the configuration used without a configuration file.
func Default() *Config {
	cfg := &Config{}
	if err := cfg.finalize(); err != nil {
		panic(err) // the defaults are valid
	}
	return cfg
}

// Load reads, defaults and validates the configuration file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse decodes, defaults and validates configuration data.
func Parse(data []byte) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	// An empty file is a valid configuration with all defaults.
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	if err := cfg.finalize(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) finalize() error {
	l := &c.Log
	switch l.Source {
	case "":
		l.Source = SourceFile
	case SourceFile, SourceJournal:
	default:
		return fmt.Errorf("config: log.source must be %q or %q, not %q", SourceFile, SourceJournal, l.Source)
	}
	if l.File == "" {
		l.File = "/var/log/mail.log"
	}
	if l.Journal.Command == "" {
		l.Journal.Command = "journalctl"
	}
	if l.SyslogName == "" {
		l.SyslogName = "postfix"
	}
	if strings.Contains(l.SyslogName, "/") {
		return fmt.Errorf("config: log.syslog_name %q must not contain a slash", l.SyslogName)
	}

	if err := c.Showq.finalize(); err != nil {
		return err
	}

	// A service name may only belong to one daemon type, or its lines
	// would be parsed by whichever type the map iteration hits first.
	owner := make(map[string]string)
	for typ, names := range c.Services {
		if !slices.Contains(DaemonTypes, typ) {
			return fmt.Errorf("config: services: unknown daemon type %q (known: %s)", typ, strings.Join(DaemonTypes, ", "))
		}
		for _, n := range names {
			if n == "" {
				return fmt.Errorf("config: services.%s: empty service name", typ)
			}
			if o, ok := owner[n]; ok && o != typ {
				return fmt.Errorf("config: service %q is listed for both %s and %s", n, o, typ)
			}
			owner[n] = typ
		}
	}

	if c.MaxServices <= 0 {
		c.MaxServices = 100
	}

	r := &c.Replies
	if r.MaxTexts <= 0 {
		r.MaxTexts = 100
	}
	for name, rules := range map[string][]ReplyRule{"delivery": r.Delivery, "smtp": r.SMTP, "reject": r.Reject} {
		for i := range rules {
			if err := rules[i].finalize(name == "delivery"); err != nil {
				return fmt.Errorf("config: replies.%s[%d]: %w", name, i, err)
			}
		}
	}
	return nil
}

func (s *Showq) finalize() error {
	if s.Address == "" {
		s.Address = "unix:///var/spool/postfix/public/showq"
	}
	if s.Timeout <= 0 {
		s.Timeout = 10 * time.Second
	}
	network, path, ok := strings.Cut(s.Address, "://")
	if !ok || path == "" {
		return fmt.Errorf("config: showq.address %q must be unix:///path or tcp://host:port", s.Address)
	}
	switch network {
	case "unix", "tcp", "tcp4", "tcp6":
	default:
		return fmt.Errorf("config: showq.address %q: unsupported network %q", s.Address, network)
	}
	s.Network, s.Path = network, path
	return nil
}

func (r *ReplyRule) finalize(delivery bool) error {
	if r.Regexp.Regexp == nil {
		return errors.New("regexp is required")
	}
	switch r.Match {
	case "":
		r.Match = MatchText
	case MatchCode, MatchEnhancedCode, MatchText:
	default:
		return fmt.Errorf("match must be %s, %s or %s, not %q", MatchCode, MatchEnhancedCode, MatchText, r.Match)
	}
	if !delivery && (len(r.Statuses) > 0 || len(r.NotStatuses) > 0) {
		return errors.New("statuses and not_statuses are only valid for delivery rules")
	}
	if r.Text == "" {
		return errors.New("text is required")
	}
	return nil
}

// TypeOf returns the daemon type of a Postfix service name such as
// "submission/smtpd" or "encrypt".
func (c *Config) TypeOf(service string) string {
	for typ, names := range c.Services {
		if slices.Contains(names, service) {
			return typ
		}
	}
	return service[strings.LastIndexByte(service, '/')+1:]
}
