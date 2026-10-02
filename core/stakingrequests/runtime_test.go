package stakingrequests

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/big"
	"reflect"
	"testing"

	"github.com/theQRL/go-qrl/common"
	"github.com/theQRL/go-qrl/core/rawdb"
	"github.com/theQRL/go-qrl/core/stakingroots"
	"github.com/theQRL/go-qrl/core/state"
	"github.com/theQRL/go-qrl/core/types"
	"github.com/theQRL/go-qrl/core/vm"
	"github.com/theQRL/go-qrl/params"
)

type queueFixture struct {
	db                    *state.StateDB
	address, system, user common.Address
	chain                 *params.ChainConfig
}

func newQueue(t *testing.T) *queueFixture {
	t.Helper()
	db, err := state.New(types.EmptyRootHash, state.NewDatabase(rawdb.NewMemoryDatabase()), nil)
	if err != nil {
		t.Fatal(err)
	}
	f := &queueFixture{
		db:      db,
		address: common.BytesToAddress(bytes.Repeat([]byte{0x77}, 64)),
		system:  common.BytesToAddress(bytes.Repeat([]byte{0xff}, 64)),
		user:    common.BytesToAddress(bytes.Repeat([]byte{0x11}, 64)),
		chain:   &params.ChainConfig{ChainID: big.NewInt(1)},
	}
	code, err := Runtime(f.system)
	if err != nil {
		t.Fatal(err)
	}
	db.SetCode(f.address, code)
	db.SetBalance(f.user, big.NewInt(1_000_000))
	return f
}

// call runs one top-level call. It builds the QRVM directly because
// core/vm/runtime imports core, which imports this package.
func (f *queueFixture) call(caller, target common.Address, data []byte, value int64, gas uint64) ([]byte, error) {
	block := vm.BlockContext{
		BlockNumber: big.NewInt(1), Time: 60, GasLimit: 30_000_000, BaseFee: new(big.Int), Random: new(common.Hash),
		CanTransfer: func(s vm.StateDB, from common.Address, amount *big.Int) bool {
			return s.GetBalance(from).Cmp(amount) >= 0
		},
		Transfer: func(s vm.StateDB, from, to common.Address, amount *big.Int) {
			s.SubBalance(from, amount)
			s.AddBalance(to, amount)
		},
		GetHash: func(uint64) common.Hash { return common.Hash{} },
	}
	rules := f.chain.Rules(block.BlockNumber, block.Time)
	f.db.Prepare(rules, caller, block.Coinbase, &target, vm.ActivePrecompiles(rules), nil)
	qrvm := vm.NewQRVM(block, vm.TxContext{Origin: caller, GasPrice: new(big.Int)}, f.db, f.chain, vm.Config{})
	out, _, err := qrvm.Call(vm.AccountRef(caller), target, data, gas, big.NewInt(value))
	return out, err
}

func (f *queueFixture) submit(t *testing.T, index uint64) Request {
	t.Helper()
	request := Request{Source: f.user, ValidatorIndex: index, PublicKeyRoot: common.HexToHash("123456789abcdef123456789abcdef")}
	if out, err := f.call(f.user, f.address, request.Submission(), 1, 1_000_000); err != nil || len(out) != 0 {
		t.Fatalf("admission output %x, error %v", out, err)
	}
	return request
}

func (f *queueFixture) drain() ([]Request, error) {
	return Drain(f.db, vm.BlockContext{BlockNumber: big.NewInt(1), Time: 60, BaseFee: new(big.Int)}, f.chain, f.address, f.system)
}

func (f *queueFixture) slot(n int) uint64 {
	value := f.db.GetState(f.address, common.BigToHash(big.NewInt(int64(n))))
	return new(big.Int).SetBytes(value[:]).Uint64()
}

