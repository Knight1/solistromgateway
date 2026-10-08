package apsystems

import (
	"encoding/json"
	"strings"
	"testing"
)

// FuzzTotalPower drives the per-port summing over arbitrary documents.
func FuzzTotalPower(f *testing.F) {
	f.Add([]byte(`{"p1":5,"p2":6,"e1":0.1,"te1":23.1}`))
	f.Add([]byte(`{"p1":null}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"p1":1e308,"p2":1e308}`))
	f.Add([]byte(`{"p999999":1}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var m map[string]json.RawMessage
		if json.Unmarshal(data, &m) != nil {
			return
		}

		total, ok := totalPower(m)
		if !ok {
			return
		}
		w, err := watts(total)
		if err != nil {
			return
		}
		if got := max(w, 0); got < 0 || got > maxWatts {
			t.Fatalf("producingWatt would be %d, outside 0..%d", got, maxWatts)
		}
	})
}

// FuzzSnippet asserts the serial never survives into an error message, whatever
// shape the body arrives in. This inverter returns its serial from every
// endpoint, and error messages end up in logs people paste into bug reports.
func FuzzSnippet(f *testing.F) {
	f.Add([]byte(`{"deviceId":"E99999999999","p1":5`))
	f.Add([]byte(`{"deviceId" : "E99999999999"}`))
	f.Add([]byte(`not json at all`))

	f.Fuzz(func(t *testing.T, data []byte) {
		const serial = "E99999999999"
		body := string(data)

		// Whatever shape it arrives in, a body that mentions the serial field
		// must not reach an error message with the serial still in it. Matching
		// on the field name alone, not on the surrounding JSON punctuation,
		// because a truncated response has none.
		if !strings.Contains(strings.ToLower(body), "deviceid") {
			return
		}
		if got := snippet(data); strings.Contains(got, serial) {
			t.Fatalf("snippet leaked the serial\n body: %q\n got:  %q", body, got)
		}
	})
}
