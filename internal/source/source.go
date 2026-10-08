// Package source defines the device-independent shape of a reading and the
// interface every device adapter implements.
package source

import "context"

// Reading is one sample from a device, in the field names the Solistrom push
// API expects.
//
// Every field is a pointer on purpose. Devices often report literal 0 for
// hardware they do not have: for example, an inverter without battery storage
// might report battery state as 0, and one without a meter might report power
// as 0. As plain ints, those 0 values are indistinguishable from real readings,
// and we would misrepresent device state to Solistrom. Using pointers, a nil
// field means the device cannot report that value, and is left out of the
// payload entirely.
type Reading struct {
	Watt              *int `json:"watt,omitempty"`
	ProducingWatt     *int `json:"producingWatt,omitempty"`
	SOC               *int `json:"soc,omitempty"`
	ChargingPower     *int `json:"chargingPower,omitempty"`
	PowerStorageState *int `json:"powerStorageState,omitempty"`
}

// IsEmpty reports whether the device gave us nothing worth sending.
func (r Reading) IsEmpty() bool {
	return r.Watt == nil &&
		r.ProducingWatt == nil &&
		r.SOC == nil &&
		r.ChargingPower == nil &&
		r.PowerStorageState == nil
}

// Int returns a pointer to v, for building a Reading in one expression.
func Int(v int) *int { return &v }

// Source reads one device.
type Source interface {
	// Name is the device name from the config, used in log lines.
	Name() string
	// Read takes one sample. It returns an error if the device cannot be
	// reached or its response cannot be understood.
	Read(ctx context.Context) (Reading, error)
}