func TestQueueFIFOExcessAndReset(t *testing.T) {
	f := newQueue(t)
	want := make([]Request, 0, 5)
	for i := 0; i < 5; i++ {
		want = append(want, f.submit(t, uint64(i)+0x0102030405060708))
	}
	if f.slot(countSlot) != 5 || f.slot(headSlot) != 0 || f.slot(tailSlot) != 5 {
		t.Fatalf("count %d, head %d, tail %d", f.slot(countSlot), f.slot(headSlot), f.slot(tailSlot))
	}
	// The first drain adds count - target to the excess; later empty-count
	// drains lower it by the target, as EIP-7002 does once per block.
	for _, excess := range []uint64{4, 3, 2} {
		got, err := f.drain()
		n := min(MaxPerBlock, len(want))
		if err != nil || !reflect.DeepEqual(got, want[:n]) {
			t.Fatalf("got %#v, want %#v, error %v", got, want[:n], err)
		}
		want = want[n:]
		if f.slot(excessSlot) != excess || f.slot(countSlot) != 0 {
			t.Fatalf("excess %d, count %d, want excess %d", f.slot(excessSlot), f.slot(countSlot), excess)
		}
	}
	if f.slot(headSlot) != 0 || f.slot(tailSlot) != 0 {
		t.Fatal("empty queue did not reset head and tail")
	}
	got, err := f.drain()
	if err != nil || len(got) != 0 || f.slot(excessSlot) != 1 {
		t.Fatalf("empty drain: %#v, excess %d, error %v", got, f.slot(excessSlot), err)
	}
	for slot := queueOffset; slot < queueOffset+3*5; slot++ {
		if f.slot(slot) != 0 {
			t.Fatalf("consumed slot %d remains set", slot)
		}
	}
	next := f.submit(t, 99)
	if got, err := f.drain(); err != nil || len(got) != 1 || got[0] != next {
		t.Fatalf("request after reset: %#v, %v", got, err)
	}
}

func TestQueueFeeFollowsExcess(t *testing.T) {
	// Reference values from an independent fake_exponential implementation.
	for excess, want := range map[uint64]int64{0: 1, 12: 1, 13: 2, 17: 2, 34: 7, 100: 357, 200: 128545} {
		if got := Fee(excess); got.Cmp(big.NewInt(want)) != 0 {
			t.Fatalf("Fee(%d) = %v, want %d", excess, got, want)
		}
	}
	f := newQueue(t)
	request := Request{ValidatorIndex: 3, PublicKeyRoot: common.Hash{31: 1}}
	for _, excess := range []uint64{0, 13, 34, 100, 1000, 5000} {
		var word common.StorageValue64
		new(big.Int).SetUint64(excess).FillBytes(word[:])
		f.db.SetState(f.address, common.BigToHash(big.NewInt(excessSlot)), word)
		fee := Fee(excess)
		out, err := f.call(f.user, f.address, nil, 0, 10_000_000)
		if err != nil || len(out) != 64 || new(big.Int).SetBytes(out).Cmp(fee) != 0 {
			t.Fatalf("excess %d: fee %x, want %v, error %v", excess, out, fee, err)
		}
		if !fee.IsInt64() || fee.Int64() > 1000 {
			continue
		}
		if _, err := f.call(f.user, f.address, request.Submission(), fee.Int64()-1, 1_000_000); !errors.Is(err, vm.ErrExecutionReverted) {
			t.Fatalf("excess %d: underpayment %v", excess, err)
		}
		if _, err := f.call(f.user, f.address, request.Submission(), fee.Int64(), 1_000_000); err != nil {
			t.Fatalf("excess %d: exact fee %v", excess, err)
		}
	}
	if _, err := f.call(f.user, f.address, nil, 1, 1_000_000); !errors.Is(err, vm.ErrExecutionReverted) {
		t.Fatalf("fee read accepted value: %v", err)
	}
}

func TestQueueAdmissionValidationAndRetainedFees(t *testing.T) {
	f := newQueue(t)
	request := Request{ValidatorIndex: ^uint64(0), PublicKeyRoot: common.Hash{31: 1}}
	for _, size := range []int{0, 39, 41, 104} {
		if _, err := f.call(f.user, f.address, make([]byte, size), 1, 1_000_000); !errors.Is(err, vm.ErrExecutionReverted) {
			t.Fatalf("width %d: error %v", size, err)
		}
	}
	if _, err := f.call(f.user, f.address, request.Submission(), 0, 1_000_000); !errors.Is(err, vm.ErrExecutionReverted) {
		t.Fatalf("underpayment: %v", err)
	}
	if _, err := f.call(f.user, f.address, request.Submission(), 5, 1_000_000); err != nil {
		t.Fatal(err)
	}
	request.Source = f.user
	got, err := f.drain()
	if err != nil || len(got) != 1 || got[0] != request {
		t.Fatalf("got %#v, error %v", got, err)
	}
	if f.db.GetBalance(f.address).Cmp(big.NewInt(5)) != 0 {
		t.Fatal("prototype must retain the full admission payment")
	}
}

