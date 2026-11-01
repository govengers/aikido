package notify

import (
	"html"
	"strings"
)

// Transport delivers a rendered message on one channel.
type Transport interface {
	Deliver(channel, recipient, payload string) error
}

// renderTemplate replaces {{key}} placeholders with HTML-escaped values.
func renderTemplate(tpl string, vars map[string]string) string {
	out := tpl
	for k, v := range vars {
		out = strings.ReplaceAll(out, "{{"+k+"}}", html.EscapeString(v))
	}
	return out
}

// Welcome renders the welcome email for a new user.
func Welcome(name string) Email {
	return Email{
		Subject: "Welcome",
		Body:    renderTemplate("Hello {{name}}, welcome aboard.", map[string]string{"name": name}),
	}
}
