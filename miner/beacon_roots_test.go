package miner

import (
	"testing"
	"time"

	"github.com/theQRL/go-qrl/common"
	"github.com/theQRL/go-qrl/consensus/beacon"
	"github.com/theQRL/go-qrl/core/rawdb"
	"github.com/theQRL/go-qrl/core/types"
	"github.com/theQRL/go-qrl/params"
)

func TestBeaconRootBuildOwnsArgumentsAcrossRebuild(t *testing.T) {
	zero := uint64(0)
	config := *params.TestChainConfig
	config.QRLBeaconRootsTime = &zero
	worker, backend := newTestWorker(t, &config, beacon.NewFaker(), rawdb.NewMemoryDatabase(), 0)
	t.Cleanup(func() { backend.txPool.Close(); backend.chain.Stop() })
	root := common.Hash{1}
	args := &BuildPayloadArgs{Parent: backend.chain.CurrentBlock().Hash(), Timestamp: uint64(time.Now().Unix()), Withdrawals: types.Withdrawals{}, ParentBeaconRoot: &root}
	id := args.Id()
	payload, err := worker.buildPayload(args)
	if err != nil {
		t.Fatal(err)
	}
	defer payload.Resolve()
	// Wait for the first full result without stopping the periodic builder.
	payload.lock.Lock()
	for payload.full == nil {
		payload.cond.Wait()
	}
	// Clear the result and mutate caller-owned input before the next rebuild.
	payload.full = nil
	root = common.Hash{9}
	args.Timestamp++
	for payload.full == nil {
		payload.cond.Wait()
	}
	result := payload.full
	payload.lock.Unlock()
	if payload.id != id || *result.BeaconRoot() != (common.Hash{1}) || result.Time() == args.Timestamp {
		t.Fatal("asynchronous rebuild reused caller-owned root or arguments")
	}
}
