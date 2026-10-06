// Package page holds what this app puts in a <head> and in its response
// headers.
//
// It sits one layer above foundation/ because a policy is a product decision
// rather than plumbing: what may be cached, what a page is allowed to load.
// foundation/web.SecureHeaders takes the policy as a value for exactly that
// reason, and nothing in foundation/ may know a domain word. This comment is
// here so nobody rediscovers why.
package page

import (
	"net/http"
	"strings"

	"github.com/jroedel/stewards/foundation/web"
)

// hsts is sent on every response. Apache adds one at the proxy too, so in
// production this is belt and braces, and it does not depend on a .htaccess
// file staying correct.
const hsts = "max-age=31536000; includeSubDomains"

// Policy is the whole header policy for this application.
//
// Script from this site only, and nothing inline. The first script is the
// inbox's send screen, which sends a batch of photos one at a time and shows
// how far it has got (inboxapp); it is a file served from this origin, so
// 'self' is all it needs, and connect-src 'self' is the fetch it sends them
// with. No 'unsafe-inline' and no nonce: a page that wants script links a
// file, and an injected <script> or onclick= still does nothing.
//
// This went in enforced rather than as a Report-Only rollout, which is what
// this comment used to ask for. A rollout is for a policy that might block
// script a page depends on; here the only script is an improvement on a form
// that works without it, so the worst a mistake in this policy can do is put
// the send screen back the way it was. Nothing in this app takes reports yet,
// either.
//
// What 'self' must never take in is a file somebody uploaded. Every response
// is nosniff (web.SecureHeaders), and every photo is served as a JPEG this app
// made from it, as image/jpeg, so a "photo" with script in it is never run.
//
// no-store is the starting point and not the settled answer. It is right for
// a steward's edit screen and wrong for a species card a volunteer wants on
// their phone with no signal. It changes per route when that route exists,
// which is why this is a function of the request.
func Policy() web.PolicyFor {
	return func(*http.Request) web.Policy {
		return web.Policy{
			ContentSecurityPolicy: strings.Join([]string{
				"default-src 'none'",
				"script-src 'self'",
				"connect-src 'self'",
				"style-src 'self'",

				// Self-hosted, never from a font service: a stylesheet that
				// pulls a font from somebody else's origin tells them who
				// opened which page, from which address.
				"font-src 'self'",
				"img-src 'self' data:",

				// The inbox's manifest, which is what lets Chrome install
				// the app and offer it in the phone's share sheet. Without
				// this it falls to default-src and is refused. The share
				// target's worker needs no line of its own: worker-src
				// falls back to script-src, and that is 'self'.
				"manifest-src 'self'",
				"form-action 'self'",
				"base-uri 'none'",
				"frame-ancestors 'none'",
			}, "; "),

			ReferrerPolicy:  "same-origin",
			CacheControl:    "no-store",
			FrameOptions:    "DENY",
			StrictTransport: hsts,
		}
	}
}
