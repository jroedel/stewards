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
// No script-src, deliberately. Nothing is served yet, and every page planned
// for Phase 1 renders on the server. The day a page genuinely needs script --
// working offline under the canopy is the likely one, per design.md -- this
// grows a directive and deserves a Report-Only rollout rather than a quiet
// edit, because a CSP that blocks your own script fails silently in a way that
// looks like the feature is broken.
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
				"style-src 'self'",

				// Self-hosted, never from a font service: a stylesheet that
				// pulls a font from somebody else's origin tells them who
				// opened which page, from which address.
				"font-src 'self'",
				"img-src 'self' data:",
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
