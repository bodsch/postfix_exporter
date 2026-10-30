package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"

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

// panicGatherer panics on its first Gather.
type panicGatherer struct{ calls int }

func (g *panicGatherer) Gather() ([]*dto.MetricFamily, error) {
	g.calls++
	if g.calls == 1 {
		panic("collector failed")
	}
	return nil, nil
}

// TestHandlerSlotAfterPanic checks that a scrape that panics releases its
// slot, so that later scrapes are not rejected with 503 forever.
func TestHandlerSlotAfterPanic(t *testing.T) {
	h := newHandler(&panicGatherer{}, "/metrics", 1, discardLogger())
	func() {
		defer func() {
			if recover() == nil {
				t.Error("first scrape did not panic")
			}
		}()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/metrics", nil))
	}()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("second scrape: status = %d, want %d", rec.Code, http.StatusOK)
	}
}

// blockingGatherer blocks every Gather until release is closed.
type blockingGatherer struct {
	started chan struct{}
	release chan struct{}
}

func (g *blockingGatherer) Gather() ([]*dto.MetricFamily, error) {
	g.started <- struct{}{}
	<-g.release
	return nil, nil
}

// TestHandlerLimit checks --web.max-requests: scrapes beyond the limit are
// refused at once instead of queueing behind a slow showq, and the slot
// is free again afterwards.
func TestHandlerLimit(t *testing.T) {
	g := &blockingGatherer{started: make(chan struct{}, 1), release: make(chan struct{})}
	h := newHandler(g, "/metrics", 1, discardLogger())

	first := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		first <- rec.Code
	}()
	select {
	case <-g.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first scrape did not start collecting")
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("scrape beyond the limit: status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}

	close(g.release)
	if code := <-first; code != http.StatusOK {
		t.Errorf("first scrape: status = %d", code)
	}
	g.started = make(chan struct{}, 1)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("scrape after the slot was freed: status = %d, want %d", rec.Code, http.StatusOK)
	}
}

// TestServeShutdown checks the promise of serve: a SIGTERM (ctx cancelled)
// stops the server and is not an error, so systemd sees a clean exit and
// does not count a failure for Restart=on-failure.
func TestServeShutdown(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	srv := newServer(http.NotFoundHandler())
	go func() { done <- serve(ctx, srv, l, "", discardLogger()) }()

	// Wait until it serves, then stop it.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		resp, err := http.Get("http://" + l.Addr().String() + "/")
		if err == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server did not start")
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serve = %v, want nil on shutdown", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not return after shutdown; systemd would have to kill the exporter")
	}
}
