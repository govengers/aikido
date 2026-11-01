package main

import (
	"fmt"
	"os"

	"example.com/notify/notify"
)

type stdout struct{}

func (stdout) Deliver(channel, recipient, payload string) error {
	_, err := fmt.Printf("[%s] %s: %s\n", channel, recipient, payload)
	return err
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: notify <email>")
		os.Exit(2)
	}
	e := notify.Welcome("friend")
	e.To = os.Args[1]
	if err := notify.SendEmail(stdout{}, e); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = notify.SendSMS(stdout{}, "+0000", "welcome sent")
}
