package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/stewards/foundation/sms"
)

// fakeTwilio answers the lookup, the send, and the status, the last with
// status after the first ask.
func fakeTwilio(t *testing.T, owns bool, status string, code int) (*sms.Twilio, *int) {
	t.Helper()

	sent := 0
	asked := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.HasSuffix(r.URL.Path, "/IncomingPhoneNumbers.json"):
			if owns {
				fmt.Fprint(w, `{"incoming_phone_numbers": [{"phone_number": "+15125550100"}]}`)
			} else {
				fmt.Fprint(w, `{"incoming_phone_numbers": []}`)
			}
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/Messages.json"):
			sent++
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"sid": "SMtest", "status": "queued", "error_code": null}`)
		case strings.HasSuffix(r.URL.Path, "/Messages/SMtest.json"):
			asked++
			if asked < 2 {
				fmt.Fprint(w, `{"status": "sent", "error_code": null}`)
				return
			}
			if code != 0 {
				fmt.Fprintf(w, `{"status": %q, "error_code": %d}`, status, code)
				return
			}
			fmt.Fprintf(w, `{"status": %q, "error_code": null}`, status)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"code": 20404, "message": "not found"}`)
		}
	}))
	t.Cleanup(srv.Close)

	tw, err := sms.NewTwilio(sms.Config{
		AccountSID: "AC1", KeySID: "SK1", KeySecret: "a-key-secret", From: "+15125550100",
		BaseURL: srv.URL, Client: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}

	return tw, &sent
}

func TestCheckTextsWithoutSending(t *testing.T) {
	tw, sent := fakeTwilio(t, true, "delivered", 0)

	var out strings.Builder
	if err := checkTexts(t.Context(), &out, tw, "+15125550100", "", time.Second, time.Millisecond); err != nil {
		t.Fatal(err)
	}

	if *sent != 0 || !strings.Contains(out.String(), "+•••••••0100 is on the account") || !strings.Contains(out.String(), "nothing was sent") {
		t.Errorf("sent %d:\n%s", *sent, out.String())
	}

	other, _ := fakeTwilio(t, false, "", 0)
	if err := checkTexts(t.Context(), &out, other, "+15125550100", "", time.Second, time.Millisecond); err == nil || !strings.Contains(err.Error(), "not on this Twilio account") {
		t.Errorf("a number not on the account: %v", err)
	}
}

func TestCheckTextsSendsOneAndWatchesIt(t *testing.T) {
	tw, sent := fakeTwilio(t, true, "delivered", 0)

	var out strings.Builder
	if err := checkTexts(t.Context(), &out, tw, "+15125550100", "(512) 555-0123", time.Second, time.Millisecond); err != nil {
		t.Fatal(err)
	}

	if *sent != 1 || !strings.Contains(out.String(), "delivered") || strings.Contains(out.String(), "5125550123") {
		t.Errorf("sent %d, and said (the number redacted):\n%s", *sent, out.String())
	}

	filtered, _ := fakeTwilio(t, true, "undelivered", 30007)
	if err := checkTexts(t.Context(), &out, filtered, "+15125550100", "512 555 0123", time.Second, time.Millisecond); err == nil || !strings.Contains(err.Error(), "carrier filtered it") {
		t.Errorf("filtered: %v", err)
	}

	slow, _ := fakeTwilio(t, true, "sent", 0)
	out.Reset()
	if err := checkTexts(t.Context(), &out, slow, "+15125550100", "512 555 0123", 10*time.Millisecond, time.Millisecond); err != nil || !strings.Contains(out.String(), "still has it") {
		t.Errorf("not yet delivered: %v\n%s", err, out.String())
	}

	if err := checkTexts(t.Context(), &out, tw, "+15125550100", "555-0123", time.Second, time.Millisecond); err == nil {
		t.Error("a test to a number with no area code was sent")
	}
}
