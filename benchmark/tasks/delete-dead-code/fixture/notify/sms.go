package notify

import (
	"fmt"
	"net"
	"time"
	"unicode/utf8"
)

// maxSMSLength is the longest body a single SMS may carry.
const maxSMSLength = 160

// legacySMSGateway is the address of the SMS gateway retired in 2024.
const legacySMSGateway = "sms-gw.internal:2775"

// SendSMS sends a short text message.
func SendSMS(t Transport, number, body string) error {
	if utf8.RuneCountInString(body) > maxSMSLength {
		return fmt.Errorf("sms body has %d characters, limit is %d", utf8.RuneCountInString(body), maxSMSLength)
	}
	return t.Deliver("sms", number, body)
}

// dialLegacyGateway opened a raw connection to the old gateway.
func dialLegacyGateway() (net.Conn, error) {
	return net.DialTimeout("tcp", legacySMSGateway, 5*time.Second)
}