func TestQueueSystemCallerUsesFullWidthAndEmptyZeroValue(t *testing.T) {
	f := newQueue(t)
	want := f.submit(t, 1)
	lookalike := f.system
	lookalike[0] ^= 1
	// Empty input from any other caller, including a one-bit lookalike of the
	// system caller, reads the fee and leaves the queue untouched.
	for _, caller := range []common.Address{f.user, lookalike} {
		out, err := f.call(caller, f.address, nil, 0, 1_000_000)
		if err != nil || len(out) != 64 || new(big.Int).SetBytes(out).Cmp(Fee(0)) != 0 {
			t.Fatalf("non-system empty call: %x, %v", out, err)
		}
	}
	f.db.SetBalance(f.system, big.NewInt(1))
	for _, tc := range []struct {
		data  []byte
		value int64
	}{{want.Submission(), 0}, {nil, 1}} {
		if _, err := f.call(f.system, f.address, tc.data, tc.value, 1_000_000); !errors.Is(err, vm.ErrExecutionReverted) {
			t.Fatalf("invalid system input/value: %v", err)
		}
	}
	got, err := f.drain()
	if err != nil || len(got) != 1 || got[0] != want {
		t.Fatalf("unauthorized call changed queue: %#v, %v", got, err)
	}
}

func forwardingCode(target common.Address, revert bool) []byte {
	a := &assembler{labels: make(map[string]int)}
	a.op(vm.CALLDATASIZE)
	a.num(0)
	a.num(0)
	a.op(vm.CALLDATACOPY)
	a.num(0)
	a.num(0)
	a.op(vm.CALLDATASIZE)
	a.num(0)
	a.op(vm.CALLVALUE)
	a.push(target[:])
	a.push([]byte{0x0f, 0x42, 0x40})
	a.op(vm.CALL, vm.ISZERO)
	a.jump("revert", true)
	if !revert {
		a.op(vm.STOP)
	}
	a.label("revert")
	a.num(0)
	a.num(0)
	a.op(vm.REVERT)
	return a.finish()
}

func TestQueueImmediateContractCallerAndEnclosingRevert(t *testing.T) {
	f := newQueue(t)
	forwarder := f.user
	forwarder[0] ^= 0x80 // Preserve the low 20 bytes to catch address truncation.
	f.db.SetCode(forwarder, forwardingCode(f.address, false))
	request := Request{ValidatorIndex: 19, PublicKeyRoot: common.Hash{0: 0x80, 31: 1}}
	if _, err := f.call(f.user, forwarder, request.Submission(), 1, 2_000_000); err != nil {
		t.Fatal(err)
	}
	got, err := f.drain()
	request.Source = forwarder
	if err != nil || len(got) != 1 || got[0] != request {
		t.Fatalf("immediate caller lost: %#v, error %v", got, err)
	}
	f.db.SetCode(forwarder, forwardingCode(f.address, true))
	beforeUser := new(big.Int).Set(f.db.GetBalance(f.user))
	beforeQueue := new(big.Int).Set(f.db.GetBalance(f.address))
	if _, err := f.call(f.user, forwarder, request.Submission(), 1, 2_000_000); !errors.Is(err, vm.ErrExecutionReverted) {
		t.Fatalf("outer revert: %v", err)
	}
	if f.db.GetBalance(f.user).Cmp(beforeUser) != 0 || f.db.GetBalance(f.address).Cmp(beforeQueue) != 0 {
		t.Fatal("outer revert failed to restore balances")
	}
	got, err = f.drain()
	if err != nil || len(got) != 0 {
		t.Fatalf("outer revert retained queued request: %#v, %v", got, err)
	}
}

func TestQueueDrainSnapshotRollbackAndRuntimeIdentity(t *testing.T) {
	f := newQueue(t)
	want := f.submit(t, 8)
	snapshot := f.db.Snapshot()
	if _, err := f.drain(); err != nil {
		t.Fatal(err)
	}
	f.db.RevertToSnapshot(snapshot)
	got, err := f.drain()
	if err != nil || len(got) != 1 || got[0] != want {
		t.Fatalf("drain snapshot rollback: %#v, %v", got, err)
	}
	f.submit(t, 9)
	code := append([]byte{}, f.db.GetCode(f.address)...)
	f.db.SetCode(f.address, []byte{byte(vm.STOP)})
	if _, err := f.drain(); !errors.Is(err, stakingroots.ErrRuntimeIdentity) {
		t.Fatalf("wrong runtime accepted: %v", err)
	}
	f.db.SetCode(f.address, code)
	got, err = f.drain()
	if err != nil || len(got) != 1 || got[0].ValidatorIndex != 9 {
		t.Fatalf("identity rejection changed queue: %#v, %v", got, err)
	}
}

