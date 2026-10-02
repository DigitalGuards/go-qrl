package stakingroots

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"math/big"
	"testing"

	"github.com/theQRL/go-qrl/common"
	"github.com/theQRL/go-qrl/core/rawdb"
	"github.com/theQRL/go-qrl/core/state"
	"github.com/theQRL/go-qrl/core/types"
	"github.com/theQRL/go-qrl/core/vm"
	"github.com/theQRL/go-qrl/params"
)

// These complete native addresses are test fixtures, with no network assignment.
var (
	testAddress = common.BytesToAddress(bytes.Repeat([]byte{0x71}, 64))
	testSystem  = common.BytesToAddress(bytes.Repeat([]byte{0xfa}, 64))
	testReader  = common.BytesToAddress(bytes.Repeat([]byte{0x23}, 64))
)

func testState(t *testing.T, capacity uint64) (*state.StateDB, []byte) {
	t.Helper()
	db, err := state.New(types.EmptyRootHash, state.NewDatabase(rawdb.NewMemoryDatabase()), nil)
	if err != nil {
		t.Fatal(err)
	}
	code, err := Runtime(testSystem, capacity)
	if err != nil {
		t.Fatal(err)
	}
	db.CreateAccount(testAddress)
	db.SetCode(testAddress, code)
	return db, code
}

func testBlock(timestamp uint64) vm.BlockContext {
	return vm.BlockContext{
		BlockNumber: big.NewInt(1), Time: timestamp, GasLimit: 30_000_000,
		BaseFee: new(big.Int), Random: new(common.Hash),
		CanTransfer: func(db vm.StateDB, address common.Address, value *big.Int) bool {
			return db.GetBalance(address).Cmp(value) >= 0
		},
		Transfer: func(db vm.StateDB, from, to common.Address, value *big.Int) {
			if value.Sign() != 0 {
				db.SubBalance(from, value)
				db.AddBalance(to, value)
			}
		},
	}
}

func rawCall(db *state.StateDB, caller common.Address, input []byte, timestamp uint64, static bool) ([]byte, error) {
	block := testBlock(timestamp)
	rules := params.TestChainConfig.Rules(block.BlockNumber, block.Time)
	db.Prepare(rules, caller, block.Coinbase, &testAddress, vm.ActivePrecompiles(rules), nil)
	engine := vm.NewQRVM(block, vm.TxContext{Origin: caller, GasPrice: new(big.Int)}, db, params.TestChainConfig, vm.Config{})
	if static {
		output, _, err := engine.StaticCall(vm.AccountRef(caller), testAddress, input, ExperimentalWriteGas)
		return output, err
	}
	output, _, err := engine.Call(vm.AccountRef(caller), testAddress, input, ExperimentalWriteGas, new(big.Int))
	return output, err
}

func query(timestamp uint64) []byte {
	input := make([]byte, 64)
	binary.BigEndian.PutUint64(input[56:], timestamp)
	return input
}

func write(t *testing.T, db *state.StateDB, capacity, timestamp uint64, root common.Hash) {
	t.Helper()
	if err := WriteParentRoot(db, testBlock(timestamp), params.TestChainConfig, testAddress, testSystem, capacity, root); err != nil {
		t.Fatal(err)
	}
}

func assertRead(t *testing.T, db *state.StateDB, timestamp uint64, root common.Hash) {
	t.Helper()
	output, err := rawCall(db, testReader, query(timestamp), timestamp+1, true)
	if err != nil || !bytes.Equal(output, root[:]) {
		t.Fatalf("read %d: output %x, error %v, want %x", timestamp, output, err, root)
	}
}

func TestNativeRootByteAlignment(t *testing.T) {
	db, _ := testState(t, ExperimentalHistoryLength)
	roots := []common.Hash{{}, {0: 0xff}, {31: 0xff}, {0: 0x91, 7: 0x13, 16: 0xab, 31: 0x29}}
	for i := 0; i < 32; i++ {
		var root common.Hash
		root[i] = byte(i + 1)
		roots = append(roots, root)
	}
	for i, root := range roots {
		timestamp := uint64(i + 1)
		write(t, db, ExperimentalHistoryLength, timestamp, root)
		assertRead(t, db, timestamp, root)
		// CALLDATALOAD left-aligns the short root in a native 64-byte word.
		value := db.GetState(testAddress, common.BigToHash(new(big.Int).SetUint64(timestamp+ExperimentalHistoryLength)))
		if !bytes.Equal(value[:32], root[:]) || !bytes.Equal(value[32:], make([]byte, 32)) {
			t.Fatalf("unexpected native storage alignment: %x", value)
		}
	}
}

