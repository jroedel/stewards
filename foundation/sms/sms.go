// Package sms sends one text message through Twilio's REST API.
//
// It knows nothing about what it is sending or why: a number, a body, one
// request. The rules about who may be texted and how often are the caller's.
//
// # What crosses the wire
//
// The number and the body, to Twilio, over TLS. Twilio and the carrier then
// hold both, and Twilio's console shows the body for as long as the account
// keeps its logs. There is no way of sending a text where that is not so, and
// it is said here so that nobody infers otherwise from its absence.
//
// # No SDK
//
// Twilio publishes a Go library and this does not use it. What is needed is
// one form-encoded POST and one JSON reply, and a vendor SDK with its own
// dependency tree would be the largest change to this module's trust path
// for less code than its import block. The adapter is eumaeus's
// (business/domain/comms/sources/twilio), which has been sending to real
// numbers since 2026-09, cut down to what this app does: no delivery
// callback, because nothing here listens for one.
//
// # An API key, not the account's token
//
// It authenticates with a Standard API key (an SK... SID and its secret) on
// the account it sends for, rather than the account's own auth token. The
// account is shared with eumaeus; a key minted for this app alone can be
// revoked without touching eumaeus, and a Standard key cannot reach the
// account or key resources, so a leaked one cannot mint another. Twilio's
// docs: "Use your API key SID as the username and your API key secret as the
// password" (twilio.com/docs/usage/requests-to-twilio), and the URL still
// names the account.
package sms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// apiBase is Twilio's REST endpoint. Config.BaseURL replaces it in tests.
const apiBase = "https://api.twilio.com"

// timeout is how long one message is given. Ten seconds is long enough that
// a slow moment at Twilio is not reported as a failure, and short enough that
// a volunteer pressing "I'll be there" is not left looking at a spinner.
const timeout = 10 * time.Second

// MaxBody is the longest body Twilio accepts, in characters. A longer one is
// refused here rather than there, which is the same answer sooner.
const MaxBody = 1600

// codeOptedOut is Twilio's 21610: "Attempt to send to unsubscribed
// recipient", a number that has texted STOP to the sending number.
const codeOptedOut = 21610

// ErrOptedOut is a refusal because the number has texted STOP. It is the one
// refusal a caller must act on rather than log: the person has said no, and
// the only way this app learns of it is the next send being refused, since
// replies to the shared number go to eumaeus rather than here.
var ErrOptedOut = errors.New("that number has replied STOP to our texts")

// ErrUnsendable is a message this package will not send, as opposed to one
// Twilio refused: no number, no body, or a body too long for a text.
var ErrUnsendable = errors.New("that text cannot be sent")

// Config is where to send from, and as whom.
type Config struct {
	// AccountSID is the account the messages belong to (AC...).
	AccountSID string

	// KeySID and KeySecret are the API key this app authenticates with.
	KeySID    string
	KeySecret string

	// From is the number messages come from, in E.164.
	From string

	// BaseURL replaces Twilio's endpoint, for tests.
	BaseURL string

	// Client is the HTTP client; nil makes one with the timeout above.
	Client *http.Client
}

// Message is one text to one number.
type Message struct {
	// To is the recipient in E.164 ("+15125550123").
	To string

	Body string
}

// Sender sends a text. It returns Twilio's ID for the message, or an error;
// a refusal by Twilio is a [Refused], and errors.Is(err, ErrOptedOut) says
// whether it was a STOP.
type Sender interface {
	Send(ctx context.Context, m Message) (string, error)
}

// Refused is Twilio declining a message: an opted-out number, a landline, a
// number that does not exist. It is an outcome of one send, not a fault of
// this program, and a caller sending to many records it and carries on.
type Refused struct {
	// Code is Twilio's error code, such as 21610 or 21614.
	Code int

	// Message is Twilio's own sentence, collapsed to one line.
	Message string
}

func (r Refused) Error() string {
	return fmt.Sprintf("the text was refused (Twilio %d): %s", r.Code, r.Message)
}

// Is makes errors.Is(err, ErrOptedOut) true for a 21610.
func (r Refused) Is(target error) bool {
	return target == ErrOptedOut && r.Code == codeOptedOut
}

// Twilio sends texts through Twilio.
type Twilio struct {
	cfg    Config
	client *http.Client
	base   string
}

var _ Sender = (*Twilio)(nil)

// NewTwilio checks the configuration and builds a sender. It sends nothing:
// a key that is wrong is found at the first text, as with the mail relay.
func NewTwilio(cfg Config) (*Twilio, error) {
	for name, v := range map[string]string{
		"an account SID": cfg.AccountSID, "an API key SID": cfg.KeySID,
		"an API key secret": cfg.KeySecret, "a number to send from": cfg.From,
	} {
		if strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("sending texts needs %s", name)
		}
	}

	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}

	base := cfg.BaseURL
	if base == "" {
		base = apiBase
	}

	return &Twilio{cfg: cfg, client: client, base: strings.TrimRight(base, "/")}, nil
}

// Twilio's two reply shapes, and they cannot be one struct: both use the key
// "status", and disagree about its type -- "queued" on an accepted message,
// 400 on a refusal. Decoded into one struct, every refusal fails to
// unmarshal, and a 21610 is never seen as a code. Eumaeus learnt that from a
// test; so the HTTP status chooses the shape.
type accepted struct {
	SID string `json:"sid"`

	// ErrorCode is set on a message Twilio took and at once knew would
	// fail. A pointer, because null and zero are different answers.
	ErrorCode    *int   `json:"error_code"`
	ErrorMessage string `json:"error_message"`
}

