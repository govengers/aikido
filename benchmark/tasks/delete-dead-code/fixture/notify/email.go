package notify

import (
	"fmt"
	"net/mail"
	"strings"
)

// Email is one outgoing message.
type Email struct {
	To      string
	Subject string
	Body    string
}

// SendEmail validates the recipient and hands the message to the transport.
func SendEmail(t Transport, e Email) error {
	if _, err := mail.ParseAddress(e.To); err != nil {
		return fmt.Errorf("invalid recipient %q: %w", e.To, err)
	}
	return t.Deliver("email", e.To, e.Subject+"\n\n"+e.Body)
}

// formatLegacySubject prefixes subjects the way the v1 mailer did.
// Deprecated: the v1 mailer is gone; nothing calls this any more.
func formatLegacySubject(subject string) string {
	return "[NOTIFY] " + strings.ToUpper(subject)
}
