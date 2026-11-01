Create a new Go package `ringbuffer` in the file `ringbuffer/ringbuffer.go` of this module (module path `example.com/collections`), following the style of the existing `stack` package. Specification:

- `type Buffer[T any] struct` - a fixed-capacity FIFO ring buffer.
- `func New[T any](capacity int) *Buffer[T]` - returns an empty buffer. Panics if `capacity < 1`.
- `func (b *Buffer[T]) Push(v T) (evicted T, ok bool)` - appends `v` as the newest element. If the buffer was already full, the oldest element is removed to make room and returned with `ok == true`; otherwise `ok` is false and `evicted` is the zero value.
- `func (b *Buffer[T]) Pop() (v T, ok bool)` - removes and returns the oldest element; `ok` is false (and `v` the zero value) when the buffer is empty.
- `func (b *Buffer[T]) Peek() (v T, ok bool)` - returns the oldest element without removing it; `ok` is false when empty.
- `func (b *Buffer[T]) Len() int` - number of stored elements.
- `func (b *Buffer[T]) Cap() int` - the capacity passed to `New`.
- `func (b *Buffer[T]) Items() []T` - a newly allocated slice of all stored elements from oldest to newest (modifying it must not affect the buffer); an empty buffer returns an empty, non-nil slice.

Push, Pop and Peek must run in O(1) (do not shift a slice on every operation). The package needs no external dependencies. `go vet ./...` and `go test ./...` must pass.