func TestQueueAdmissionOutOfGasRollsBack(t *testing.T) {
	f := newQueue(t)
	request := Request{ValidatorIndex: 7, PublicKeyRoot: common.Hash{31: 1}}
	beforeUser := new(big.Int).Set(f.db.GetBalance(f.user))
	if _, err := f.call(f.user, f.address, request.Submission(), 1, 30_000); !errors.Is(err, vm.ErrOutOfGas) {
		t.Fatalf("expected bounded OOG: %v", err)
	}
	got, err := f.drain()
	if err != nil || len(got) != 0 || f.db.GetBalance(f.user).Cmp(beforeUser) != 0 || f.db.GetBalance(f.address).Sign() != 0 {
		t.Fatalf("OOG failed to roll back: %#v, %v", got, err)
	}
}

func TestQueueDrainHonorsLargerBound(t *testing.T) {
	const bound = 3
	f := newQueue(t)
	code, err := runtimeCode(f.system, bound)
	if err != nil {
		t.Fatal(err)
	}
	f.db.SetCode(f.address, code)
	want := make([]Request, 0, bound+1)
	for i := 0; i < bound+1; i++ {
		want = append(want, f.submit(t, uint64(i)+1))
	}
	for _, size := range []int{bound, 1, 0} {
		out, err := f.call(f.system, f.address, nil, 0, 1_000_000)
		if err != nil || len(out) != size*RecordBytes {
			t.Fatalf("drain of %d: got %d bytes, error %v", size, len(out), err)
		}
		for i := 0; i < size; i++ {
			got, err := Decode(out[i*RecordBytes : (i+1)*RecordBytes])
			if err != nil || got != want[i] {
				t.Fatalf("record %d: got %#v, want %#v, error %v", i, got, want[i], err)
			}
		}
		want = want[size:]
	}
	for _, bound := range []int{0, baseMemory/RecordBytes + 1} {
		if _, err := runtimeCode(f.system, bound); err == nil {
			t.Fatalf("accepted drain bound %d", bound)
		}
	}
}

// The system call rejects any account whose code differs from the assembled
// runtime, so these bytes are consensus-critical once a fork activates. Pin
// them to catch silent changes in the assembler or in vm.OpCode values.
func TestRuntimeBytecodeGolden(t *testing.T) {
	queue, err := Runtime(stakingroots.ExperimentalSystemCaller())
	if err != nil {
		t.Fatal(err)
	}
	roots, err := stakingroots.Runtime(stakingroots.ExperimentalSystemCaller(), stakingroots.ExperimentalHistoryLength)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		name, want string
		code       []byte
	}{
		{"exit queue", "33081c4de0a60a2723b1c4146f5f6a9cb336f7d75863e0a9e56c14f6ef257ed3", queue},
		{"root history", "02cac1b87f68f917545ee5b768c2054e87de336233d74cd8487965e8f66acaae", roots},
	} {
		sum := sha256.Sum256(item.code)
		if got := hex.EncodeToString(sum[:]); got != item.want {
			t.Errorf("%s runtime sha256 %s, want %s", item.name, got, item.want)
		}
	}
}

// A burst of minimum-fee submissions cannot fill the queue: admission stays
// open, the drain raises the fee for the next block, and FIFO order holds.
func TestQueueSpamRaisesFeeWithoutBlockingAdmission(t *testing.T) {
	f := newQueue(t)
	const spam = 50
	for i := 0; i < spam; i++ {
		f.submit(t, uint64(1000+i))
	}
	honest := f.submit(t, 7)
	if _, err := f.drain(); err != nil {
		t.Fatal(err)
	}
	excess := uint64(spam + 1 - TargetPerBlock)
	fee := Fee(excess)
	if f.slot(excessSlot) != excess || fee.Cmp(big.NewInt(17)) < 0 {
		t.Fatalf("excess %d, fee %v after burst", f.slot(excessSlot), fee)
	}
	request := Request{ValidatorIndex: 8, PublicKeyRoot: common.Hash{31: 1}}
	if _, err := f.call(f.user, f.address, request.Submission(), 1, 1_000_000); !errors.Is(err, vm.ErrExecutionReverted) {
		t.Fatalf("minimum fee accepted after burst: %v", err)
	}
	if _, err := f.call(f.user, f.address, request.Submission(), fee.Int64(), 1_000_000); err != nil {
		t.Fatalf("current fee rejected: %v", err)
	}
	var last []Request
	for f.slot(tailSlot) != 0 {
		got, err := f.drain()
		if err != nil {
			t.Fatal(err)
		}
		last = append(last, got...)
	}
	if n := len(last); n != spam || last[n-2] != honest || last[n-1].ValidatorIndex != 8 {
		t.Fatalf("drained %d records after the first batch, tail %#v", n, last[max(0, n-2):])
	}
}
