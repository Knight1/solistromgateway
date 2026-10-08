package push

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"testing"
	"time"

	"github.com/Knight1/solistromgateway/internal/source"
)

func TestPushSendsExactPayload(t *testing.T) {
	var gotBody, gotType, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody, gotType, gotMethod = string(b), r.Header.Get("Content-Type"), r.Method
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	err := New().Push(context.Background(), srv.URL, source.Reading{ProducingWatt: source.Int(814)})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if gotType != "application/json" {
		t.Errorf("content-type = %q", gotType)
	}
	// Absent fields must not appear at all. Sending "soc":0 would tell
	// Solistrom the battery is flat.
	if gotBody != `{"producingWatt":814}` {
		t.Errorf("body = %s, want {\"producingWatt\":814}", gotBody)
	}
}

func TestPushAcceptsAny2xx(t *testing.T) {
	for _, code := range []int{200, 202, 204} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))
		err := New().Push(context.Background(), srv.URL, source.Reading{ProducingWatt: source.Int(1)})
		srv.Close()
		if err != nil {
			t.Errorf("status %d: %v", code, err)
		}
	}
}

func TestPushErrorIncludesStatusAndBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "device not found", http.StatusNotFound)
	}))
	defer srv.Close()

	err := New().Push(context.Background(), srv.URL, source.Reading{ProducingWatt: source.Int(1)})
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "device not found") {
		t.Errorf("error should carry status and body, got %v", err)
	}
}

func TestPushErrorNeverLeaksTheURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()

	err := New().Push(context.Background(), srv.URL+"/p?code=SUPERSECRET", source.Reading{ProducingWatt: source.Int(1)})
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "SUPERSECRET") {
		t.Errorf("error leaks the key: %v", err)
	}
}

func TestPushErrorBodyEchoingTheURLDoesNotLeak(t *testing.T) {
	// Many servers put the request target in their error body. When the push
	// endpoint does that, the body carries the key, so the body has to be
	// scrubbed as carefully as the URL itself.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no route for "+r.URL.RequestURI(), http.StatusNotFound)
	}))
	defer srv.Close()

	secret := srv.URL + "/api/v2/ACC/generic-push/DEV?code=SUPERSECRETKEYVALUE"
	err := New().Push(context.Background(), secret, source.Reading{ProducingWatt: source.Int(1)})
	if err == nil {
		t.Fatal("want an error for a 404")
	}
	if strings.Contains(err.Error(), "SUPERSECRETKEYVALUE") {
		t.Errorf("the key leaked through the response body: %v", err)
	}
	if strings.Contains(err.Error(), "generic-push/DEV") {
		t.Errorf("the device id leaked through the response body: %v", err)
	}
}

func TestPushDoesNotFollowRedirects(t *testing.T) {
	// A captive portal answering a redirect with a 200 login page would make
	// Push return nil if redirects were followed: the reading would be
	// silently discarded while the log reported success. The push endpoint
	// never redirects, so a 3xx here must surface as an error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login", http.StatusFound)
	}))
	defer srv.Close()

	err := New().Push(context.Background(), srv.URL, source.Reading{ProducingWatt: source.Int(1)})
	if err == nil {
		t.Fatal("want an error for a redirect, got nil")
	}
	if !strings.Contains(err.Error(), "302") {
		t.Errorf("error should mention 302, got %v", err)
	}
}

func TestPushRefusesEmptyReading(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	err := New().Push(context.Background(), srv.URL, source.Reading{})
	if err == nil {
		t.Fatal("want an error for an empty reading")
	}
	if called {
		t.Error("an empty reading must not reach the network")
	}
}

func TestPushHonoursContextCancellation(t *testing.T) {
	// The handler is held open until the test releases it. Relying on the
	// server to notice the client went away does not work here: an unread
	// request body keeps the server from spotting the disconnect, so the
	// request context would never fire and Close would block forever.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()    // runs last
	defer close(release) // runs first, so the handler can always exit

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := New().Push(ctx, srv.URL, source.Reading{ProducingWatt: source.Int(1)})
	if err == nil {
		t.Fatal("want an error when the context expires")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Push ignored the deadline, took %v", elapsed)
	}
}

func TestPushRequestBuildErrorNeverLeaksTheURL(t *testing.T) {
	// A push URL pasted with a stray newline makes net/url refuse it, and the
	// resulting error quotes the whole URL. The key must not survive that.
	secret := "https://push.example.com/api/v2/ACC/generic-push/DEV?code=SUPERSECRETKEY\n"

	err := New().Push(context.Background(), secret, source.Reading{ProducingWatt: source.Int(1)})
	if err == nil {
		t.Fatal("want an error for a malformed push URL")
	}
	if strings.Contains(err.Error(), "SUPERSECRETKEY") {
		t.Errorf("the key leaked into the error: %v", err)
	}
	if u := errors.Unwrap(err); u != nil && strings.Contains(u.Error(), "SUPERSECRETKEY") {
		t.Errorf("the key is reachable through errors.Unwrap: %v", u)
	}
}

