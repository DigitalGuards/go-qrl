package core

import (
	"errors"
	"fmt"

	"github.com/theQRL/go-qrl/common"
	"github.com/theQRL/go-qrl/core/stakingrequests"
	"github.com/theQRL/go-qrl/core/stakingroots"
	"github.com/theQRL/go-qrl/core/state"
	"github.com/theQRL/go-qrl/core/types"
	"github.com/theQRL/go-qrl/params"
)

// installExitQueue places the demo exit queue runtime at its reserved address
// when the exit-requests fork activates after genesis. It applies the same
// collision rules as the beacon-root history installation.
func installExitQueue(db *state.StateDB) error {
	address := stakingrequests.ExperimentalAddress()
	storageRoot := db.GetStorageRoot(address)
	if db.GetNonce(address) != 0 || len(db.GetCode(address)) != 0 || (storageRoot != (common.Hash{}) && storageRoot != types.EmptyRootHash) {
		return errors.New("experimental exit queue address collision at activation")
	}
	code, err := stakingrequests.Runtime(stakingroots.ExperimentalSystemCaller())
	if err != nil {
		return err
	}
	if !db.Exist(address) {
		db.CreateAccount(address)
	}
	db.SetNonce(address, 1)
	db.SetCode(address, code)
	return nil
}

// ProcessExitRequests drains the demo exit queue once per activated block,
// after ordinary transactions and before withdrawal credits, the ordering
// go-ethereum uses for EIP-7002. It returns the EIP-7685 request groups and
// their commitment. Inactive blocks return no groups and a nil commitment.
func ProcessExitRequests(config *params.ChainConfig, chain ChainContext, header *types.Header, db *state.StateDB) ([][]byte, *common.Hash, error) {
	if !config.IsQRLExitRequests(header.Time) {
		return nil, nil, nil
	}
	requests, err := stakingrequests.Drain(db, NewQRVMBlockContext(header, chain, nil), config, stakingrequests.ExperimentalAddress(), stakingroots.ExperimentalSystemCaller())
	if err != nil {
		return nil, nil, fmt.Errorf("exit request drain: %w", err)
	}
	groups := stakingrequests.Groups(requests)
	hash, err := stakingrequests.HashGroups(groups)
	if err != nil {
		return nil, nil, err
	}
	db.Finalise(true)
	return groups, &hash, nil
}
