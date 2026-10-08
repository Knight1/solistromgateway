package push

import (
	"strings"
	"testing"
)

func TestRedactKeepsOnlySchemeAndHost(t *testing.T) {
	const raw = "https://push.example.com/api/v2/11111111-2222-3333-4444-555555555555/generic-push/66666666-7777-8888-9999-000000000000?code=aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

	got := Redact(raw)

	if got != "https://push.example.com/..." {
		t.Errorf("got %q", got)
	}
	for _, secret := range []string{"code=", "11111111", "66666666", "aaaaaaaa", "generic-push"} {
		if strings.Contains(got, secret) {
			t.Errorf("redacted URL leaks %q: %s", secret, got)
		}
	}
}

// Review Focus: redaction runs on error paths, including for malformed URLs.
func TestRedactMalformedInput(t *testing.T) {
	for _, in := range []string{"", "not a url", "://", "%%%", "https://"} {
		got := Redact(in)
		if got == "" {
			t.Errorf("Redact(%q) returned an empty string", in)
		}
		if strings.Contains(got, "code=") {
			t.Errorf("Redact(%q) leaked a query string: %s", in, got)
		}
	}
}

func TestRedactDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Redact panicked: %v", r)
		}
	}()
	Redact("https://user:password@host/path?code=secret")
}

func TestRedactDropsUserInfo(t *testing.T) {
	if got := Redact("https://user:password@push.example.com/p?code=K"); strings.Contains(got, "password") {
		t.Errorf("credentials leaked: %s", got)
	}
}
