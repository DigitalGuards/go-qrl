package core

import (
	"errors"
	"fmt"

	"github.com/theQRL/go-qrl/common"
	"github.com/theQRL/go-qrl/core/stakingroots"
	"github.com/theQRL/go-qrl/core/state"
	"github.com/theQRL/go-qrl/core/types"
	"github.com/theQRL/go-qrl/params"
)

// ProcessBeaconRoot applies the experimental pre-transaction block operation.
// Every caller must supply parent state, including empty blocks and transaction
// replay. Calling this on an already processed block state is invalid usage.
func ProcessBeaconRoot(config *params.ChainConfig, chain ChainContext, header *types.Header, db *state.StateDB) error {
	active := config.IsQRLBeaconRoots(header.Time)
	if active != (header.ParentBeaconRoot != nil) {
		return errors.New("parent beacon root presence disagrees with experimental fork")
	}
	if !active {
		return nil
	}
	if header.Number.Sign() == 0 {
		if *header.ParentBeaconRoot != (common.Hash{}) {
			return errors.New("genesis parent beacon root must be zero")
		}
		return nil
	}
	parent := chain.GetHeader(header.ParentHash, header.Number.Uint64()-1)
	if parent == nil {
		return errors.New("missing parent header for beacon root processing")
	}
	snapshot := db.Snapshot()
	address, caller := stakingroots.ExperimentalAddress(), stakingroots.ExperimentalSystemCaller()
	if !config.IsQRLBeaconRoots(parent.Time) {
		storageRoot := db.GetStorageRoot(address)
		if db.GetNonce(address) != 0 || len(db.GetCode(address)) != 0 || (storageRoot != (common.Hash{}) && storageRoot != types.EmptyRootHash) {
			return errors.New("experimental beacon history address collision at activation")
		}
		code, err := stakingroots.Runtime(caller, stakingroots.ExperimentalHistoryLength)
		if err != nil {
			return err
		}
		// Anyone can transfer funds to an empty reserved address before the
		// fork. Preserve that balance so such a transfer cannot stop activation.
		if !db.Exist(address) {
			db.CreateAccount(address)
		}
		db.SetNonce(address, 1)
		db.SetCode(address, code)
	}
	err := stakingroots.WriteParentRoot(db, NewQRVMBlockContext(header, chain, nil), config, address, caller, stakingroots.ExperimentalHistoryLength, *header.ParentBeaconRoot)
	if err != nil {
		db.RevertToSnapshot(snapshot)
		return fmt.Errorf("beacon root pre-block operation: %w", err)
	}
	// Commit the system operation's original storage values before user gas
	// accounting begins. There are no receipts or user gas charges to finalize.
	db.Finalise(true)
	return nil
}
