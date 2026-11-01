package wordstat

import (
	"reflect"
	"testing"
)

func TestBenchTopWords(t *testing.T) {
	text := "the cat and the dog and THE bird. Bird? bird! zebra 2 2"
	got := TopWords(text, 4)
	want := []WordCount{{"bird", 3}, {"the", 3}, {"2", 2}, {"and", 2}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TopWords(text, 4) = %v, want %v", got, want)
	}
	all := TopWords(text, 100)
	if len(all) != 7 {
		t.Fatalf("TopWords(text, 100) has %d entries, want 7: %v", len(all), all)
	}
	if last := all[len(all)-1]; last != (WordCount{"zebra", 1}) {
		t.Fatalf("last entry = %v, want {zebra 1}", last)
	}
	for _, c := range []struct {
		text string
		n    int
	}{{text, 0}, {text, -3}, {"", 5}, {"...!", 5}} {
		got := TopWords(c.text, c.n)
		if got == nil || len(got) != 0 {
			t.Errorf("TopWords(%q, %d) = %#v, want empty non-nil", c.text, c.n, got)
		}
	}
}
