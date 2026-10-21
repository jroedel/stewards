package types

import (
	"errors"
	"fmt"
	"strings"
)

// Contact is how to reach a volunteer: an email address, a mobile number, or
// both. Whatever they gave, every message reaches them by -- a person who
// gave both gets the email and the text, because they said both.
//
// At least one, never neither. A form that took neither would put a name on
// a list nobody can reach, which for the email list is nothing and for a
// stewardship day is a promise of a reminder that cannot be kept.
type Contact struct {
	Email Email
	Phone Phone
}

// ErrNoContact is a contact with neither half.
var ErrNoContact = errors.New("there is no email address or phone number")

// ContactInvalid says which half of a contact was wrong, so a form can put
// the sentence beside the field that needs fixing. Field is "email" or
// "phone", or "" when neither was given.
type ContactInvalid struct {
	Field string
	Err   error
}

func (c ContactInvalid) Error() string {
	if c.Field == "" {
		return c.Err.Error()
	}

	return fmt.Sprintf("the %s: %v", c.Field, c.Err)
}

func (c ContactInvalid) Unwrap() error { return c.Err }

// ParseContact reads the two fields of a form. Either may be blank, not
// both. A half that is filled in and wrong is refused even when the other
// half is fine: someone who typed a phone number wants texts, and quietly
// dropping a mistyped one would leave them waiting for a reminder that never
// comes.
func ParseContact(email, phone string) (Contact, error) {
	var c Contact

	email, phone = strings.TrimSpace(email), strings.TrimSpace(phone)
	if email == "" && phone == "" {
		return Contact{}, ContactInvalid{Err: ErrNoContact}
	}

	if email != "" {
		e, err := ParseEmail(email)
		if err != nil {
			return Contact{}, ContactInvalid{Field: "email", Err: err}
		}

		c.Email = e
	}

	if phone != "" {
		p, err := ParsePhone(phone)
		if err != nil {
			return Contact{}, ContactInvalid{Field: "phone", Err: err}
		}

		c.Phone = p
	}

	return c, nil
}

// Zero reports whether there is no way to reach this contact.
func (c Contact) Zero() bool { return c.Email.Zero() && c.Phone.Zero() }

// Redacted is the contact for a log line: which halves there are, and the
// last four digits of the phone, never the address.
func (c Contact) Redacted() string {
	switch {
	case c.Email.Zero() && c.Phone.Zero():
		return "(none)"
	case c.Phone.Zero():
		return "email"
	case c.Email.Zero():
		return "phone " + c.Phone.Redacted()
	default:
		return "email and phone " + c.Phone.Redacted()
	}
}