type refused struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Send hands one message to Twilio and returns its message SID.
//
// A 4xx is a [Refused]. A 5xx, a network failure, or a reply that is not
// Twilio's is a plain error, and nothing here retries it: a retried text that
// did in fact go is a second copy on a real person's phone.
func (t *Twilio) Send(ctx context.Context, m Message) (string, error) {
	switch {
	case m.To == "":
		return "", fmt.Errorf("%w: there is no number to send it to", ErrUnsendable)
	case strings.TrimSpace(m.Body) == "":
		return "", fmt.Errorf("%w: it is empty", ErrUnsendable)
	case utf8.RuneCountInString(m.Body) > MaxBody:
		return "", fmt.Errorf("%w: it is longer than %d characters", ErrUnsendable, MaxBody)
	}

	form := url.Values{"To": {m.To}, "From": {t.cfg.From}, "Body": {m.Body}}
	endpoint := t.base + "/2010-04-01/Accounts/" + url.PathEscape(t.cfg.AccountSID) + "/Messages.json"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("building the request to Twilio: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(t.cfg.KeySID, t.cfg.KeySecret)

	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("reaching Twilio: %w", err)
	}
	defer resp.Body.Close()

	// Capped: the reply is a few hundred bytes, and this is a response from
	// the internet that could, misdirected, be anything that streams.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("reading Twilio's reply: %w", err)
	}

	if resp.StatusCode >= 400 {
		var bad refused
		if err := json.Unmarshal(body, &bad); err != nil {
			// Reported by its status, not its contents: a body that is not
			// JSON is a proxy or a captive portal, not Twilio.
			return "", fmt.Errorf("the reply to a text was a %d that did not come from Twilio", resp.StatusCode)
		}

		if resp.StatusCode >= 500 {
			return "", fmt.Errorf("the text did not go: Twilio failed with %d: %s", resp.StatusCode, oneLine(bad.Message))
		}

		return "", Refused{Code: bad.Code, Message: oneLine(bad.Message)}
	}

	var ok accepted
	if err := json.Unmarshal(body, &ok); err != nil {
		return "", fmt.Errorf("the reply to a text was a %d that did not come from Twilio", resp.StatusCode)
	}

	if ok.ErrorCode != nil && *ok.ErrorCode != 0 {
		return ok.SID, Refused{Code: *ok.ErrorCode, Message: oneLine(ok.ErrorMessage)}
	}

	if ok.SID == "" {
		return "", errors.New("the reply from Twilio named no message, so whether the text went is unknown")
	}

	return ok.SID, nil
}

// oneLine collapses a provider's sentence so it cannot break a log line.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// Recorder is a Sender that keeps texts instead of sending them, for tests
// and for a development run with no Twilio. In this package rather than a
// test file for the reason mail.Recorder is: the development path and the
// tested path should be the same code.
//
// Unlike mail.Recorder it is safe to share between goroutines, because the
// sends to a whole list run in the background while a test reads.
type Recorder struct {
	mu   sync.Mutex
	sent []Message

	// Refuse, when set, is asked about each message first, and its error is
	// returned instead of recording it -- a Refused{Code: 21610} is how a
	// test plays a volunteer who has texted STOP.
	Refuse func(Message) error
}

var _ Sender = (*Recorder)(nil)

// Send records the message.
func (r *Recorder) Send(_ context.Context, m Message) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.Refuse != nil {
		if err := r.Refuse(m); err != nil {
			return "", err
		}
	}

	r.sent = append(r.sent, m)

	return fmt.Sprintf("SMrecorded%d", len(r.sent)), nil
}

// Sent is every message recorded so far, oldest first.
func (r *Recorder) Sent() []Message {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]Message(nil), r.sent...)
}

// Last is the most recent message, which is what a test almost always wants.
func (r *Recorder) Last() (Message, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.sent) == 0 {
		return Message{}, false
	}

	return r.sent[len(r.sent)-1], true
}

// gsm is the GSM 03.38 basic character set, which a text is sent in when
// every character of it is here: 160 to a segment. One character outside it
// -- á, í, ó or ú, which Spanish is full of, or an emoji -- and the whole
// text goes as UCS-2 instead, 70 to a segment. ñ, é and ü are in it; the
// other Spanish vowels are not.
const gsm = "@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?" +
	"¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà"

// gsmExtended are in the set by an escape, and count as two.
const gsmExtended = "^{}\\[~]|€"

// Segments is how many text messages body goes as, and whether it needs
// UCS-2 to do it. Each segment is billed, and a long text arrives in pieces
// on an old phone, so a steward writing one is shown the count. Twilio's
// own rule: one segment up to 160 GSM characters or 70 UCS-2 units, and
// beyond that 153 or 67 to a segment, the rest being the header that joins
// them.
func Segments(body string) (n int, unicode bool) {
	units := 0

	for _, r := range body {
		switch {
		case strings.ContainsRune(gsm, r):
			units++
		case strings.ContainsRune(gsmExtended, r):
			units += 2
		default:
			unicode = true
		}
	}

	if unicode {
		// UTF-16 code units: an emoji outside the basic plane is two.
		units = 0
		for _, r := range body {
			if r > 0xFFFF {
				units += 2
			} else {
				units++
			}
		}

		return segmentsOf(units, 70, 67), true
	}

	return segmentsOf(units, 160, 153), false
}

func segmentsOf(units, single, multi int) int {
	switch {
	case units == 0:
		return 0
	case units <= single:
		return 1
	default:
		return (units + multi - 1) / multi
	}
}
