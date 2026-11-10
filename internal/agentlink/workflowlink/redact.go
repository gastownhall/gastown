package workflowlink

import (
	"net/url"
	"regexp"
	"strings"
)

// RedactRemote returns a git remote URL safe to send to the server (R34):
// user info (user:token@), the query string and the fragment are removed
// from URLs with a scheme ("https://u:tok@host/r.git?token=x" becomes
// "https://host/r.git"). scp-style remotes (git@host:o/r.git) and local
// paths carry no secret and are returned unchanged. A URL that does not
// parse, or a scheme-less string that still looks like it carries
// credentials, is replaced by "" rather than risk sending a secret.
//
// util.RedactURL strips user info only; it keeps the query string, which
// can hold a token, so it is not enough here.
func RedactRemote(raw string) string {
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		// scp-style (user@host:path) or a local path. Anything with a query
		// marker, a password-looking "user:pw@", or whitespace is not a
		// remote gtb wrote; drop it.
		if strings.ContainsAny(raw, "?# \t") {
			return ""
		}
		if at := strings.Index(raw, "@"); at >= 0 && strings.Contains(raw[:at], ":") {
			return ""
		}
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.User = nil
	u.RawQuery, u.ForceQuery = "", false
	u.Fragment, u.RawFragment = "", ""
	return u.String()
}

// urlInTextRe matches scheme://... URLs inside free-form text, up to
// whitespace or a quote.
var urlInTextRe = regexp.MustCompile("[A-Za-z][A-Za-z0-9+.-]*://[^\\s'\"<>`]+")

// Bare-secret patterns (R37), applied after URL redaction. Each keeps its
// first group and replaces the secret with "<redacted>".
var secretRes = []*regexp.Regexp{
	// Authorization: Bearer <x> / Basic <x>
	regexp.MustCompile(`(?i)(authorization:\s*(?:bearer|basic)\s+)[^\s'"]+`),
	// token=, access_token=, password=, passwd=, secret= and env-style
	// *_TOKEN=, *_SECRET=, *_PASSWORD= (query or env style, quoted or not).
	regexp.MustCompile(`(?i)(\b(?:[A-Za-z0-9_]*_(?:token|secret|password)|token|access_token|password|passwd|secret)=)(?:"[^"]*"|'[^']*'|[^\s&'"]+)`),
	// user:secret@host without a scheme (x-access-token:<x>@github.com).
	regexp.MustCompile(`(\b[A-Za-z0-9._-]+:)[^\s@:/'"]+(@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b)`),
}

// tokenRes match whole secrets that need no key (GitHub tokens).
var tokenRes = []*regexp.Regexp{
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`),
}

// SafeTail returns s safe to send when it may have been cut at its start
// (R38): a secret whose key or prefix was cut off ("…SSWORD=hunter2",
// "…rization: Bearer …") no longer matches any RedactText pattern, so when
// truncated is true everything up to and including the first newline is
// dropped ("" when there is none). The rest goes through RedactText.
func SafeTail(s string, truncated bool) string {
	if truncated {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			return ""
		}
		s = s[i+1:]
	}
	return RedactText(s)
}

// RedactText removes secrets from free-form text that leaves the machine
// (command output tails, terminal captures, error messages):
//   - every scheme://URL goes through RedactRemote (user info, query and
//     fragment dropped; "<redacted-url>" when it cannot be cleaned);
//   - "Authorization: Bearer|Basic <x>", key=value secrets (token,
//     access_token, password, passwd, secret, *_TOKEN, *_SECRET,
//     *_PASSWORD; case-insensitive) and "user:<x>@host" keep their key and
//     have the value replaced by "<redacted>";
//   - GitHub tokens (gh[pousr]_..., github_pat_...) become "<redacted>".
func RedactText(s string) string {
	s = urlInTextRe.ReplaceAllStringFunc(s, func(m string) string {
		if r := RedactRemote(m); r != "" {
			return r
		}
		return "<redacted-url>"
	})
	for _, re := range secretRes {
		if re.NumSubexp() == 2 {
			s = re.ReplaceAllString(s, "${1}<redacted>${2}")
		} else {
			s = re.ReplaceAllString(s, "${1}<redacted>")
		}
	}
	for _, re := range tokenRes {
		s = re.ReplaceAllString(s, "<redacted>")
	}
	return s
}
