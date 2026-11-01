package shell

import (
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

// bufferedFile is a redirect target: writes collect in memory and Close
// stores them with one WriteFile. The interpreter ignores Close errors, so a
// failed store is reported on stderr here. Safe for concurrent writes (a
// background job and the script may share a redirect).
type bufferedFile struct {
	mutex sync.Mutex
	// label starts a message about the file: "bash: line N: name".
	label   string
	content []byte
	commit  func(content []byte) error
	stderr  io.Writer
	closed  bool
}

func (f *bufferedFile) Read([]byte) (int, error) { return 0, errWrongDirection }

func (f *bufferedFile) Write(data []byte) (int, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	if f.closed {
		return 0, os.ErrClosed
	}
	if len(f.content)+len(data) > maximumBufferedFileBytes {
		return 0, errTooLarge
	}
	f.content = append(f.content, data...)
	return len(data), nil
}

func (f *bufferedFile) Close() error {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	if err := f.commit(f.content); err != nil {
		fmt.Fprintf(f.stderr, "%s: %v\n", f.label, describe(err))
		return err
	}
	return nil
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
