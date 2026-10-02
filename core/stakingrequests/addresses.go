package stakingrequests

import "github.com/theQRL/go-qrl/common"

// ExperimentalAddress is the demo-network queue address. The 0x7002 suffix
// names EIP-7002. It has no QIP or public-network allocation, and callers
// receive a copy. The protocol drains it with stakingroots'
// ExperimentalSystemCaller.
func ExperimentalAddress() common.Address {
	var address common.Address
	for i := range address {
		address[i] = 0xff
	}
	address[62], address[63] = 0x70, 0x02
	return address
}