func TestNativeRingRolloverAndTimestampMaximum(t *testing.T) {
	db, _ := testState(t, ExperimentalHistoryLength)
	first, second := common.Hash{1}, common.Hash{2}
	write(t, db, ExperimentalHistoryLength, 60, first)
	write(t, db, ExperimentalHistoryLength, 60+ExperimentalHistoryLength, second)
	if _, err := rawCall(db, testReader, query(60), 100, true); !errors.Is(err, vm.ErrExecutionReverted) {
		t.Fatalf("stale query error = %v", err)
	}
	assertRead(t, db, 60+ExperimentalHistoryLength, second)
	write(t, db, ExperimentalHistoryLength, math.MaxUint64, first)
	assertRead(t, db, math.MaxUint64, first)
}

func TestNativeReadBounds(t *testing.T) {
	db, _ := testState(t, 3)
	write(t, db, 3, 1, common.Hash{7})
	overflow := query(1)
	overflow[55] = 1
	highBit := query(1)
	highBit[0] = 0x80
	for _, input := range [][]byte{nil, make([]byte, 32), query(1)[:63], append(query(1), 0), query(0), overflow, highBit, query(2)} {
		if _, err := rawCall(db, testReader, input, 2, true); !errors.Is(err, vm.ErrExecutionReverted) {
			t.Errorf("query %x: error = %v", input, err)
		}
	}
	assertRead(t, db, 1, common.Hash{7})
}

func TestNativeWriteBoundsAndFullWidthCaller(t *testing.T) {
	db, _ := testState(t, 3)
	root := common.Hash{0x43}
	for _, offset := range []int{0, 19, 31, 32, 63} {
		caller := testSystem
		caller[offset] ^= 1
		if _, err := rawCall(db, caller, root[:], 1, false); !errors.Is(err, vm.ErrExecutionReverted) {
			t.Fatalf("caller mismatch byte %d: %v", offset, err)
		}
	}
	for _, input := range [][]byte{nil, root[:31], append(root[:], 0), make([]byte, 64)} {
		if _, err := rawCall(db, testSystem, input, 1, false); !errors.Is(err, vm.ErrExecutionReverted) {
			t.Errorf("write length %d error = %v", len(input), err)
		}
	}
	if _, err := rawCall(db, testSystem, root[:], 0, false); !errors.Is(err, vm.ErrExecutionReverted) {
		t.Fatalf("zero timestamp error = %v", err)
	}
	if _, err := rawCall(db, testSystem, root[:], 1, true); !errors.Is(err, vm.ErrWriteProtection) {
		t.Fatalf("static write error = %v", err)
	}
	write(t, db, 3, 1, root)
	assertRead(t, db, 1, root)
}

func TestNativeAuthorizationUsesCallerAndRejectsValue(t *testing.T) {
	db, _ := testState(t, 3)
	block := testBlock(1)
	rules := params.TestChainConfig.Rules(block.BlockNumber, block.Time)
	db.Prepare(rules, testSystem, block.Coinbase, &testAddress, vm.ActivePrecompiles(rules), nil)
	// A privileged ORIGIN does not grant the same authority to its callee.
	engine := vm.NewQRVM(block, vm.TxContext{Origin: testSystem, GasPrice: new(big.Int)}, db, params.TestChainConfig, vm.Config{})
	_, _, err := engine.Call(vm.AccountRef(testReader), testAddress, make([]byte, 32), ExperimentalWriteGas, new(big.Int))
	if !errors.Is(err, vm.ErrExecutionReverted) {
		t.Fatalf("origin-based authorization error = %v", err)
	}
	db.SetBalance(testSystem, big.NewInt(1))
	engine = vm.NewQRVM(block, vm.TxContext{Origin: testSystem, GasPrice: new(big.Int)}, db, params.TestChainConfig, vm.Config{})
	_, _, err = engine.Call(vm.AccountRef(testSystem), testAddress, make([]byte, 32), ExperimentalWriteGas, big.NewInt(1))
	if !errors.Is(err, vm.ErrExecutionReverted) || db.GetBalance(testSystem).Cmp(big.NewInt(1)) != 0 || db.GetBalance(testAddress).Sign() != 0 {
		t.Fatalf("value-bearing system call did not revert its transfer: %v", err)
	}
}

