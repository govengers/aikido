// Package stack provides a generic LIFO stack.
package stack

// Stack is a LIFO stack. The zero value is an empty stack ready to use.
type Stack[T any] struct {
	items []T
}

// Push adds v on top of the stack.
func (s *Stack[T]) Push(v T) {
	s.items = append(s.items, v)
}

// Pop removes and returns the top element. ok is false when the stack is
// empty.
func (s *Stack[T]) Pop() (v T, ok bool) {
	if len(s.items) == 0 {
		return v, false
	}
	v = s.items[len(s.items)-1]
	s.items = s.items[:len(s.items)-1]
	return v, true
}

// Len returns the number of elements.
func (s *Stack[T]) Len() int { return len(s.items) }
