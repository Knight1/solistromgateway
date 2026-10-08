package source

import (
	"encoding/json"
	"testing"
)

func TestReadingOmitsAbsentFields(t *testing.T) {
	r := Reading{ProducingWatt: Int(782)}

	got, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	want := `{"producingWatt":782}`
	if string(got) != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestReadingKeepsExplicitZero(t *testing.T) {
	// powerStorageState 0 means "idle", which is a real reading and must
	// survive marshalling. This is why the fields are pointers.
	r := Reading{ProducingWatt: Int(0), PowerStorageState: Int(0)}

	got, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	want := `{"producingWatt":0,"powerStorageState":0}`
	if string(got) != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestReadingIsEmpty(t *testing.T) {
	if !(Reading{}).IsEmpty() {
		t.Error("a reading with no fields set should be empty")
	}
	if (Reading{ProducingWatt: Int(0)}).IsEmpty() {
		t.Error("a reading with an explicit zero is not empty")
	}
}
