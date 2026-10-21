package sms_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/stewards/foundation/sms"
)

// Invented credentials and numbers. An account SID is "AC" and thirty-two
// hex characters; this one is assembled so it is plainly not a real one.
const (
	accountSID = "AC" + "00000000000000000000" + "000000000001"
	keySID     = "SK" + "00000000000000000000" + "000000000002"
	keySecret  = "a-key-secret"
	from       = "+15125550100"
	to         = "+15125550123"
)

// serve starts a fake Twilio answering with h, and a sender pointed at it.
func serve(t *testing.T, h http.HandlerFunc) *sms.Twilio {
	t.Helper()

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	s, err := sms.NewTwilio(sms.Config{
		AccountSID: accountSID, KeySID: keySID, KeySecret: keySecret, From: from,
		BaseURL: srv.URL, Client: srv.Client(),
	})
	if err != nil {
		t.Fatalf("NewTwilio: %v", err)
	}

	return s
}

func reply(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprint(w, body)
}

func TestSendPostsWhatTwilioExpects(t *testing.T) {
	var (
		method, path, ctype, user, pass string
		form                            url.Values
	)

	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		method, path, ctype = r.Method, r.URL.Path, r.Header.Get("Content-Type")
		user, pass, _ = r.BasicAuth()
		_ = r.ParseForm()
		form = r.PostForm
		reply(w, http.StatusCreated, `{"sid": "SM123", "status": "queued", "error_code": null}`)
	})

	sid, err := s.Send(t.Context(), sms.Message{To: to, Body: "See you Saturday at 8."})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	switch {
	case sid != "SM123":
		t.Errorf("sid = %q", sid)
	case method != http.MethodPost:
		t.Errorf("method = %s", method)
	case path != "/2010-04-01/Accounts/"+accountSID+"/Messages.json":
		// The URL names the account even though the key authenticates.
		t.Errorf("path = %s", path)
	case ctype != "application/x-www-form-urlencoded":
		t.Errorf("content type = %s", ctype)
	case user != keySID || pass != keySecret:
		t.Errorf("basic auth = %q:%q, want the API key and its secret", user, pass)
	case form.Get("To") != to || form.Get("From") != from || form.Get("Body") != "See you Saturday at 8.":
		t.Errorf("form = %v", form)
	}
}

func TestARefusalIsRefusedAndAStopIsOptedOut(t *testing.T) {
	// A refusal's "status" is a number where an accepted message's is a
	// string: the reason the two shapes are decoded separately.
	s := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		reply(w, http.StatusBadRequest, `{"code": 21610, "message": "Attempt to send\n to unsubscribed recipient", "status": 400}`)
	})

	_, err := s.Send(t.Context(), sms.Message{To: to, Body: "hello"})

	refused, ok := errors.AsType[sms.Refused](err)
	if !ok {
		t.Fatalf("err = %v, want a Refused", err)
	}

	if refused.Code != 21610 || refused.Message != "Attempt to send to unsubscribed recipient" {
		t.Errorf("refused = %+v", refused)
	}

	if !errors.Is(err, sms.ErrOptedOut) {
		t.Error("a 21610 is not ErrOptedOut")
	}

	// Any other refusal is not a STOP.
	other := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		reply(w, http.StatusBadRequest, `{"code": 21614, "message": "not a mobile number", "status": 400}`)
	})

	if _, err := other.Send(t.Context(), sms.Message{To: to, Body: "hello"}); errors.Is(err, sms.ErrOptedOut) {
		t.Errorf("a 21614 is ErrOptedOut: %v", err)
	}
}

func TestAnAcceptedMessageThatAlreadyFailedIsRefused(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		reply(w, http.StatusCreated, `{"sid": "SM9", "status": "failed", "error_code": 30007, "error_message": "filtered"}`)
	})

	sid, err := s.Send(t.Context(), sms.Message{To: to, Body: "hello"})
	if refused, ok := errors.AsType[sms.Refused](err); !ok || refused.Code != 30007 || sid != "SM9" {
		t.Errorf("Send = %q, %v; want SM9 and a 30007", sid, err)
	}
}

