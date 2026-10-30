package logsource

import (
	"context"
	"fmt"

	"bodsch.me/postfix_exporter/internal/config"
)

// Handler receives every record of a source, or the error from parsing a
// line. It is called from a single goroutine.
type Handler func(Record, error)

// Source reads log records.
type Source interface {
	// Run passes records to h until ctx is cancelled, the source fails
	// or, when the source replays, it reaches the end.
	Run(ctx context.Context, h Handler) error
	// String describes the source for log messages.
	String() string
}

// Options control how a source is read.
type Options struct {
	// Replay reads existing log lines and returns at the end instead of
	// following new ones.
	Replay bool
	// Since limits a journal replay to this time span; a file replay
	// always reads the whole file.
	Since string
}

// New returns the source configured in cfg.
func New(cfg config.Log, opts Options) (Source, error) {
	switch cfg.Source {
	case config.SourceFile:
		return &File{Path: cfg.File, Poll: cfg.Poll, Replay: opts.Replay}, nil
	case config.SourceJournal:
		return &Journal{
			Command:   cfg.Journal.Command,
			Units:     cfg.Journal.Units,
			Directory: cfg.Journal.Directory,
			Replay:    opts.Replay,
			Since:     opts.Since,
		}, nil
	}
	return nil, fmt.Errorf("unknown log source %q", cfg.Source)
}
