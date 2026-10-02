package core

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/theQRL/go-qrl/common"
	"github.com/theQRL/go-qrl/consensus"
	"github.com/theQRL/go-qrl/consensus/beacon"
	"github.com/theQRL/go-qrl/core/rawdb"
	"github.com/theQRL/go-qrl/core/stakingroots"
	"github.com/theQRL/go-qrl/core/state"
	"github.com/theQRL/go-qrl/core/types"
	"github.com/theQRL/go-qrl/params"
	"github.com/theQRL/go-qrl/trie"
)

type rootTestChain struct{ parent *types.Header }

func (c rootTestChain) Engine() consensus.Engine { return beacon.NewFaker() }
func (c rootTestChain) GetHeader(hash common.Hash, number uint64) *types.Header {
	if c.parent.Hash() == hash && c.parent.Number.Uint64() == number {
		return c.parent
	}
	return nil
}

func TestBeaconRootGenesisAllocation(t *testing.T) {
	zero := uint64(0)
	config := *params.TestChainConfig
	config.QRLBeaconRootsTime = &zero
	genesis := &Genesis{Config: &config, Timestamp: 7, Alloc: GenesisAlloc{}}
	db := rawdb.NewMemoryDatabase()
	block, err := genesis.Commit(db, trie.NewDatabase(db, nil))
	if err != nil {
		t.Fatal(err)
	}
	if block.BeaconRoot() == nil || *block.BeaconRoot() != (common.Hash{}) || len(genesis.Alloc) != 0 {
		t.Fatal("genesis root or allocation mutation")
	}
	statedb, err := state.New(block.Root(), state.NewDatabase(db), nil)
	if err != nil {
		t.Fatal(err)
	}
	address := stakingroots.ExperimentalAddress()
	code, _ := stakingroots.Runtime(stakingroots.ExperimentalSystemCaller(), stakingroots.ExperimentalHistoryLength)
	if !bytes.Equal(statedb.GetCode(address), code) || statedb.GetNonce(address) != 1 || statedb.GetState(address, common.BigToHash(big.NewInt(7))) != (common.StorageValue64{}) {
		t.Fatal("genesis runtime installation or absent history entry")
	}
	read, err := ReadGenesis(db)
	if err != nil || read.ToBlock().Hash() != block.Hash() {
		t.Fatalf("stored genesis round trip: %v", err)
	}
	genesis.Alloc[address] = GenesisAccount{Balance: big.NewInt(1)}
	allocation, err := genesis.beaconRootAlloc()
	if err != nil || allocation[address].Balance.Cmp(big.NewInt(1)) != 0 || !bytes.Equal(allocation[address].Code, code) {
		t.Fatalf("balance-only genesis allocation: %v", err)
	}
	genesis.Alloc[address] = GenesisAccount{Balance: big.NewInt(1), Nonce: 2}
	if _, err := genesis.Commit(rawdb.NewMemoryDatabase(), trie.NewDatabase(rawdb.NewMemoryDatabase(), nil)); err == nil {
		t.Fatal("accepted reserved genesis allocation collision")
	}
}

func TestBeaconRootActivationAndFailure(t *testing.T) {
	activation := uint64(100)
	config := *params.TestChainConfig
	config.QRLBeaconRootsTime = &activation
	parent := &types.Header{Number: big.NewInt(0), Time: 99}
	header := &types.Header{ParentHash: parent.Hash(), Number: big.NewInt(1), Time: 100, ParentBeaconRoot: &common.Hash{1, 2, 3}}
	chain := rootTestChain{parent}
	newState := func() *state.StateDB {
		db, err := state.New(types.EmptyRootHash, state.NewDatabase(rawdb.NewMemoryDatabase()), nil)
		if err != nil {
			t.Fatal(err)
		}
		return db
	}
	db := newState()
	if err := ProcessBeaconRoot(&config, chain, header, db); err != nil {
		t.Fatal(err)
	}
	address := stakingroots.ExperimentalAddress()
	value := db.GetState(address, common.BigToHash(new(big.Int).SetUint64(100+stakingroots.ExperimentalHistoryLength)))
	if !bytes.Equal(value[:32], header.ParentBeaconRoot[:]) {
		t.Fatal("activation did not record beacon root")
	}
	missing := types.CopyHeader(header)
	missing.ParentBeaconRoot = nil
	if err := ProcessBeaconRoot(&config, chain, missing, newState()); err == nil {
		t.Fatal("accepted active header without root")
	}
	early := types.CopyHeader(header)
	early.Time = 99
	if err := ProcessBeaconRoot(&config, chain, early, newState()); err == nil {
		t.Fatal("accepted root before activation")
	}
	collision := newState()
	collision.AddBalance(address, big.NewInt(1))
	if err := ProcessBeaconRoot(&config, chain, header, collision); err != nil || len(collision.GetCode(address)) == 0 || collision.GetBalance(address).Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("pre-fork transfer prevented activation or lost balance: %v", err)
	}
	collision = newState()
	collision.AddBalance(address, big.NewInt(1))
	collision.SetNonce(address, 2)
	if err := ProcessBeaconRoot(&config, chain, header, collision); err == nil || len(collision.GetCode(address)) != 0 || collision.GetBalance(address).Cmp(big.NewInt(1)) != 0 {
		t.Fatal("activation collision changed existing account")
	}
	for _, mode := range []string{"code", "storage"} {
		collision = newState()
		if mode == "code" {
			collision.SetCode(address, []byte{0})
		} else {
			collision.SetState(address, common.Hash{1}, common.StorageValue64{1})
			collision.IntermediateRoot(false)
		}
		if err := ProcessBeaconRoot(&config, chain, header, collision); err == nil {
			t.Fatalf("accepted activation %s collision", mode)
		}
	}
	parent.Time = 100
	header.ParentHash = parent.Hash()
	header.Time = 101
	if err := ProcessBeaconRoot(&config, chain, header, newState()); err == nil {
		t.Fatal("accepted missing runtime after activation")
	}
	wrong := newState()
	wrong.SetCode(address, []byte{0})
	if err := ProcessBeaconRoot(&config, chain, header, wrong); err == nil {
		t.Fatal("accepted wrong runtime after activation")
	}
}
