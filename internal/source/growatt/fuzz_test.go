package growatt

import (
	"encoding/json"
	"testing"
)

// checkContract asserts the reading obeys what the push API accepts. A logic
// error in the mapping would show up here as an impossible value rather than as
// a crash.
func checkContract(t *testing.T, r interface {
	IsEmpty() bool
}, producing, soc, charging, state *int) {
	t.Helper()
	if producing != nil && (*producing < 0 || *producing > maxWatts) {
		t.Fatalf("producingWatt = %d, outside 0..%d", *producing, maxWatts)
	}
	if soc != nil && (*soc < 0 || *soc > 100) {
		t.Fatalf("soc = %d, outside 0..100", *soc)
	}
	if charging != nil && *charging < 0 {
		t.Fatalf("chargingPower = %d, which the API requires to be positive", *charging)
	}
	if state != nil {
		switch *state {
		case 0, 1, 2, 3:
		default:
			t.Fatalf("powerStorageState = %d, which is not one of 0, 1, 2, 3", *state)
		}
	}
}

// FuzzMapReading feeds arbitrary documents through protocol detection and
// mapping, under every battery and grid setting.
func FuzzMapReading(f *testing.F) {
	f.Add([]byte(`{"OutputPower":781.8,"SOC":0,"BatteryVoltage":0,"ACPowerToUser":0,"ACPowerToGrid":0}`))
	f.Add([]byte(`{"AcPower":512.7}`))
	f.Add([]byte(`{"OutActivePwr":1430.6,"BattSOC":88,"BattPwr":-430,"BattVoltage":53.1}`))
	f.Add([]byte(`{"OutputPower":2400,"BDCStateOfCharge":77,"BDCChargePower":500,"BDCBatteryVoltage":102.4}`))
	f.Add([]byte(`{"OutputPower":null,"SOC":null}`))
	f.Add([]byte(`{}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var doc statusDoc
		if json.Unmarshal(data, &doc) != nil {
			return
		}

		for _, battery := range []string{"auto", "on", "off"} {
			for _, grid := range []bool{false, true} {
				s := &Source{name: "fuzz", battery: battery, reportGrid: grid}
				r, err := s.mapReading(doc)
				if err != nil {
					continue
				}
				checkContract(t, r, r.ProducingWatt, r.SOC, r.ChargingPower, r.PowerStorageState)
			}
		}
	})
}

// FuzzWatts covers the conversion from a reported float to whole watts.
func FuzzWatts(f *testing.F) {
	f.Add(781.8)
	f.Add(-3.2)
	f.Add(1e12)
	f.Add(0.0)

	f.Fuzz(func(t *testing.T, v float64) {
		w, err := watts(v)
		if err != nil {
			return
		}
		if w > maxWatts || w < -maxWatts {
			t.Fatalf("watts(%g) = %d, which is outside the bound it is meant to enforce", v, w)
		}
	})
}
