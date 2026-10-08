// Package push sends readings to the Solistrom generic push API.
package push

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Knight1/solistromgateway/internal/source"
)

// maxErrorBody caps how much of an error response is quoted in a log line.
const maxErrorBody = 200

// ErrEmptyReading is returned when there is nothing worth sending.
var ErrEmptyReading = errors.New("reading has no values to send")

// Client posts readings. One Client is safe for concurrent use by several
// device loops.
type Client struct {
	http *http.Client
}

// New builds a Client. Per-request deadlines come from the context, so the
// underlying client has no timeout of its own.
func New() *Client {
	return &Client{http: &http.Client{
		// The push endpoint never redirects. Following one would send the
		// reading somewhere else and, worse, let a captive portal's 200 look
		// like a successful push. Surface the 3xx as the error it is.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

// Push sends one reading. It makes a single attempt; retrying is the caller's
// decision, because only the caller knows how much of the tick is left.
//
// The API returns 202 Accepted on success. Any 2xx status is accepted.
func (c *Client) Push(ctx context.Context, pushURL string, r source.Reading) error {
	if r.IsEmpty() {
		return ErrEmptyReading
	}

	body, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("encoding reading: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, pushURL, bytes.NewReader(body))
	if err != nil {
		return pushError("building request for", pushURL, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return pushError("posting to", pushURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return fmt.Errorf("%s returned %d %s: %s",
			Redact(pushURL), resp.StatusCode, http.StatusText(resp.StatusCode),
			scrub(strings.TrimSpace(string(respBody)), pushURL))
	}

	// Drain the body so the transport can reuse this connection for the next
	// tick instead of opening a fresh one each time.
	io.Copy(io.Discard, resp.Body)
	return nil
}

// scrub removes the push URL from a message, then verifies the result.
//
// The forms below are a blocklist and could always be incomplete, so the
// leaksQuery check afterwards is what actually decides whether the message is
// safe to log. If anything recognisable from the query survived, the cause is
// dropped rather than risk writing the key to a log file.
func scrub(msg, raw string) string {
	red := Redact(raw)
	for _, form := range urlForms(raw) {
		if form == "" {
			// An empty pattern would make ReplaceAll splice the replacement
			// between every character of the message.
			continue
		}
		msg = strings.ReplaceAll(msg, strconv.Quote(form), red)
		msg = strings.ReplaceAll(msg, form, red)
	}
	if leaksQuery(msg, raw) {
		return "cause suppressed because it referenced the push URL"
	}
	return msg
}

// urlForms lists the shapes the standard library might have embedded the URL
// in. net/url strips a fragment before reporting a parse error, and the
// transport reports a re-serialized URL, so neither necessarily equals the
// string we were handed.
func urlForms(raw string) []string {
	forms := []string{raw}
	if i := strings.IndexByte(raw, '#'); i >= 0 {
		forms = append(forms, raw[:i])
	}
	if u, err := url.Parse(raw); err == nil {
		if s := u.String(); s != raw {
			forms = append(forms, s)
		}
	}
	return forms
}

// leaksQuery reports whether msg still contains a recognisable run of the push
// URL's path and query, which together carry the account id, device id and
// key. Matching in windows rather than whole catches a query that was escaped
// or truncated on its way into the message.
func leaksQuery(msg, raw string) bool {
	q := rawPathAndQuery(raw)
	if q == "" {
		return false
	}
	const window = 12
	if len(q) < window {
		return strings.Contains(msg, q)
	}
	for i := 0; i+window <= len(q); i++ {
		if strings.Contains(msg, q[i:i+window]) {
			return true
		}
	}
	return false
}

// rawPathAndQuery returns everything after the host. The whole tail identifies
// the account and the device, so none of it may reach a log.
func rawPathAndQuery(raw string) string {
	s := raw
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	i := strings.IndexByte(s, '/')
	if i < 0 {
		return rawQuery(raw)
	}
	s = s[i:]
	if j := strings.IndexByte(s, '#'); j >= 0 {
		s = s[:j]
	}
	return s
}

// rawQuery returns a URL's query without parsing it, so it still works on the
// malformed URLs net/url rejects.
func rawQuery(raw string) string {
	i := strings.IndexByte(raw, '?')
	if i < 0 {
		return ""
	}
	q := raw[i+1:]
	if j := strings.IndexByte(q, '#'); j >= 0 {
		q = q[:j]
	}
	return q
}

// pushError builds an error that cannot carry the push URL.
//
// It deliberately does not wrap the cause with %w. Wrapping would let a caller
// reach the original error, whose text still holds the URL and its key, by
// calling errors.Unwrap.
func pushError(action, raw string, cause error) error {
	return fmt.Errorf("%s %s: %s", action, Redact(raw), scrub(cause.Error(), raw))
}
