package push

import (
	"strings"
	"testing"
)

// FuzzScrub asserts the security property the whole package exists for: no
// matter what URL it is given and no matter what text comes back from a server,
// the result must not contain the URL's query, which is where the API key lives.
func FuzzScrub(f *testing.F) {
	f.Add("https://push.example.com/api/v2/A/generic-push/B?code=SECRET", "nothing here")
	f.Add("https://push.example.com/p?code=SECRET", `Post "https://push.example.com/p?code=SECRET": dial tcp: refused`)
	f.Add("https://push.example.com/p?code=SECRET#frag", `parse "https://push.example.com/p?code=SECRET": bad`)
	f.Add("", "")
	f.Add("://", "x")

	f.Fuzz(func(t *testing.T, raw, msg string) {
		got := scrub(msg, raw)

		query := rawQuery(raw)
		if query == "" {
			return
		}
		// A long query is the interesting case: a one or two character query can
		// legitimately appear in unrelated text.
		if len(query) < 8 {
			return
		}
		if strings.Contains(got, query) {
			t.Fatalf("scrub leaked the query\n raw:   %q\n msg:   %q\n got:   %q\n query: %q", raw, msg, got, query)
		}
	})
}

// FuzzRedact checks the one-line formatter never panics and never emits a query.
func FuzzRedact(f *testing.F) {
	f.Add("https://push.example.com/p?code=SECRET")
	f.Add("")
	f.Add("://")
	f.Add("https://user:pass@host/p?code=S")
	f.Add("%%%%")

	f.Fuzz(func(t *testing.T, raw string) {
		got := Redact(raw)
		if got == "" {
			t.Fatal("Redact returned an empty string, which would leave a log line with nothing in it")
		}
		if strings.Contains(got, "?") {
			t.Fatalf("Redact(%q) = %q, which still carries a query string", raw, got)
		}
	})
}

// FuzzRawPathAndQuery exercises the index arithmetic that splits a URL by hand,
// which has to cope with URLs net/url itself rejects.
func FuzzRawPathAndQuery(f *testing.F) {
	f.Add("https://host/path?query#frag")
	f.Add("host")
	f.Add("://")
	f.Add("#")
	f.Add("?")
	f.Add("https://")

	f.Fuzz(func(t *testing.T, raw string) {
		got := rawPathAndQuery(raw)
		if len(got) > len(raw) {
			t.Fatalf("rawPathAndQuery(%q) = %q, which is longer than its input", raw, got)
		}
	})
}
