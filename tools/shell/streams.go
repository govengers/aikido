package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"unicode/utf8"
)

// errWrongDirection is returned when a script reads a stream opened for
// writing or the other way round.
var errWrongDirection = errors.New("bad file descriptor")

// noInput is the stdin of a command without one: it reads as empty.
type noInput struct{}

func (noInput) Read([]byte) (int, error) { return 0, io.EOF }

// emptyInput is the stdin of a command that has none, like </dev/null.
func emptyInput() io.Reader { return noInput{} }

// pipeReader returns an OS pipe that yields content. The interpreter hands an
// *os.File to commands without a copying goroutine of its own; the writing
// goroutine here ends when content is written or the reader is closed (the
// interpreter closes it after the statement).
func pipeReader(content []byte) (*os.File, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("aikido/tools/shell: pipe: %w", err)
	}
	go func() {
		defer writer.Close()
		// A write error means the reader stopped early, like a command that
		// does not read its stdin; nothing waits for the rest.
		_, _ = writer.Write(content)
	}()
	return reader, nil
}

// bufferedFile is a redirect target: writes collect in memory, counted
// against the Shell's memory budget, and are stored with one WriteFile. The
// store happens when the redirect closes, or earlier, when the command that
// owns the redirect ends (flush), so a failed store can still fail that
// command before && or || decide. The interpreter ignores Close errors, so a
// failed store is reported on stderr here, once. Safe for concurrent writes
// (a background job and the script may share a redirect).
type bufferedFile struct {
	mutex sync.Mutex
	// label starts a message about the file: "bash: line N: name".
	label string
	// owner is the offset+1 of the simple command whose statement opened
	// the redirect (cmd >file), 0 for a compound statement; see flushOwned.
	owner   uint
	content []byte
	// reserved is the part of content charged to budget: what is not
	// stored yet.
	reserved int
	budget   *memoryBudget
	// commit stores content; storeContext bounds it.
	commit       func(ctx context.Context, content []byte) error
	storeContext func() (context.Context, context.CancelFunc)
	// onClose runs once when the file closes, to forget it.
	onClose func(*bufferedFile)
	stderr  io.Writer

	dirty    bool  // content changed since the last store
	failure  error // the last store's error, reported once
	reported bool
	closed   bool
}

func (f *bufferedFile) Read([]byte) (int, error) { return 0, errWrongDirection }

// Write appends data, or fails like a full disk when the memory budget is
// spent and with "File too large" beyond the per-file cap; the writer
// reports those itself.
func (f *bufferedFile) Write(data []byte) (int, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	switch {
	case f.closed:
		return 0, os.ErrClosed
	case len(f.content)+len(data) > maximumBufferedFileBytes:
		return 0, errTooLarge
	case !f.budget.reserve(len(data)):
		return 0, errNoSpace
	}
	f.reserved += len(data)
	f.content = append(f.content, data...)
	f.dirty = true
	return len(data), nil
}

// flush stores the content now if it changed, with its own deadline, and
// returns the store's failure (reported on stderr once).
func (f *bufferedFile) flush() error {
	ctx, cancel := f.storeContext()
	defer cancel()
	f.mutex.Lock()
	defer f.mutex.Unlock()
	return f.storeLocked(ctx)
}

func (f *bufferedFile) storeLocked(ctx context.Context) error {
	if f.dirty {
		// The stored bytes are charged where they are stored (/tmp) or
		// are the workspace's: the buffer no longer counts them.
		f.budget.release(f.reserved)
		f.reserved, f.dirty = 0, false
		f.failure = f.commit(ctx, f.content)
		f.reported = false
	}
	if f.failure != nil && !f.reported {
		f.reported = true
		fmt.Fprintf(f.stderr, "%s: %v\n", f.label, describe(f.failure))
	}
	return f.failure
}

// Close stores the content with the file's own deadline.
func (f *bufferedFile) Close() error {
	ctx, cancel := f.storeContext()
	defer cancel()
	return f.closeWith(ctx)
}

