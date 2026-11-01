package ringbuffer

import (
	"reflect"
	"testing"
)

func TestBenchRingBuffer(t *testing.T) {
	b := New[int](3)
	if b.Len() != 0 || b.Cap() != 3 {
		t.Fatalf("new buffer Len=%d Cap=%d", b.Len(), b.Cap())
	}
	if items := b.Items(); items == nil || len(items) != 0 {
		t.Fatalf("empty Items = %#v, want empty non-nil", items)
	}
	if _, ok := b.Pop(); ok {
		t.Fatal("Pop on empty returned ok")
	}
	if _, ok := b.Peek(); ok {
		t.Fatal("Peek on empty returned ok")
	}
	for i := 1; i <= 3; i++ {
		if ev, ok := b.Push(i); ok || ev != 0 {
			t.Fatalf("Push(%d) evicted %d, %v", i, ev, ok)
		}
	}
	if ev, ok := b.Push(4); !ok || ev != 1 {
		t.Fatalf("Push(4) evicted %d, %v, want 1, true", ev, ok)
	}
	if ev, ok := b.Push(5); !ok || ev != 2 {
		t.Fatalf("Push(5) evicted %d, %v, want 2, true", ev, ok)
	}
	if got := b.Items(); !reflect.DeepEqual(got, []int{3, 4, 5}) {
		t.Fatalf("Items = %v, want [3 4 5]", got)
	}
	items := b.Items()
	items[0] = 99
	if v, ok := b.Peek(); !ok || v != 3 {
		t.Fatalf("Peek = %d, %v after mutating Items copy", v, ok)
	}
	if v, ok := b.Pop(); !ok || v != 3 {
		t.Fatalf("Pop = %d, %v, want 3", v, ok)
	}
	b.Push(6)
	b.Push(7)
	if got := b.Items(); !reflect.DeepEqual(got, []int{5, 6, 7}) {
		t.Fatalf("Items after wrap = %v, want [5 6 7]", got)
	}
	for _, want := range []int{5, 6, 7} {
		if v, ok := b.Pop(); !ok || v != want {
			t.Fatalf("Pop = %d, %v, want %d", v, ok, want)
		}
	}
	if b.Len() != 0 {
		t.Fatalf("Len after drain = %d", b.Len())
	}
}

func TestBenchRingBufferStrings(t *testing.T) {
	b := New[string](1)
	b.Push("a")
	if ev, ok := b.Push("b"); !ok || ev != "a" {
		t.Fatalf("Push(b) evicted %q, %v", ev, ok)
	}
	if got := b.Items(); !reflect.DeepEqual(got, []string{"b"}) {
		t.Fatalf("Items = %v", got)
	}
}

func TestBenchRingBufferPanics(t *testing.T) {
	for _, c := range []int{0, -1} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("New(%d) did not panic", c)
				}
			}()
			New[int](c)
		}()
	}
}
