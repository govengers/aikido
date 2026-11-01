package wordstat

import (
	"reflect"
	"testing"
)

func TestWords(t *testing.T) {
	got := Words("Hello, hello world! 42 times-over")
	want := []string{"hello", "hello", "world", "42", "times", "over"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Words = %v, want %v", got, want)
	}
	if Count("a b  c") != 3 {
		t.Fatal("Count")
	}
}
