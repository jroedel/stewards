package page

import (
	"errors"

	"github.com/jroedel/stewards/business/types"
)

// ContactFields is what the "contact-fields" partial draws: the email and
// phone a volunteer leaves on any form that will write back to them -- the
// email list and a stewardship day's sign-up alike, which the stewards asked
// to go hand in hand. A page calls it with
// {{template "contact-fields" (pass $l .Contact)}}.
//
// It is in the layout's templates rather than one app's, because an app's
// partials are its own (render.go) and two apps draw these fields; the home
// page used to carry a copy of the list's form, kept alike by a comment.
type ContactFields struct {
	// ID prefixes the fields' ids, so that two forms on one page differ.
	ID string

	// Email and Phone are what was typed, shown again after a problem.
	Email, Phone string

	// Texts is whether the phone field is offered at all: it is only when
	// the app can send a text.
	Texts bool

	// Problem is what was wrong, and Field which field it was about:
	// "email", "phone", or "" for neither having been given.
	Problem types.Text
	Field   string
}

// ContactProblem turns what types.ParseContact refused into the sentence
// for the form and the field to put it beside. An error that is not a
// ContactInvalid is the neither-given sentence, which is the safe thing to
// show for a refusal this does not recognise.
func (c *ContactFields) ContactProblem(err error) {
	invalid, _ := errors.AsType[types.ContactInvalid](err)

	c.Field = invalid.Field

	switch invalid.Field {
	case "email":
		c.Problem = contact.NotAnEmail
	case "phone":
		c.Problem = contact.NotAPhone
	default:
		if c.Texts {
			c.Problem = contact.Neither
		} else {
			c.Problem = contact.NoEmail
		}
	}
}

// ContactWords is the catalog's entry for these, which the muxer lists with
// the apps' own.
var ContactWords = Copy{
	Where: "what a volunteer is told when the email or phone number they left on a form will not do, read on a phone",
	Words: contact,
}

var contact = struct {
	NotAnEmail, NotAPhone, Neither, NoEmail types.Text
}{
	NotAnEmail: types.Text{EN: "That doesn't look like an email address. Check it for a typo."},
	NotAPhone:  types.Text{EN: "That doesn't look like a mobile number. Check the digits; a number from outside the US starts with + and its country code."},
	Neither:    types.Text{EN: "Leave an email, a mobile number, or both, so we can reach you."},
	NoEmail:    types.Text{EN: "Leave your email, so we can reach you."},
}
