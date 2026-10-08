package growatt

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// protocol names the JSON keys one Modbus protocol version uses. The gateway
// exposes different register tables per protocol, so the key names differ.
// An empty field name means this protocol does not expose that value.
type protocol struct {
	name           string
	production     string
	soc            string
	chargePower    string
	dischargePower string
	batteryVoltage string
	gridToUser     string
	gridToGrid     string

	// Lifetime meter totals, used to tell a real meter from registers that
	// merely exist. Empty when the protocol does not expose them.
	gridTotalToUser string
	gridTotalToGrid string
}

// Only protocol 1.24 is verified against hardware. The other three are read
// from the gateway's register tables and are unverified.
var (
	proto124 = protocol{
		name: "1.24", production: "OutputPower",
		soc: "SOC", chargePower: "ChargePower", dischargePower: "DischargePower",
		batteryVoltage: "BatteryVoltage",
		gridToUser:     "ACPowerToUser", gridToGrid: "ACPowerToGrid",
		gridTotalToUser: "ACPowerToUserTotal", gridTotalToGrid: "ACPowerToGridTotal",
	}
	proto305 = protocol{
		name: "3.05", production: "AcPower",
	}
	protoSPF = protocol{
		name: "SPF", production: "OutActivePwr",
		soc: "BattSOC", chargePower: "BattPwr", batteryVoltage: "BattVoltage",
	}
	protoTLXH = protocol{
		name: "TL-XH", production: "OutputPower",
		soc: "BDCStateOfCharge", chargePower: "BDCChargePower",
		dischargePower: "BDCDischargePower", batteryVoltage: "BDCBatteryVoltage",
		gridToUser: "TotalForwardPower", gridToGrid: "TotalReversePower",
	}
)

// statusDoc is a decoded /status body. Values stay raw because the document
// mixes numbers with strings such as Hostname and Mac.
type statusDoc map[string]json.RawMessage

// number returns the value of key as a float, and whether it was present and
// numeric.
func (d statusDoc) number(key string) (float64, bool) {
	if key == "" {
		return 0, false
	}
	raw, ok := d[key]
	if !ok || string(raw) == "null" {
		// A null is the gateway saying it has no value for this register. That
		// has to stay indistinguishable from the key being absent.
		return 0, false
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		return 0, false
	}
	return f, true
}

// detect picks the protocol from which keys are present.
//
// The order matters. Both 1.24 and TL-XH expose OutputPower, so the most
// distinctive marker has to be tested first or a TL-XH would be read as a 1.24
// and its battery registers missed.
func detect(d statusDoc) (protocol, error) {
	for k := range d {
		if strings.HasPrefix(k, "BDC") {
			return protoTLXH, nil
		}
	}
	if _, ok := d[protoSPF.production]; ok {
		return protoSPF, nil
	}
	if _, ok := d[proto124.production]; ok {
		return proto124, nil
	}
	if _, ok := d[proto305.production]; ok {
		return proto305, nil
	}
	return protocol{}, fmt.Errorf("unrecognised /status response: no known production field; keys found: %s", strings.Join(keys(d), ", "))
}

func keys(d statusDoc) []string {
	out := make([]string, 0, len(d))
	for k := range d {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
