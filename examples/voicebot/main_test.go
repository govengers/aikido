package main

import "testing"

// The example's vocabulary must fix the corpus names and leave ordinary
// sentences of a bathroom-planning conversation alone.
func TestNewVocabulary(t *testing.T) {
	t.Parallel()
	vocabulary := newVocabulary()
	for _, testCase := range []struct{ input, want string }{
		{"Ist der Vok 60 g von Warnot und Selbig lieferbar?", "Ist der VOK60G von VARNOTH & ZELBIG lieferbar?"},
		{"Die Fliese hat ein schönes Muster und man sieht es sofort.", "Die Fliese hat ein schönes Muster und man sieht es sofort."},
		{"Dieses Muster kann ich mir gut vorstellen.", "Dieses Muster kann ich mir gut vorstellen."},
		{"Welches Muster dann am besten passt, weiß ich nicht.", "Welches Muster dann am besten passt, weiß ich nicht."},
		{"Ich mag das Muster, wann kommt der Monteur?", "Ich mag das Muster, wann kommt der Monteur?"},
		{"Der Wasserdruck ist zu niedrig.", "Der Wasserdruck ist zu niedrig."},
		{"Wir wollen eine Walk-in-Dusche und einen Unterschrank.", "Wir wollen eine Walk-in-Dusche und einen Unterschrank."},
	} {
		if got := vocabulary.Correct(testCase.input); got != testCase.want {
			t.Errorf("Correct(%q) = %q, want %q", testCase.input, got, testCase.want)
		}
	}
}