func TestPushTransportErrorNeverLeaksTheURL(t *testing.T) {
	// Nothing is listening here, so the transport fails and puts the URL it
	// dialled into its own error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	dead := srv.URL
	srv.Close() // free the port so the connection is refused

	secret := dead + "/api/v2/ACC/generic-push/DEV?code=SUPERSECRETKEY"
	err := New().Push(context.Background(), secret, source.Reading{ProducingWatt: source.Int(1)})
	if err == nil {
		t.Fatal("want an error when the connection is refused")
	}
	if strings.Contains(err.Error(), "SUPERSECRETKEY") {
		t.Errorf("the key leaked into the error: %v", err)
	}
	if strings.Contains(err.Error(), "generic-push") {
		t.Errorf("the device path leaked into the error: %v", err)
	}
}

func TestPushDrainsSuccessBody(t *testing.T) {
	// A success body longer than the error cap must still be read to EOF, or
	// the transport cannot reuse the connection. Pin that directly with an
	// httptrace GotConn hook: push twice against one server and require the
	// second request's connection was reused rather than freshly dialled.
	//
	// The body has to exceed 256KB. net/http's own Transport will silently
	// salvage a connection by draining a *small* unread body on Close (up to
	// 256KB, within a 50ms budget - see maxPostCloseReadBytes in
	// net/http/transport.go), which would let this test pass even with our
	// drain deleted. A body past that threshold defeats the stdlib's own
	// fallback, so only our explicit drain can make reuse happen.
	big := strings.Repeat("x", 300*1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(big))
	}))
	defer srv.Close()

	c := New()
	if err := c.Push(context.Background(), srv.URL, source.Reading{ProducingWatt: source.Int(1)}); err != nil {
		t.Fatalf("Push 0: %v", err)
	}

	var reused bool
	trace := &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused },
	}
	ctx := httptrace.WithClientTrace(context.Background(), trace)
	if err := c.Push(ctx, srv.URL, source.Reading{ProducingWatt: source.Int(1)}); err != nil {
		t.Fatalf("Push 1: %v", err)
	}

	if !reused {
		t.Error("the second push did not reuse the connection; the body was not drained")
	}
}

func TestPushFragmentedMalformedURLDoesNotLeak(t *testing.T) {
	// net/url strips the fragment before reporting a parse error, so the error
	// quotes a truncated URL that a naive search for the whole string misses.
	secret := "https://push.example.com/api/v2/ACC/generic-push/DEV?code=SUPERSECRETKEY\n#frag"

	err := New().Push(context.Background(), secret, source.Reading{ProducingWatt: source.Int(1)})
	if err == nil {
		t.Fatal("want an error for a malformed push URL")
	}
	if strings.Contains(err.Error(), "SUPERSECRETKEY") {
		t.Errorf("the key leaked into the error: %v", err)
	}
	if strings.Contains(err.Error(), "cause suppressed") {
		t.Errorf("the fragment form was not recognised, so the whole cause was dropped: %v", err)
	}
}

func TestScrubSuppressesWhatItCannotRedact(t *testing.T) {
	// The fail-safe: if a cause mentions the query in a form the replacements
	// did not catch, the whole cause is dropped instead of being logged.
	raw := "https://push.example.com/p?code=SUPERSECRETKEYVALUE"
	// A cause that holds the query but not any whole form of the URL.
	got := scrub("dial failed for host with code=SUPERSECRETKEYVALUE appended", raw)

	if strings.Contains(got, "SUPERSECRETKEYVALUE") {
		t.Errorf("scrub returned a message still carrying the key: %s", got)
	}
}

func TestScrubKeepsUsefulCauses(t *testing.T) {
	// The fail-safe must not fire on ordinary transport errors, or the most
	// useful log line a person gets when their network is broken disappears.
	raw := "https://push.example.com/p?code=abcdef12-3456-7890-abcd-ef1234567890"
	cause := `Post "` + raw + `": dial tcp 10.0.0.9:443: connect: connection refused`

	got := scrub(cause, raw)

	if strings.Contains(got, "abcdef12") {
		t.Errorf("key survived: %s", got)
	}
	if !strings.Contains(got, "connection refused") {
		t.Errorf("the useful part of the cause was lost: %s", got)
	}
}

func TestScrubHandlesDegenerateURLs(t *testing.T) {
	// An empty or fragment-only URL must not make scrub splice its replacement
	// between every character of the message.
	for _, raw := range []string{"", "#frag"} {
		got := scrub(`Post "": unsupported protocol scheme ""`, raw)
		if strings.Contains(got, "p<") || strings.Count(got, "<unparseable") > 1 {
			t.Errorf("scrub(%q) corrupted the message: %s", raw, got)
		}
		if !strings.Contains(got, "unsupported protocol scheme") {
			t.Errorf("scrub(%q) lost the cause: %s", raw, got)
		}
	}
}
