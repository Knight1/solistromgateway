package ahoydtu

import (
	"encoding/json"
	"testing"
)

// FuzzIndexDecodeAndPick drives the datalogger's index document through decoding
// and inverter selection, including ids the document does not contain.
func FuzzIndexDecodeAndPick(f *testing.F) {
	f.Add([]byte(`{"inverter":[{"enabled":true,"id":0,"name":"roof","cur_pwr":5.1,"is_avail":true}]}`), 0)
	f.Add([]byte(`{"inverter":[]}`), 0)
	f.Add([]byte(`{"inverter":[{"enabled":true,"id":0,"cur_pwr":null,"is_avail":true}]}`), 0)
	f.Add([]byte(`{}`), 7)

	f.Fuzz(func(t *testing.T, data []byte, id int) {
		var doc indexDoc
		if json.Unmarshal(data, &doc) != nil {
			return
		}
		s := &Source{name: "fuzz", inverter: id}

		iv, err := s.pick(doc)
		if err != nil {
			return
		}
		if iv.ID != id {
			t.Fatalf("pick returned inverter %d when asked for %d", iv.ID, id)
		}

		// The same arithmetic Read performs on the chosen inverter.
		if !iv.Enabled || !iv.IsAvail || iv.CurPwr == nil {
			return
		}
		w, convErr := watts(*iv.CurPwr)
		if convErr != nil {
			return
		}
		if got := max(w, 0); got < 0 || got > maxWatts {
			t.Fatalf("producingWatt would be %d, outside 0..%d", got, maxWatts)
		}
	})
}
