package stakingroots

import "github.com/theQRL/go-qrl/common"

// ExperimentalAddress and ExperimentalSystemCaller are local research network
// assignments. They have no QIP or public-network allocation. Every byte of each
// native address is fixed, and callers receive a copy. The 0x4788 suffix names
// EIP-4788, and the system caller is EIP-4788's SYSTEM_ADDRESS widened to 64
// bytes.
func ExperimentalAddress() common.Address {
	var address common.Address
	for i := range address {
		address[i] = 0xff
	}
	address[62], address[63] = 0x47, 0x88
	return address
}

func ExperimentalSystemCaller() common.Address {
	var address common.Address
	for i := range address {
		address[i] = 0xff
	}
	address[63] = 0xfe
	return address
}
