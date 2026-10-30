package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bodsch.me/postfix_exporter/internal/collector"
	"bodsch.me/postfix_exporter/internal/config"
	"bodsch.me/postfix_exporter/internal/logsource"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestRunReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mail.log")
	log := "Jan  1 00:00:00 mx postfix/smtpd[1]: connect from x[192.0.2.1]\n" +
		"Jan  1 00:00:00 mx postfix/smtpd[1]: NOQUEUE: reject: RCPT from x[192.0.2.1]: 554 5.7.1 <a@example.net>: Relay access denied; from=<b@example.org> to=<a@example.net> proto=ESMTP helo=<x>\n"
	if err := os.WriteFile(path, []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	col := collector.New(config.Default(), discardLogger())
	if err := runReplay(context.Background(), col, &logsource.File{Path: path, Replay: true}, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`postfix_smtpd_connections_total{service="smtpd"} 1`,
		`postfix_smtpd_rejects_total{code="554",command="RCPT",reason="relay_denied",service="smtpd"} 1`,
		`postfix_exporter_log_lines_total{result="matched"} 2`,
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("replay output lacks %s", want)
		}
	}
}

func TestHandler(t *testing.T) {
	col := collector.New(config.Default(), discardLogger())
	h := newHandler(newRegistry(col), "/metrics", 1, discardLogger())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	for _, want := range []string{"postfix_exporter_build_info", "postfix_exporter_log_lines_total", "go_goroutines"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("metrics lack %s", want)
		}
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `href="/metrics"`) {
		t.Errorf("landing page: %d %q", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/foo", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("/foo: status = %d", rec.Code)
	}
}
