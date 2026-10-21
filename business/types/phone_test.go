package types_test

import (
	"errors"
	"testing"

	"github.com/jroedel/stewards/business/types"
)

// The numbers here are invented: 555-01xx is the block set aside for
// fiction, and never a volunteer's.

func TestParsePhoneAccepts(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		// The ways one North American number gets typed, all one person.
		{"5125550123", "+15125550123"},
		{"(512) 555-0123", "+15125550123"},
		{"512.555.0123", "+15125550123"},
		{"1 512 555 0123", "+15125550123"},
		{"+1 (512) 555-0123", "+15125550123"},
		{"  512-555-0123\n", "+15125550123"},
		{"tel:+15125550123", "+15125550123"},

		// Elsewhere, with its country code, as a Mexican or a European
		// address book writes it.
		{"+52 55 5555 0123", "+525555550123"},
		{"0052 55 5555 0123", "+525555550123"},
		{"+44 20 7946 0123", "+442079460123"},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := types.ParsePhone(tt.in)
			if err != nil {
				t.Fatalf("ParsePhone(%q): %v", tt.in, err)
			}
			if got.String() != tt.want {
				t.Errorf("ParsePhone(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestParsePhoneRefuses(t *testing.T) {
	for _, in := range []string{
		"",
		"   ",
		"call me",
		"555-0123",          // no area code
		"512 155 0123",      // an exchange cannot begin with 1
		"(012) 555-0123",    // nor an area code with 0
		"2 512 555 0123",    // eleven digits that do not begin with 1
		"+0 512 555 0123",   // a lost country code
		"+1234567890123456", // sixteen digits
		"+12345",            // too short
		"５１２５５５０１２３",        // fullwidth digits are not quietly converted
	} {
		t.Run(in, func(t *testing.T) {
			if p, err := types.ParsePhone(in); !errors.Is(err, types.ErrNotAPhone) {
				t.Errorf("ParsePhone(%q) = %q, %v; want ErrNotAPhone", in, p, err)
			}
		})
	}
}

func TestPhoneForms(t *testing.T) {
	p, err := types.ParsePhone("512 555 0123")
	if err != nil {
		t.Fatal(err)
	}

	if got, want := p.National(), "(512) 555-0123"; got != want {
		t.Errorf("National = %q, want %q", got, want)
	}

	// The log form keeps four digits and nothing a person could dial.
	if got, want := p.Redacted(), "+•••••••0123"; got != want {
		t.Errorf("Redacted = %q, want %q", got, want)
	}

	mx, err := types.ParsePhone("+52 55 5555 0123")
	if err != nil {
		t.Fatal(err)
	}

	if got, want := mx.National(), "+525555550123"; got != want {
		t.Errorf("National of a Mexican number = %q, want it left in E.164", got)
	}

	var zero types.Phone
	if !zero.Zero() || zero.Redacted() != "(none)" || zero.National() != "" {
		t.Errorf("the zero Phone: Zero %v, Redacted %q, National %q", zero.Zero(), zero.Redacted(), zero.National())
	}
}

func TestParseContact(t *testing.T) {
	both, err := types.ParseContact(" Ana@Example.invalid ", "(512) 555-0123")
	if err != nil {
		t.Fatal(err)
	}

	if both.Email.String() != "ana@example.invalid" || both.Phone.String() != "+15125550123" {
		t.Errorf("both = %q and %q", both.Email, both.Phone)
	}

	if got, want := both.Redacted(), "email and phone +•••••••0123"; got != want {
		t.Errorf("Redacted = %q, want %q", got, want)
	}

	if c, err := types.ParseContact("", "512 555 0123"); err != nil || !c.Email.Zero() || c.Phone.Zero() {
		t.Errorf("phone only = %+v, %v", c, err)
	}

	if c, err := types.ParseContact("ana@example.invalid", "  "); err != nil || c.Email.Zero() || !c.Phone.Zero() {
		t.Errorf("email only = %+v, %v", c, err)
	}

	tests := []struct {
		name, email, phone, field string
		is                        error
	}{
		{"neither", " ", "", "", types.ErrNoContact},
		{"a bad email beside a good phone", "ana@", "512 555 0123", "email", types.ErrNotAnEmail},
		{"a bad phone beside a good email", "ana@example.invalid", "555-0123", "phone", types.ErrNotAPhone},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := types.ParseContact(tt.email, tt.phone)

			invalid, ok := errors.AsType[types.ContactInvalid](err)
			if !ok {
				t.Fatalf("err = %v, want a ContactInvalid", err)
			}
			if invalid.Field != tt.field || !errors.Is(err, tt.is) {
				t.Errorf("err = %v on %q, want %v on %q", err, invalid.Field, tt.is, tt.field)
			}
		})
	}
}
