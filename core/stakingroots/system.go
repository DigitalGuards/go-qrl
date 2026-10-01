package stakingroots

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"

	"github.com/theQRL/go-qrl/common"
	"github.com/theQRL/go-qrl/core/types"
	"github.com/theQRL/go-qrl/core/vm"
	"github.com/theQRL/go-qrl/params"
)

// These gas bounds are experimental fixtures and require later qualification.
const (
	ExperimentalWriteGas uint64 = 100_000
	MaxSystemCallGas     uint64 = 1_000_000
)

var (
	ErrRuntimeIdentity = errors.New("system contract runtime is missing or mismatched")
	ErrSystemCall      = errors.New("invalid experimental system call")
)

// SystemCall describes a trusted protocol operation. Callers must derive Input
// and Runtime from their validated protocol context. This package registers no
// protocol operation in block construction, import, replay, or RPC execution.
type SystemCall struct {
	Caller   common.Address
	Target   common.Address
	Runtime  []byte
	Input    []byte
	GasLimit uint64
}

// ExecuteSystemCall executes a bounded, zero-value native call. Persistent writes
// are journaled into db and are reverted on failure. The operation has a fresh
// access list and refund counter, and discards logs and preimages. It leaves the
// enclosing transaction's corresponding state untouched. This QRVM revision has
// no transient-storage interface; a future addition must extend this isolation.
//
// The helper creates no receipt, charges no balance, consumes no nonce, and does
// not update any block gas accumulator. Callers must validate output and revert
// their own surrounding snapshot if a successful call has invalid output.
func ExecuteSystemCall(db vm.StateDB, block vm.BlockContext, chain *params.ChainConfig, call SystemCall) ([]byte, error) {
	if db == nil || chain == nil || block.BlockNumber == nil || call.Caller == (common.Address{}) ||
		call.Target == (common.Address{}) || call.Target == call.Caller ||
		call.GasLimit == 0 || call.GasLimit > MaxSystemCallGas {
		return nil, ErrSystemCall
	}
	if len(call.Runtime) == 0 || !bytes.Equal(db.GetCode(call.Target), call.Runtime) {
		return nil, ErrRuntimeIdentity
	}
	rules := chain.Rules(block.BlockNumber, block.Time)
	precompiles := vm.ActivePrecompiles(rules)
	for _, address := range precompiles {
		if address == call.Target {
			return nil, fmt.Errorf("%w: precompile target", ErrSystemCall)
		}
	}
	// The outer snapshot also protects against changes made before QRVM's own
	// call snapshot. The isolated wrapper keeps warmth and refunds out of db.
	snapshot := db.Snapshot()
	isolated := &systemState{StateDB: db}
	isolated.Prepare(rules, call.Caller, block.Coinbase, &call.Target, precompiles, nil)
	// All calls have zero value. Supply the ordinary transfer behavior locally
	// so this helper needs no import from core and cannot form an import cycle.
	block.CanTransfer = func(db vm.StateDB, addr common.Address, value *big.Int) bool {
		return db.GetBalance(addr).Cmp(value) >= 0
	}
	block.Transfer = func(db vm.StateDB, from, to common.Address, value *big.Int) {
		if value.Sign() != 0 {
			db.SubBalance(from, value)
			db.AddBalance(to, value)
		}
	}
	engine := vm.NewQRVM(block, vm.TxContext{Origin: call.Caller, GasPrice: new(big.Int)}, isolated, chain, vm.Config{NoBaseFee: true})
	output, _, err := engine.Call(vm.AccountRef(call.Caller), call.Target, call.Input, call.GasLimit, new(big.Int))
	if err != nil {
		db.RevertToSnapshot(snapshot)
		return nil, fmt.Errorf("system contract execution: %w", err)
	}
	return output, nil
}

// WriteParentRoot performs one pre-block history write, including for a block
// with zero transactions. The caller must supply the authenticated beacon parent
// and install the exact Runtime beforehand. Genesis handling and all block-path
// wiring remain responsibilities of the eventual fork integration.
func WriteParentRoot(db vm.StateDB, block vm.BlockContext, chain *params.ChainConfig, address, systemCaller common.Address, capacity uint64, root common.Hash) error {
	if db == nil {
		return ErrSystemCall
	}
	code, err := Runtime(systemCaller, capacity)
	if err != nil {
		return err
	}
	snapshot := db.Snapshot()
	output, err := ExecuteSystemCall(db, block, chain, SystemCall{
		Caller: systemCaller, Target: address, Runtime: code, Input: root[:], GasLimit: ExperimentalWriteGas,
	})
	if err != nil || len(output) != 0 {
		db.RevertToSnapshot(snapshot)
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: unexpected history write output", ErrSystemCall)
	}
	return nil
}

