// Package notices is what the apps that write to volunteers share: building
// a message in both its forms from the words in an app's catalog, and the
// list of every kind of message there is, for the stewards' screen that shows
// them before anything goes out.
//
// The words stay in each app -- the welcome is signupapp's, a day's
// confirmation the day app's -- and so does deciding when to send. What is
// here is the part two apps would otherwise each write, which is the layering
// rule's trigger for moving it down (CLAUDE.md, section 5).
package notices

import (
	"unicode/utf8"

	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/notice/noticebus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sms"
)

// Words is one kind of message, as copy: the subject and the email, and the
// text when the email is too long to be one. A Text left empty means the
// email goes as the text too -- write one only when the email will not fit
// noticebus.TextLimit, as the stewards asked.
type Words struct {
	Subject, Email, Text types.Text
}

// Build is the message in l, with pairs put into all three as page.Put puts
// them: "days", url, "leave", url.
func Build(r *page.Renderer, l types.Lang, w Words, pairs ...any) noticebus.Notice {
	n := noticebus.Notice{
		Subject: r.Plain(l, page.Put(w.Subject, pairs...)),
		Email:   r.Plain(l, page.Put(w.Email, pairs...)),
	}

	if w.Text.Written() {
		n.Text = r.Plain(l, page.Put(w.Text, pairs...))
	}

	return n
}

// Catalog is the copy for the translation memory, one entry per kind, each
// saying where it is read.
func (w Words) Catalog(where string) page.Copy {
	words := []types.Text{w.Subject, w.Email}
	if w.Text.Written() {
		words = append(words, w.Text)
	}

	return page.Copy{Where: where, Words: words}
}

// Kind is one kind of message, for the stewards' screen that shows every
// one: what it is called, when it goes, and a sample of it in a language.
// The sample is made with invented details -- a day that is not scheduled,
// links to nowhere real -- since the point is the words.
type Kind struct {
	Name, When types.Text
	Sample     func(l types.Lang) noticebus.Notice

	// Words is the kind's copy, so the screen can say when its Spanish is
	// still waiting for a translation and the sample is in English.
	Words Words
}

// Preview is one message in one language, both ways, described for a
// steward: the stewards' messages screen shows every kind like this, and the
// screens that send to many show the real message the same way before the
// button that sends it.
type Preview struct {
	Lang types.Text

	Subject, Email, Text string

	// Same is the text being the email word for word, shown once.
	Same bool

	// Segments is how many texts the text goes as, and Unicode whether a
	// character outside the plain set -- á, í, ó, ú -- made each shorter.
	Segments int
	Unicode  bool
	Chars    int

	// Waiting is l's words not translated yet, so the message would go in
	// the other language.
	Waiting bool
}

// PreviewOf is n, made from w in l, as a Preview.
func PreviewOf(r *page.Renderer, l types.Lang, w Words, n noticebus.Notice) Preview {
	p := Preview{
		Lang: page.Language(l), Subject: n.Subject, Email: n.Email,
		Text: n.TextBody(), Same: n.Same(),
	}

	p.Segments, p.Unicode = sms.Segments(p.Text)
	p.Chars = utf8.RuneCountInString(p.Text)

	_, shown := r.PlainShown(l, w.Email)
	p.Waiting = shown != l

	return p
}
