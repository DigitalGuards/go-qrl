package core

import (
	"math/big"
	"testing"

	"github.com/theQRL/go-qrl/common"
	"github.com/theQRL/go-qrl/consensus/beacon"
	"github.com/theQRL/go-qrl/core/rawdb"
	"github.com/theQRL/go-qrl/core/stakingrequests"
	"github.com/theQRL/go-qrl/core/types"
	"github.com/theQRL/go-qrl/core/vm"
	"github.com/theQRL/go-qrl/internal/testutil"
	"github.com/theQRL/go-qrl/params"
)

// An account's exit request lands in the next block's requests hash, and the
// imported chain re-executes the drain to the same commitment.
func TestExitRequestsCommittedAndReexecuted(t *testing.T) {
	zero := uint64(0)
	config := *params.TestChainConfig
	config.QRLBeaconRootsTime = &zero
	config.QRLExitRequestsTime = &zero
	var (
		wallet  = testutil.LoadAccount(t, "alice").Wallet(t)
		address = wallet.GetAddress()
		genesis = &Genesis{Config: &config, Alloc: GenesisAlloc{address: {Balance: big.NewInt(1_000_000_000_000_000_000)}}, BaseFee: big.NewInt(params.InitialBaseFee)}
		signer  = types.LatestSigner(&config)
		queue   = stakingrequests.ExperimentalAddress()
		request = stakingrequests.Request{Source: address, ValidatorIndex: 5, PublicKeyRoot: common.Hash{31: 7}}
	)
	_, blocks, _ := GenerateChainWithGenesis(genesis, beacon.NewFaker(), 3, func(i int, block *BlockGen) {
		if i != 1 {
			return
		}
		tx, err := types.SignTx(types.NewTx(&types.DynamicFeeTx{
			ChainID: config.ChainID, Nonce: block.TxNonce(address), To: &queue, Value: big.NewInt(1),
			Gas: 200_000, GasFeeCap: big.NewInt(params.InitialBaseFee * 2), Data: request.Submission(),
		}), signer, wallet)
		if err != nil {
			t.Fatal(err)
		}
		block.AddTx(tx)
	})
	empty, _ := stakingrequests.HashGroups(nil)
	withRequest, _ := stakingrequests.HashGroups(stakingrequests.Groups([]stakingrequests.Request{request}))
	// The request is drained in the block that admits it, after its transactions.
	for i, want := range []common.Hash{empty, withRequest, empty} {
		if got := blocks[i].RequestsHash(); got == nil || *got != want {
			t.Fatalf("block %d requests hash %v, want %x", i+1, got, want)
		}
	}
	chain, err := NewBlockChain(rawdb.NewMemoryDatabase(), nil, genesis, beacon.NewFaker(), vm.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer chain.Stop()
	if n, err := chain.InsertChain(blocks); err != nil {
		t.Fatalf("block %d: %v", n, err)
	}
	// A header that claims a different commitment fails re-execution.
	forged := types.CopyHeader(blocks[1].Header())
	*forged.RequestsHash = empty
	forgedBlock := types.NewBlockWithHeader(forged).WithBody(*blocks[1].Body())
	fresh, err := NewBlockChain(rawdb.NewMemoryDatabase(), nil, genesis, beacon.NewFaker(), vm.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Stop()
	if _, err := fresh.InsertChain(types.Blocks{blocks[0], forgedBlock}); err == nil {
		t.Fatal("accepted a block whose requests hash differs from its drain")
	}
}