func TestFailuresThatAreNotRefusals(t *testing.T) {
	tests := []struct {
		name string
		h    http.HandlerFunc
	}{
		{"a 5xx", func(w http.ResponseWriter, _ *http.Request) {
			reply(w, http.StatusServiceUnavailable, `{"code": 20500, "message": "down"}`)
		}},
		{"a reply that is not Twilio's", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, "<html>captive portal</html>")
		}},
		{"an accepted reply with no SID", func(w http.ResponseWriter, _ *http.Request) {
			reply(w, http.StatusCreated, `{"status": "queued"}`)
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := serve(t, tt.h).Send(t.Context(), sms.Message{To: to, Body: "hello"})
			if err == nil {
				t.Fatal("Send succeeded")
			}
			if _, ok := errors.AsType[sms.Refused](err); ok {
				t.Errorf("err = %v is a Refused; a caller would record it as the person's answer", err)
			}
		})
	}
}

func TestAProviderThatNeverAnswersIsLetGo(t *testing.T) {
	// Released when the test ends. Waiting on the request's context alone
	// hangs srv.Close: the server notices a client hanging up only once
	// the body has been read, and this handler never reads it.
	done := make(chan struct{})
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-done:
		}
	})
	t.Cleanup(func() { close(done) })

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	if _, err := s.Send(ctx, sms.Message{To: to, Body: "hello"}); err == nil {
		t.Fatal("Send succeeded")
	}
}

func TestWhatIsNeverSent(t *testing.T) {
	called := false
	s := serve(t, func(w http.ResponseWriter, _ *http.Request) { called = true })

	for name, m := range map[string]sms.Message{
		"no number": {Body: "hello"},
		"no body":   {To: to, Body: "  "},
		"too long":  {To: to, Body: strings.Repeat("ñ", sms.MaxBody+1)},
	} {
		if _, err := s.Send(t.Context(), m); !errors.Is(err, sms.ErrUnsendable) {
			t.Errorf("%s: err = %v, want ErrUnsendable", name, err)
		}
	}

	if called {
		t.Error("Twilio was asked")
	}
}

func TestNewTwilioWantsEverything(t *testing.T) {
	full := sms.Config{AccountSID: accountSID, KeySID: keySID, KeySecret: keySecret, From: from}

	for name, cfg := range map[string]sms.Config{
		"account": {KeySID: keySID, KeySecret: keySecret, From: from},
		"key":     {AccountSID: accountSID, KeySecret: keySecret, From: from},
		"secret":  {AccountSID: accountSID, KeySID: keySID, From: from},
		"from":    {AccountSID: accountSID, KeySID: keySID, KeySecret: keySecret},
	} {
		if _, err := sms.NewTwilio(cfg); err == nil {
			t.Errorf("no %s: NewTwilio succeeded", name)
		}
	}

	if _, err := sms.NewTwilio(full); err != nil {
		t.Errorf("NewTwilio: %v", err)
	}
}

func TestRecorder(t *testing.T) {
	var r sms.Recorder

	if _, ok := r.Last(); ok {
		t.Error("an empty Recorder has a Last")
	}

	r.Refuse = func(m sms.Message) error {
		if m.To == to {
			return sms.Refused{Code: 21610, Message: "stopped"}
		}
		return nil
	}

	if _, err := r.Send(t.Context(), sms.Message{To: to, Body: "a"}); !errors.Is(err, sms.ErrOptedOut) {
		t.Errorf("a refused number: %v", err)
	}

	if _, err := r.Send(t.Context(), sms.Message{To: from, Body: "b"}); err != nil {
		t.Fatal(err)
	}

	if last, _ := r.Last(); last.Body != "b" || len(r.Sent()) != 1 {
		t.Errorf("Sent = %+v", r.Sent())
	}
}

