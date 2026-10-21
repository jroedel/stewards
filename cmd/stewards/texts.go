package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sms"
)

// checkTexts is -check-texts: whether the [sms] this binary was given can
// send, asked from wherever it runs -- on the server, through make
// prod-texts-check, which is the point: the config the app runs with, the
// server's own way out to the internet, and the app's own sender, so that a
// pass here is a pass for the welcome a volunteer is waiting for.
//
// It asks Twilio whether the from number is the account's, which sends
// nothing and costs nothing. Given a number to, it then sends that number
// one text, and watches for up to wait to say whether it arrived: the one
// thing a lookup cannot tell, and on the number shared with eumaeus the one
// most likely to go wrong (30007, a carrier filtering it).
//
// It never prints the key, and prints numbers redacted, since its output is
// pasted into conversations.
func checkTexts(ctx context.Context, out io.Writer, t *sms.Twilio, from string, to string, wait, every time.Duration) error {
	fromNumber, _ := types.ParsePhone(from)

	if err := t.Owns(ctx); err != nil {
		return err
	}

	fmt.Fprintf(out, "ok   Twilio accepts the API key, and %s is on the account\n", fromNumber.Redacted())

	if to == "" {
		fmt.Fprintln(out, "     nothing was sent; give a number to send one test text to it")

		return nil
	}

	phone, err := types.ParsePhone(to)
	if err != nil {
		return fmt.Errorf("the number to send the test to: %w", err)
	}

	sid, err := t.Send(ctx, sms.Message{To: phone.String(), Body: "Garden stewards: a test text from the app. Nothing to do; it is checking that texts reach phones."})
	if err != nil {
		if errors.Is(err, sms.ErrOptedOut) {
			return fmt.Errorf("%s has replied STOP to this number, so Twilio will not text it until it replies START", phone.Redacted())
		}

		return fmt.Errorf("the test text was not sent: %w", err)
	}

	fmt.Fprintf(out, "ok   Twilio took a test text to %s (%s)\n", phone.Redacted(), sid)

	deadline := time.Now().Add(wait)

	for {
		status, code, err := t.Status(ctx, sid)
		if err != nil {
			return err
		}

		switch status {
		case "delivered", "read":
			fmt.Fprintln(out, "ok   the carrier says it was delivered")

			return nil
		case "undelivered", "failed":
			return fmt.Errorf("the test text was %s (Twilio %d): %s", status, code, meaning(code))
		}

		if !time.Now().Before(deadline) {
			fmt.Fprintf(out, "     Twilio still has it as %q after %s; carriers can take longer. If the phone does not get it, look the text up in the Twilio console\n", status, wait)

			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(every):
		}
	}
}

// meaning is what a delivery error code means for this app, for the ones
// most likely here.
func meaning(code int) string {
	switch code {
	case 30007:
		return "a carrier filtered it. On the number shared with eumaeus this is the risk the stewards accepted; a number of the app's own, registered for volunteer texts, is the fix"
	case 30003:
		return "the phone is off or out of reach; try again later"
	case 30005:
		return "the number does not exist, or is no longer in service"
	case 30006:
		return "that is a landline, which cannot get texts"
	case 30034:
		return "the sending number's carrier registration (A2P 10DLC) does not cover it"
	default:
		return "see twilio.com/docs/api/errors/" + fmt.Sprint(code)
	}
}
