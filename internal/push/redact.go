package push

import "net/url"

// Redact reduces a push URL to something safe to log.
//
// The whole URL is a credential: the path carries the account and device
// identifiers and the query carries the key. Only the scheme and host survive,
// which is enough to tell which endpoint was being called.
func Redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "<unparseable push url>"
	}
	return u.Scheme + "://" + u.Host + "/..."
}
