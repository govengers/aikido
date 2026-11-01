package notify

import (
	"strings"
	"testing"
)

type recorder struct{ got []string }

func (r *recorder) Deliver(channel, recipient, payload string) error {
	r.got = append(r.got, channel+"|"+recipient+"|"+payload)
	return nil
}

func TestSendEmail(t *testing.T) {
	var r recorder
	if err := SendEmail(&r, Email{To: "a@example.com", Subject: "Hi", Body: "there"}); err != nil {
		t.Fatal(err)
	}
	if err := SendEmail(&r, Email{To: "not-an-address"}); err == nil {
		t.Fatal("invalid address accepted")
	}
	if len(r.got) != 1 || r.got[0] != "email|a@example.com|Hi\n\nthere" {
		t.Fatalf("deliveries = %q", r.got)
	}
}

func TestSendSMS(t *testing.T) {
	var r recorder
	if err := SendSMS(&r, "+49123", "ok"); err != nil {
		t.Fatal(err)
	}
	if err := SendSMS(&r, "+49123", strings.Repeat("x", 161)); err == nil {
		t.Fatal("long sms accepted")
	}
}

func TestWelcome(t *testing.T) {
	if got := Welcome("<Ann>").Body; got != "Hello &lt;Ann&gt;, welcome aboard." {
		t.Fatalf("Welcome body = %q", got)
	}
}
