package logsource

import (
	"bufio"
	"context"
	"io"
	"os"

	"github.com/nxadm/tail"
)

// maxLineSize bounds the memory of one log line. Postfix lines are far
// shorter; syslog daemons cut messages at a few kB.
const maxLineSize = 1 << 20

// File reads a syslog file. It follows the file from its end, across
// rotation (rename or truncate), like "tail -F".
type File struct {
	Path string
	Poll bool
	// Replay reads the file from the start and returns at its end.
	Replay bool
}

func (f *File) String() string { return "file " + f.Path }

// Run implements Source.
func (f *File) Run(ctx context.Context, h Handler) error {
	if f.Replay {
		return f.replay(ctx, h)
	}
	t, err := tail.TailFile(f.Path, tail.Config{
		Location: &tail.SeekInfo{Whence: io.SeekEnd},
		ReOpen:   true,
		Follow:   true,
		Poll:     f.Poll,
		// The file must exist at start: a typo in the path would
		// otherwise wait silently forever. A file that disappears
		// later, during rotation, is waited for.
		MustExist: true,
		// Without a limit the tail buffers a line of any length.
		MaxLineSize: maxLineSize,
		Logger:      tail.DiscardingLogger,
	})
	if err != nil {
		return err
	}
	defer t.Cleanup()
	defer func() { _ = t.Stop() }()

	for {
		select {
		case <-ctx.Done():
			return nil
		case l, ok := <-t.Lines:
			if !ok {
				return t.Err()
			}
			if l.Err != nil {
				continue
			}
			// Lines longer than maxLineSize arrive split into parts;
			// they fail to parse and are counted as invalid.
			h(ParseLine(l.Text))
		}
	}
}

func (f *File) replay(ctx context.Context, h Handler) error {
	file, err := os.Open(f.Path)
	if err != nil {
		return err
	}
	defer file.Close()

	sc := bufio.NewScanner(file)
	sc.Buffer(make([]byte, 64*1024), maxLineSize)
	for sc.Scan() {
		if ctx.Err() != nil {
			return nil
		}
		h(ParseLine(sc.Text()))
	}
	return sc.Err()
}
