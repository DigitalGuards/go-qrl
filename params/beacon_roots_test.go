package params

import "testing"

func TestBeaconRootConfigCompatibility(t *testing.T) {
	zero, ten, twenty := uint64(0), uint64(10), uint64(20)
	for _, test := range []struct {
		old, next *uint64
		time      uint64
		invalid   bool
	}{
		{nil, nil, 100, false}, {nil, &ten, 9, false}, {nil, &ten, 10, true},
		{&ten, &twenty, 9, false}, {&ten, &twenty, 10, true}, {&ten, nil, 11, true},
		{&zero, nil, 0, true}, {&ten, &ten, 100, false},
	} {
		old, next := *TestChainConfig, *TestChainConfig
		old.QRLBeaconRootsTime, next.QRLBeaconRootsTime = test.old, test.next
		if err := old.CheckCompatible(&next, 1, test.time); (err != nil) != test.invalid {
			t.Fatalf("old %v new %v at %d: %v", test.old, test.next, test.time, err)
		}
	}
}
