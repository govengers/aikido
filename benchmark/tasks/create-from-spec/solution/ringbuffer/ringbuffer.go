// Package ringbuffer provides a generic fixed-capacity FIFO ring buffer.
package ringbuffer

// Buffer is a fixed-capacity FIFO ring buffer.
type Buffer[T any] struct {
	items []T
	head  int
	n     int
}

// New returns an empty buffer. It panics if capacity < 1.
func New[T any](capacity int) *Buffer[T] {
	if capacity < 1 {
		panic("ringbuffer: capacity must be at least 1")
	}
	return &Buffer[T]{items: make([]T, capacity)}
}

// Push appends v; when full the oldest element is evicted and returned.
func (b *Buffer[T]) Push(v T) (evicted T, ok bool) {
	if b.n == len(b.items) {
		evicted, ok = b.items[b.head], true
		b.items[b.head] = v
		b.head = (b.head + 1) % len(b.items)
		return evicted, ok
	}
	b.items[(b.head+b.n)%len(b.items)] = v
	b.n++
	return evicted, false
}

// Pop removes and returns the oldest element.
func (b *Buffer[T]) Pop() (v T, ok bool) {
	if b.n == 0 {
		return v, false
	}
	var zero T
	v = b.items[b.head]
	b.items[b.head] = zero
	b.head = (b.head + 1) % len(b.items)
	b.n--
	return v, true
}

// Peek returns the oldest element without removing it.
func (b *Buffer[T]) Peek() (v T, ok bool) {
	if b.n == 0 {
		return v, false
	}
	return b.items[b.head], true
}

// Len returns the number of stored elements.
func (b *Buffer[T]) Len() int { return b.n }

// Cap returns the capacity.
func (b *Buffer[T]) Cap() int { return len(b.items) }

// Items returns a copy of the elements from oldest to newest.
func (b *Buffer[T]) Items() []T {
	out := make([]T, b.n)
	for i := range out {
		out[i] = b.items[(b.head+i)%len(b.items)]
	}
	return out
}
