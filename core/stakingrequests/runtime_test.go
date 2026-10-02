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
	"github.com/theQRL/go-qrl/core/vm/runtime"
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

func (f *queueFixture) call(caller, target common.Address, data []byte, value int64, gas uint64) ([]byte, error) {
	out, _, err := runtime.Call(target, data, &runtime.Config{
		State: f.db, Origin: caller, ChainConfig: f.chain, BlockNumber: big.NewInt(1),
		Time: 60, Value: big.NewInt(value), GasLimit: gas,
	})
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

func TestQueueNativeFIFOAndRingRollover(t *testing.T) {
	f := newQueue(t)
	want := make([]Request, 0, MaxPending)
	for i := 0; i < MaxPending; i++ {
		want = append(want, f.submit(t, uint64(i)+0x0102030405060708))
	}
	beforeBalance := new(big.Int).Set(f.db.GetBalance(f.address))
	if _, err := f.call(f.user, f.address, want[0].Submission(), 1, 1_000_000); !errors.Is(err, vm.ErrExecutionReverted) {
		t.Fatalf("full queue error %v", err)
	}
	if f.db.GetBalance(f.address).Cmp(beforeBalance) != 0 {
		t.Fatal("failed admission retained payment")
	}
	for cycle := 0; cycle < 6; cycle++ {
		got, err := f.drain()
		if err != nil || !reflect.DeepEqual(got, want[:2]) {
			t.Fatalf("cycle %d: got %#v, want %#v, error %v", cycle, got, want[:2], err)
		}
		want = want[2:]
		for i := 0; i < 2; i++ {
			want = append(want, f.submit(t, uint64(100+cycle*2+i)))
		}
	}
	for len(want) > 0 {
		got, err := f.drain()
		if err != nil || !reflect.DeepEqual(got, want[:2]) {
			t.Fatalf("drain got %#v, error %v", got, err)
		}
		want = want[2:]
	}
	got, err := f.drain()
	if err != nil || len(got) != 0 {
		t.Fatalf("empty drain: %v", err)
	}
	for slot := int64(16); slot < 40; slot++ {
		if value := f.db.GetState(f.address, common.BigToHash(big.NewInt(slot))); value != (common.StorageValue64{}) {
			t.Fatalf("consumed slot %d remains set", slot)
		}
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
	for _, caller := range []common.Address{f.user, lookalike} {
		if _, err := f.call(caller, f.address, nil, 0, 1_000_000); !errors.Is(err, vm.ErrExecutionReverted) {
			t.Fatalf("unauthorized drain: %v", err)
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
	for _, bound := range []int{0, MaxPending + 1} {
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
		{"exit queue", "5dadc6fb80ac08db59b369eb7d0016a2ae3cc4ef8add75eef3b71979bd09a870", queue},
		{"root history", "02cac1b87f68f917545ee5b768c2054e87de336233d74cd8487965e8f66acaae", roots},
	} {
		sum := sha256.Sum256(item.code)
		if got := hex.EncodeToString(sum[:]); got != item.want {
			t.Errorf("%s runtime sha256 %s, want %s", item.name, got, item.want)
		}
	}
}
