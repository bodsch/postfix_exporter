package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestDefault(t *testing.T) {
	c := Default()
	if c.Log.Source != SourceFile || c.Log.File != "/var/log/mail.log" || c.Log.SyslogName != "postfix" {
		t.Errorf("log defaults = %+v", c.Log)
	}
	if c.Showq.Network != "unix" || c.Showq.Path != "/var/spool/postfix/public/showq" || c.Showq.Timeout != 10*time.Second {
		t.Errorf("showq defaults = %+v", c.Showq)
	}
	if c.Replies.MaxTexts != 100 {
		t.Errorf("max_texts = %d", c.Replies.MaxTexts)
	}
	if c.MaxServices != 100 {
		t.Errorf("max_services = %d", c.MaxServices)
	}
}

func TestParseEmpty(t *testing.T) {
	if _, err := Parse(nil); err != nil {
		t.Error(err)
	}
}

func TestExampleConfig(t *testing.T) {
	data, err := os.ReadFile("../../config.example.yml")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Replies.Delivery) == 0 || len(c.Replies.Reject) == 0 {
		t.Error("example config has no reply rules")
	}
}

func TestMaxServices(t *testing.T) {
	c, err := Parse([]byte("max_services: 3\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.MaxServices != 3 {
		t.Errorf("max_services = %d, want 3", c.MaxServices)
	}
}

func TestTypeOf(t *testing.T) {
	c, err := Parse([]byte("services:\n  smtp: [encrypt]\n"))
	if err != nil {
		t.Fatal(err)
	}
	for service, want := range map[string]string{
		"smtpd": "smtpd", "submission/smtpd": "smtpd", "relay/smtp": "smtp", "encrypt": "smtp",
	} {
		if got := c.TypeOf(service); got != want {
			t.Errorf("TypeOf(%q) = %q, want %q", service, got, want)
		}
	}
}

func TestInvalid(t *testing.T) {
	for name, tt := range map[string]struct{ yaml, err string }{
		"unknown field": {"foo: 1", "field foo not found"},
		"source":        {"log: {source: syslog}", "log.source"},
		"syslog_name":   {"log: {syslog_name: a/b}", "slash"},
		"showq address": {"showq: {address: /var/spool/postfix/public/showq}", "showq.address"},
		"showq network": {"showq: {address: 'udp://x:1'}", "unsupported network"},
		"daemon type":   {"services: {smtpx: [a]}", "unknown daemon type"},
		"service twice": {"services: {smtp: [a], lmtp: [a]}", "listed for both"},
		"regexp":        {"replies: {reject: [{regexp: '(', text: x}]}", "missing closing"},
		"no regexp":     {"replies: {reject: [{text: x}]}", "regexp is required"},
		"no text":       {"replies: {reject: [{regexp: x}]}", "text is required"},
		"match":         {"replies: {reject: [{regexp: x, text: x, match: foo}]}", "match must be"},
		"statuses":      {"replies: {smtp: [{regexp: x, text: x, statuses: [sent]}]}", "only valid for delivery"},
	} {
		_, err := Parse([]byte(tt.yaml))
		if err == nil || !strings.Contains(err.Error(), tt.err) {
			t.Errorf("%s: err = %v, want %q", name, err, tt.err)
		}
	}
}