func TestSegments(t *testing.T) {
	tests := []struct {
		body    string
		n       int
		unicode bool
	}{
		{"", 0, false},
		{"See you Saturday at 8.", 1, false},
		{strings.Repeat("a", 160), 1, false},
		{strings.Repeat("a", 161), 2, false},
		{strings.Repeat("a", 306), 2, false},
		{strings.Repeat("a", 307), 3, false},
		{strings.Repeat("€", 80), 1, false}, // two each, 160 in all
		{strings.Repeat("€", 81), 2, false},

		// ñ and é are GSM; á is not, and turns the whole text to UCS-2.
		{"Mañana, café.", 1, false},
		{"Sábado", 1, true},
		{strings.Repeat("á", 70), 1, true},
		{strings.Repeat("á", 71), 2, true},
		{strings.Repeat("😀", 35), 1, true}, // two units each
		{strings.Repeat("😀", 36), 2, true},
	}

	for _, tt := range tests {
		if n, u := sms.Segments(tt.body); n != tt.n || u != tt.unicode {
			t.Errorf("Segments(%.20q…) = %d, %v; want %d, %v", tt.body, n, u, tt.n, tt.unicode)
		}
	}
}

func TestOwnsSaysWhichPartIsWrong(t *testing.T) {
	tests := []struct {
		name string
		h    http.HandlerFunc
		want string
	}{
		{"the number is the account's", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/2010-04-01/Accounts/"+accountSID+"/IncomingPhoneNumbers.json" || r.URL.Query().Get("PhoneNumber") != from {
				t.Errorf("asked %s %s", r.Method, r.URL)
			}
			if u, p, _ := r.BasicAuth(); u != keySID || p != keySecret {
				t.Errorf("auth %q:%q", u, p)
			}
			reply(w, http.StatusOK, `{"incoming_phone_numbers": [{"phone_number": "+15125550100"}]}`)
		}, ""},
		{"a wrong key", func(w http.ResponseWriter, _ *http.Request) {
			reply(w, http.StatusUnauthorized, `{"code": 20003, "message": "Authenticate"}`)
		}, "did not accept the API key"},
		{"a wrong account", func(w http.ResponseWriter, _ *http.Request) {
			reply(w, http.StatusNotFound, `{"code": 20404, "message": "not found"}`)
		}, "no such account"},
		{"a number that is someone else's", func(w http.ResponseWriter, _ *http.Request) {
			reply(w, http.StatusOK, `{"incoming_phone_numbers": []}`)
		}, "not on this Twilio account"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := serve(t, tt.h).Owns(t.Context())

			switch {
			case tt.want == "" && err != nil:
				t.Errorf("Owns: %v", err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
				t.Errorf("Owns = %v, want it to say %q", err, tt.want)
			}
		})
	}
}

func TestStatusReadsTheMessage(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/2010-04-01/Accounts/" + accountSID + "/Messages/SM1.json":
			reply(w, http.StatusOK, `{"sid": "SM1", "status": "delivered", "error_code": null}`)
		case "/2010-04-01/Accounts/" + accountSID + "/Messages/SM2.json":
			reply(w, http.StatusOK, `{"sid": "SM2", "status": "undelivered", "error_code": 30007}`)
		default:
			reply(w, http.StatusNotFound, `{"code": 20404, "message": "not found"}`)
		}
	})

	if status, code, err := s.Status(t.Context(), "SM1"); status != "delivered" || code != 0 || err != nil {
		t.Errorf("SM1 = %q %d %v", status, code, err)
	}

	if status, code, err := s.Status(t.Context(), "SM2"); status != "undelivered" || code != 30007 || err != nil {
		t.Errorf("SM2 = %q %d %v", status, code, err)
	}

	if _, _, err := s.Status(t.Context(), "SM3"); err == nil {
		t.Error("an unknown message has a status")
	}
}
