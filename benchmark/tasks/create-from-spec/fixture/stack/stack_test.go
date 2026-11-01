package stack

import "testing"

func TestStack(t *testing.T) {
	var s Stack[int]
	s.Push(1)
	s.Push(2)
	if v, ok := s.Pop(); !ok || v != 2 {
		t.Fatalf("Pop = %d, %v", v, ok)
	}
	if s.Len() != 1 {
		t.Fatalf("Len = %d", s.Len())
	}
}