// closeWith stores what is left within ctx and releases the buffer;
// closing a closed file does nothing.
func (f *bufferedFile) closeWith(ctx context.Context) error {
	f.mutex.Lock()
	if f.closed {
		f.mutex.Unlock()
		return nil
	}
	f.closed = true
	err := f.storeLocked(ctx)
	f.budget.release(f.reserved)
	f.reserved, f.content = 0, nil
	f.mutex.Unlock()
	if f.onClose != nil {
		f.onClose(f)
	}
	return err
}

// streamFile is /dev/stdin, /dev/stdout or /dev/stderr: it passes through to
// the shell's stream and does not close it.
type streamFile struct {
	reader io.Reader
	writer io.Writer
}

func (f *streamFile) Read(data []byte) (int, error) {
	if f.reader == nil {
		return 0, errWrongDirection
	}
	return f.reader.Read(data)
}

func (f *streamFile) Write(data []byte) (int, error) {
	if f.writer == nil {
		return 0, errWrongDirection
	}
	return f.writer.Write(data)
}

func (f *streamFile) Close() error { return nil }

// boundedOutput collects what a script prints for the model: the first and
// the last half of the limit, the middle cut. Safe for concurrent writes
// (both sides of a pipeline write stderr).
type boundedOutput struct {
	mutex sync.Mutex
	limit int // bytes; negative keeps everything
	head  []byte
	tail  []byte
	total int
}

func newBoundedOutput(limit int) *boundedOutput {
	return &boundedOutput{limit: limit}
}

func (o *boundedOutput) Write(data []byte) (int, error) {
	o.mutex.Lock()
	defer o.mutex.Unlock()
	written := len(data)
	o.total += written
	if o.limit < 0 {
		o.head = append(o.head, data...)
		return written, nil
	}
	headLimit := o.limit / 2
	if room := headLimit - len(o.head); room > 0 {
		taken := min(room, len(data))
		o.head = append(o.head, data[:taken]...)
		data = data[taken:]
	}
	o.tail = append(o.tail, data...)
	if tailLimit := o.limit - headLimit; len(o.tail) > 2*tailLimit {
		o.tail = append(o.tail[:0], o.tail[len(o.tail)-tailLimit:]...)
	}
	return written, nil
}

// result returns the collected output and whether its middle was cut.
func (o *boundedOutput) result() (string, bool) {
	o.mutex.Lock()
	defer o.mutex.Unlock()
	if o.limit < 0 || o.total <= o.limit {
		return string(o.head) + string(o.tail), false
	}
	// The marker counts against the limit; its length is bounded by the
	// one for cutting everything.
	budget := max(o.limit-len(cutMarker(o.total)), 0)
	head := trimIncompleteRune(o.head[:min(len(o.head), budget/2)])
	tail := o.tail[max(len(o.tail)-(budget-len(head)), 0):]
	for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
		tail = tail[1:]
	}
	return string(head) + cutMarker(o.total-len(head)-len(tail)) + string(tail), true
}

// cutMarker replaces the middle of an output that was too long.
func cutMarker(cut int) string {
	return fmt.Sprintf("\n... [%d bytes of output cut] ...\n", cut)
}

// trimIncompleteRune drops a UTF-8 sequence the cut left unfinished at the
// end of data.
func trimIncompleteRune(data []byte) []byte {
	for back := 1; back <= utf8.UTFMax && back <= len(data); back++ {
		if start := len(data) - back; utf8.RuneStart(data[start]) {
			if !utf8.FullRune(data[start:]) {
				return data[:start]
			}
			return data
		}
	}
	return data
}

// maximumSubstitutionBytes caps the output of $(...): the interpreter
// collects it in memory. A variable for tests.
var maximumSubstitutionBytes = maximumBufferedFileBytes

// substitutionWriter is a command's stdout inside $(...): the interpreter's
// buffer for the whole substitution, refused with "File too large" once it
// would hold more than maximumSubstitutionBytes.
type substitutionWriter struct {
	buffer *strings.Builder
}

func (w substitutionWriter) Write(data []byte) (int, error) {
	if w.buffer.Len()+len(data) > maximumSubstitutionBytes {
		return 0, errTooLarge
	}
	return w.buffer.Write(data)
}