type accessSet map[common.Address]map[common.Hash]struct{}

func (a accessSet) copy() accessSet {
	copy := make(accessSet, len(a))
	for address, slots := range a {
		copy[address] = make(map[common.Hash]struct{}, len(slots))
		for slot := range slots {
			copy[address][slot] = struct{}{}
		}
	}
	return copy
}

type localSnapshot struct {
	id     int
	access accessSet
	refund uint64
}

type systemState struct {
	vm.StateDB
	access    accessSet
	originals map[common.Address]map[common.Hash]common.StorageValue64
	refund    uint64
	snapshots []localSnapshot
}

func (s *systemState) Prepare(_ params.Rules, sender, coinbase common.Address, dest *common.Address, precompiles []common.Address, accesses types.AccessList) {
	s.access = make(accessSet)
	s.originals = make(map[common.Address]map[common.Hash]common.StorageValue64)
	s.refund = 0
	s.AddAddressToAccessList(sender)
	s.AddAddressToAccessList(coinbase)
	if dest != nil {
		s.AddAddressToAccessList(*dest)
	}
	for _, address := range precompiles {
		s.AddAddressToAccessList(address)
	}
	for _, access := range accesses {
		s.AddAddressToAccessList(access.Address)
		for _, slot := range access.StorageKeys {
			s.AddSlotToAccessList(access.Address, slot)
		}
	}
}

// Each system operation has its own original storage values for SSTORE gas and
// refund accounting, even when the enclosing db still has earlier dirty writes.
func (s *systemState) GetCommittedState(address common.Address, slot common.Hash) common.StorageValue64 {
	if slots, ok := s.originals[address]; ok {
		if value, ok := slots[slot]; ok {
			return value
		}
	} else {
		s.originals[address] = make(map[common.Hash]common.StorageValue64)
	}
	value := s.StateDB.GetState(address, slot)
	s.originals[address][slot] = value
	return value
}

func (s *systemState) SetState(address common.Address, slot common.Hash, value common.StorageValue64) {
	s.GetCommittedState(address, slot)
	s.StateDB.SetState(address, slot, value)
}

func (s *systemState) AddressInAccessList(address common.Address) bool {
	_, ok := s.access[address]
	return ok
}

func (s *systemState) SlotInAccessList(address common.Address, slot common.Hash) (bool, bool) {
	slots, ok := s.access[address]
	_, present := slots[slot]
	return ok, present
}

func (s *systemState) AddAddressToAccessList(address common.Address) {
	if !s.AddressInAccessList(address) {
		s.access[address] = make(map[common.Hash]struct{})
	}
}

func (s *systemState) AddSlotToAccessList(address common.Address, slot common.Hash) {
	s.AddAddressToAccessList(address)
	s.access[address][slot] = struct{}{}
}

func (s *systemState) AddRefund(gas uint64) { s.refund += gas }
func (s *systemState) GetRefund() uint64    { return s.refund }
func (s *systemState) SubRefund(gas uint64) {
	if gas > s.refund {
		panic("system call refund underflow")
	}
	s.refund -= gas
}

func (s *systemState) AddLog(*types.Log)               {}
func (s *systemState) AddPreimage(common.Hash, []byte) {}

func (s *systemState) Snapshot() int {
	id := s.StateDB.Snapshot()
	s.snapshots = append(s.snapshots, localSnapshot{id: id, access: s.access.copy(), refund: s.refund})
	return id
}

func (s *systemState) RevertToSnapshot(id int) {
	for i := len(s.snapshots) - 1; i >= 0; i-- {
		if s.snapshots[i].id == id {
			s.StateDB.RevertToSnapshot(id)
			s.access, s.refund = s.snapshots[i].access, s.snapshots[i].refund
			s.snapshots = s.snapshots[:i]
			return
		}
	}
	panic("unknown system call snapshot")
}
