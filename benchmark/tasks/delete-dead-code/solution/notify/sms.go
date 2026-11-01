package notify

import (
	"fmt"
	"unicode/utf8"
)

// maxSMSLength is the longest body a single SMS may carry.
const maxSMSLength = 160

// SendSMS sends a short text message.
func SendSMS(t Transport, number, body string) error {
	if utf8.RuneCountInString(body) > maxSMSLength {
		return fmt.Errorf("sms body has %d characters, limit is %d", utf8.RuneCountInString(body), maxSMSLength)
	}
	return t.Deliver("sms", number, body)
}
