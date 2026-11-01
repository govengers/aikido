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

// renderTemplateV1 is the old $key syntax renderer.
func renderTemplateV1(tpl string, vars map[string]string) string {
	out := tpl
	for k, v := range vars {
		out = strings.ReplaceAll(out, "$"+k, escapeV1(v))
	}
	return out
}

// escapeV1 is the hand-rolled escaping used by renderTemplateV1 only.
func escapeV1(s string) string {
	return strings.NewReplacer("<", "&lt;", ">", "&gt;").Replace(s)
}

// Welcome renders the welcome email for a new user.
func Welcome(name string) Email {
	return Email{
		Subject: "Welcome",
		Body:    renderTemplate("Hello {{name}}, welcome aboard.", map[string]string{"name": name}),
	}
}
