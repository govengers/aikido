package accounts

import "testing"

func TestBenchNormalizeEmail(t *testing.T) {
	ok := map[string]string{
		" Mixed@Case.Example ": "mixed@case.example",
		"a@b.co":               "a@b.co",
		"first@sub.domain.io":  "first@sub.domain.io",
	}
	for in, want := range ok {
		got, err := normalizeEmail(in)
		if err != nil || got != want {
			t.Errorf("normalizeEmail(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	long := make([]byte, 250)
	for i := range long {
		long[i] = 'a'
	}
	bad := map[string]string{
		"   ":                  "email is required",
		string(long) + "@x.io": "email is too long",
		"nobody":               "email is missing @",
		"@example.com":         "email is missing @",
		"x@":                   "email is missing @",
		"x@localhost":          `email domain "localhost" has no dot`,
		"x@Mailinator.com":     `email domain "mailinator.com" is blocked`,
	}
	for in, want := range bad {
		if _, err := normalizeEmail(in); err == nil || err.Error() != want {
			t.Errorf("normalizeEmail(%q) error = %v, want %q", in, err, want)
		}
	}
}
