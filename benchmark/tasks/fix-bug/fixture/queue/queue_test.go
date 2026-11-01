package queue

import (
	"errors"
	"testing"
)

func TestQueueFIFO(t *testing.T) {
	var q Queue
	if err := q.Submit("a", "2-3", 5); err != nil {
		t.Fatal(err)
	}
	if err := q.Submit("b", "1", 1); err != nil {
		t.Fatal(err)
	}
	j, err := q.Next()
	if err != nil || j.ID != "a" || len(j.Pages) != 2 {
		t.Fatalf("first job = %+v, %v", j, err)
	}
	j, err = q.Next()
	if err != nil || j.ID != "b" || len(j.Pages) != 1 {
		t.Fatalf("second job = %+v, %v", j, err)
	}
	if _, err := q.Next(); !errors.Is(err, ErrEmpty) {
		t.Fatalf("Next on empty queue = %v, want ErrEmpty", err)
	}
}
