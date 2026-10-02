package stakingrequests

import (
	"github.com/theQRL/go-qrl/common"
	"github.com/theQRL/go-qrl/core/stakingroots"
	"github.com/theQRL/go-qrl/core/vm"
	"github.com/theQRL/go-qrl/params"
)

// ExperimentalDrainGas is a bounded fixture budget, pending gas qualification.
const ExperimentalDrainGas uint64 = 250_000

// Drain runs the native queue with isolated system-call accounting and validates
// its raw output. A caller integrating this helper must commit the exact records
// in the block header and Engine transport, and provide fork and ordering checks.
// It is not invoked by any canonical client path in this prototype.
func Drain(db vm.StateDB, block vm.BlockContext, chain *params.ChainConfig, address, systemCaller common.Address) ([]Request, error) {
	code, err := Runtime(systemCaller)
	if err != nil {
		return nil, err
	}
	if db == nil {
		return nil, stakingroots.ErrSystemCall
	}
	snapshot := db.Snapshot()
	output, err := stakingroots.ExecuteSystemCall(db, block, chain, stakingroots.SystemCall{
		Caller: systemCaller, Target: address, Runtime: code, GasLimit: ExperimentalDrainGas,
	})
	if err != nil {
		db.RevertToSnapshot(snapshot)
		return nil, err
	}
	requests, err := DecodeBatch(output, MaxPerBlock)
	if err != nil {
		db.RevertToSnapshot(snapshot)
		return nil, err
	}
	return requests, nil
}