func TestHistorySurvivesCommitAndSnapshotRollback(t *testing.T) {
	db, _ := testState(t, 3)
	root := common.Hash{0: 0x87, 31: 0x29}
	// The helper runs with no user transaction, covering empty-block semantics
	// at the helper boundary. Production block processors remain unwired.
	write(t, db, 3, 9, root)
	stateRoot, err := db.Commit(1, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Database().TrieDB().Commit(stateRoot, false); err != nil {
		t.Fatal(err)
	}
	reopened, err := state.New(stateRoot, db.Database(), nil)
	if err != nil {
		t.Fatal(err)
	}
	assertRead(t, reopened, 9, root)
	snapshot := reopened.Snapshot()
	write(t, reopened, 3, 12, common.Hash{4})
	reopened.RevertToSnapshot(snapshot)
	assertRead(t, reopened, 9, root)
	if _, err := rawCall(reopened, testReader, query(12), 13, true); !errors.Is(err, vm.ErrExecutionReverted) {
		t.Fatalf("reverted branch query error = %v", err)
	}
}

func TestSystemCallPreservesTransactionContext(t *testing.T) {
	db, code := testState(t, 3)
	warm := common.BytesToAddress([]byte{0xaa, 0xbb})
	slot := common.Hash{9}
	db.AddSlotToAccessList(warm, slot)
	db.AddRefund(17)
	db.SetNonce(testSystem, 12)
	db.SetBalance(testSystem, big.NewInt(45))
	txHash := common.Hash{23}
	db.SetTxContext(txHash, 5)
	oldLog := &types.Log{Address: warm, Data: []byte{1}}
	db.AddLog(oldLog)
	// A committed nonzero root followed by zero exercises the SSTORE refund
	// branch; the local refund counter must not change the outer transaction.
	write(t, db, 3, 1, common.Hash{8})
	db.Finalise(true)
	db.AddRefund(17)
	db.AddSlotToAccessList(warm, slot)
	_, err := ExecuteSystemCall(db, testBlock(4), params.TestChainConfig, SystemCall{
		Caller: testSystem, Target: testAddress, Runtime: code, Input: make([]byte, 32), GasLimit: ExperimentalWriteGas,
	})
	if err != nil {
		t.Fatal(err)
	}
	if db.GetRefund() != 17 || db.AddressInAccessList(testAddress) || db.AddressInAccessList(testSystem) {
		t.Fatal("system operation leaked transaction warmth or refunds")
	}
	if address, storage := db.SlotInAccessList(warm, slot); !address || !storage {
		t.Fatal("system operation lost existing access list")
	}
	if db.GetNonce(testSystem) != 12 || db.GetBalance(testSystem).Cmp(big.NewInt(45)) != 0 || db.TxIndex() != 5 {
		t.Fatal("system operation changed nonce, balance, or transaction context")
	}
	if len(db.Logs()) != 1 || db.Logs()[0] != oldLog {
		t.Fatal("system operation changed existing logs")
	}
}

func TestSystemCallRuntimeIdentityAndGas(t *testing.T) {
	for _, mode := range []string{"missing", "wrong", "out-of-gas", "zero-gas", "excess-gas"} {
		t.Run(mode, func(t *testing.T) {
			db, code := testState(t, 3)
			call := SystemCall{Caller: testSystem, Target: testAddress, Runtime: code, Input: make([]byte, 32), GasLimit: ExperimentalWriteGas}
			want := ErrRuntimeIdentity
			switch mode {
			case "missing":
				db.SetCode(testAddress, nil)
			case "wrong":
				db.SetCode(testAddress, []byte{byte(vm.STOP)})
			case "out-of-gas":
				call.GasLimit, want = 1, vm.ErrOutOfGas
			case "zero-gas":
				call.GasLimit, want = 0, ErrSystemCall
			case "excess-gas":
				call.GasLimit, want = MaxSystemCallGas+1, ErrSystemCall
			}
			if _, err := ExecuteSystemCall(db, testBlock(1), params.TestChainConfig, call); !errors.Is(err, want) {
				t.Fatalf("error = %v, want %v", err, want)
			}
			if value := db.GetState(testAddress, common.BigToHash(big.NewInt(1))); value != (common.StorageValue64{}) {
				t.Fatalf("failed system operation changed storage: %x", value)
			}
		})
	}
}

type writeObserver struct {
	vm.StateDB
	writes int
}

func (db *writeObserver) SetState(address common.Address, slot common.Hash, value common.StorageValue64) {
	db.writes++
	db.StateDB.SetState(address, slot, value)
}

func TestSystemCallRevertsPartialWrites(t *testing.T) {
	db, _ := testState(t, 3)
	// A reviewed runtime identity can still fail after persistent writes.
	code := []byte{byte(vm.PUSH1), 7, byte(vm.PUSH1), 1, byte(vm.SSTORE), byte(vm.PUSH1), 0, byte(vm.PUSH1), 0, byte(vm.REVERT)}
	db.SetCode(testAddress, code)
	observed := &writeObserver{StateDB: db}
	_, err := ExecuteSystemCall(observed, testBlock(1), params.TestChainConfig, SystemCall{
		Caller: testSystem, Target: testAddress, Runtime: code, GasLimit: ExperimentalWriteGas,
	})
	if !errors.Is(err, vm.ErrExecutionReverted) || observed.writes != 1 {
		t.Fatalf("partial-write revert: error %v, observed writes %d", err, observed.writes)
	}
	if value := db.GetState(testAddress, common.BigToHash(big.NewInt(1))); value != (common.StorageValue64{}) {
		t.Fatalf("reverted storage = %x", value)
	}
}

func TestSystemCallStartsFreshStorageAccounting(t *testing.T) {
	db, _ := testState(t, 3)
	slot := common.BigToHash(big.NewInt(1))
	initial := common.BytesToStorageValue64([]byte{9})
	db.SetState(testAddress, slot, initial)
	db.Finalise(true)
	// Simulate an earlier unfinalized transaction that cleared a nonzero slot.
	// The independent system operation sees zero as its original value. Reusing
	// db's older original would attempt to subtract a refund it never accrued.
	db.SetState(testAddress, slot, common.StorageValue64{})
	db.AddRefund(23)
	code := []byte{byte(vm.PUSH1), 7, byte(vm.PUSH1), 1, byte(vm.SSTORE), byte(vm.STOP)}
	db.SetCode(testAddress, code)
	_, err := ExecuteSystemCall(db, testBlock(1), params.TestChainConfig, SystemCall{
		Caller: testSystem, Target: testAddress, Runtime: code, GasLimit: ExperimentalWriteGas,
	})
	if err != nil {
		t.Fatal(err)
	}
	if value := db.GetState(testAddress, slot); value != common.BytesToStorageValue64([]byte{7}) {
		t.Fatalf("system write = %x", value)
	}
	if db.GetRefund() != 23 {
		t.Fatal("system operation changed outer refund")
	}
}

func TestSystemCallRejectsPrecompileTarget(t *testing.T) {
	db, code := testState(t, 3)
	target := vm.ActivePrecompiles(params.TestRules)[0]
	db.SetCode(target, code)
	_, err := ExecuteSystemCall(db, testBlock(1), params.TestChainConfig, SystemCall{
		Caller: testSystem, Target: target, Runtime: code, Input: make([]byte, 32), GasLimit: ExperimentalWriteGas,
	})
	if !errors.Is(err, ErrSystemCall) {
		t.Fatalf("precompile call error = %v", err)
	}
}

func TestRuntimeConfigurationBounds(t *testing.T) {
	for _, capacity := range []uint64{0, math.MaxUint64/2 + 1, math.MaxUint64} {
		if _, err := Runtime(testSystem, capacity); err == nil {
			t.Errorf("accepted capacity %d", capacity)
		}
	}
	if _, err := Runtime(common.Address{}, 3); err == nil {
		t.Fatal("accepted zero system caller")
	}
	for _, capacity := range []uint64{1, ExperimentalHistoryLength, math.MaxUint64 / 2} {
		db, _ := testState(t, capacity)
		write(t, db, capacity, math.MaxUint64, common.Hash{1})
		assertRead(t, db, math.MaxUint64, common.Hash{1})
	}
}
