package stakingrequests

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/theQRL/go-qrl/common"
	"github.com/theQRL/go-qrl/core/vm"
)

// Runtime assembles the experimental queue for an explicitly selected system
// caller. It has no deployment address and is never installed automatically.
// The design follows the EIP-7002 withdrawal request contract: an execution
// account, here the validator's withdrawal recipient, pays to enqueue an exit
// and the system caller drains a bounded batch each block.
//
// Ordinary callers submit exactly 40 bytes and pay at least MinimumFee. The
// entire payment, including overpayment, stays in the queue account. This
// prototype has no refund, fee collection, or fee adjustment policy.
//
// Only systemCaller can drain, using empty input and zero value. The drain
// returns at most MaxPerBlock concatenated 104-byte records without a
// request-type byte.
// Storage slots 0 and 1 hold the bounded ring head and count; slots 16 through 39
// hold eight records, each as source, index, and key root. Consumed slots clear.
func Runtime(systemCaller common.Address) ([]byte, error) {
	return runtimeCode(systemCaller, MaxPerBlock)
}

// runtimeCode takes the drain bound as a parameter so tests can check that
// the generated drain stays correct when the MaxPerBlock fixture changes.
func runtimeCode(systemCaller common.Address, maxPerBlock int) ([]byte, error) {
	if systemCaller == (common.Address{}) {
		return nil, errors.New("system caller must be nonzero")
	}
	if maxPerBlock < 1 || maxPerBlock > MaxPending {
		return nil, fmt.Errorf("drain bound %d outside 1..%d", maxPerBlock, MaxPending)
	}
	a := &assembler{labels: make(map[string]int)}
	a.op(vm.CALLER)
	a.push(systemCaller[:])
	a.op(vm.EQ)
	a.jump("drain", true)

	a.op(vm.CALLDATASIZE)
	a.num(SubmissionBytes)
	a.op(vm.EQ, vm.ISZERO)
	a.jump("fail", true)
	a.num(MinimumFee)
	a.op(vm.CALLVALUE, vm.LT)
	a.jump("fail", true)
	a.num(MaxPending)
	a.num(1)
	a.op(vm.SLOAD, vm.LT, vm.ISZERO)
	a.jump("fail", true)

	// base = 16 + 3 * ((head + count) % 8).
	a.num(MaxPending)
	a.num(0)
	a.op(vm.SLOAD)
	a.num(1)
	a.op(vm.SLOAD, vm.ADD, vm.MOD)
	a.num(3)
	a.op(vm.MUL)
	a.num(16)
	a.op(vm.ADD, vm.DUP1)
	a.num(baseMemory)
	a.op(vm.MSTORE, vm.CALLER, vm.SWAP1, vm.SSTORE)

	// A 64-byte CALLDATALOAD places the 8-byte index in the top 64 bits.
	a.num(0)
	a.op(vm.CALLDATALOAD)
	a.num(448)
	a.op(vm.SHR)
	a.base(1)
	a.op(vm.SSTORE)
	a.num(8)
	a.op(vm.CALLDATALOAD)
	a.num(256)
	a.op(vm.SHR)
	a.base(2)
	a.op(vm.SSTORE)
	a.num(1)
	a.op(vm.SLOAD)
	a.num(1)
	a.op(vm.ADD)
	a.num(1)
	a.op(vm.SSTORE, vm.STOP)

	a.label("drain")
	a.op(vm.CALLDATASIZE)
	a.jump("fail", true)
	a.op(vm.CALLVALUE)
	a.jump("fail", true)
	// An empty queue before record i returns the i records already copied.
	for i := 0; i < maxPerBlock; i++ {
		a.num(1)
		a.op(vm.SLOAD, vm.ISZERO)
		a.jump(returnLabel(i), true)
		a.num(0)
		a.op(vm.SLOAD)
		a.num(3)
		a.op(vm.MUL)
		a.num(16)
		a.op(vm.ADD)
		a.num(baseMemory)
		a.op(vm.MSTORE)
		offset := i * RecordBytes
		// Write the right-aligned root first. Source and index then overwrite
		// the preceding zero padding introduced by native 64-byte MSTORE.
		a.base(2)
		a.op(vm.SLOAD)
		a.num(offset + 40)
		a.op(vm.MSTORE)
		a.base(0)
		a.op(vm.SLOAD)
		a.num(offset)
		a.op(vm.MSTORE)
		a.base(1)
		a.op(vm.SLOAD)
		for b := 0; b < 8; b++ {
			a.op(vm.DUP1)
			a.num(b * 8)
			a.op(vm.SHR)
			a.num(255)
			a.op(vm.AND)
			a.num(offset + 64 + b)
			a.op(vm.MSTORE8)
		}
		a.op(vm.POP)
		for slot := 0; slot < 3; slot++ {
			a.num(0)
			a.base(slot)
			a.op(vm.SSTORE)
		}
		a.num(MaxPending)
		a.num(0)
		a.op(vm.SLOAD)
		a.num(1)
		a.op(vm.ADD, vm.MOD)
		a.num(0)
		a.op(vm.SSTORE)
		a.num(1)
		a.num(1)
		a.op(vm.SLOAD, vm.SUB)
		a.num(1)
		a.op(vm.SSTORE)
	}
	a.ret(maxPerBlock * RecordBytes)
	for i := maxPerBlock - 1; i >= 0; i-- {
		a.label(returnLabel(i))
		a.ret(i * RecordBytes)
	}
	a.label("fail")
	a.num(0)
	a.num(0)
	a.op(vm.REVERT)
	return a.finish(), nil
}

const baseMemory = 1024

func returnLabel(records int) string {
	return fmt.Sprintf("return%d", records)
}

// The local assembler emits native opcode constants, including PUSH64. Jump
// targets are fixed-width PUSH2 operands patched after labels have been emitted.
type assembler struct {
	code   []byte
	labels map[string]int
	fixups []fixup
}

type fixup struct {
	offset int
	label  string
}

func (a *assembler) op(ops ...vm.OpCode) {
	for _, op := range ops {
		a.code = append(a.code, byte(op))
	}
}

func (a *assembler) push(value []byte) {
	if len(value) < 1 || len(value) > 64 {
		panic("invalid assembler push width")
	}
	a.code = append(a.code, byte(vm.PUSH1)+byte(len(value)-1))
	a.code = append(a.code, value...)
}

func (a *assembler) num(value int) {
	if value < 0 || value > 65535 {
		panic("assembler integer out of range")
	}
	if value <= 255 {
		a.push([]byte{byte(value)})
	} else {
		a.push([]byte{byte(value >> 8), byte(value)})
	}
}

func (a *assembler) jump(label string, conditional bool) {
	a.op(vm.PUSH2)
	a.fixups = append(a.fixups, fixup{len(a.code), label})
	a.code = append(a.code, 0, 0)
	if conditional {
		a.op(vm.JUMPI)
	} else {
		a.op(vm.JUMP)
	}
}

func (a *assembler) label(label string) {
	if _, exists := a.labels[label]; exists {
		panic("duplicate assembler label")
	}
	a.labels[label] = len(a.code)
	a.op(vm.JUMPDEST)
}

func (a *assembler) base(offset int) {
	a.num(baseMemory)
	a.op(vm.MLOAD)
	if offset != 0 {
		a.num(offset)
		a.op(vm.ADD)
	}
}

func (a *assembler) ret(size int) {
	a.num(size)
	a.num(0)
	a.op(vm.RETURN)
}

func (a *assembler) finish() []byte {
	for _, fixup := range a.fixups {
		target, exists := a.labels[fixup.label]
		if !exists || target > 65535 {
			panic("invalid assembler jump target")
		}
		binary.BigEndian.PutUint16(a.code[fixup.offset:fixup.offset+2], uint16(target))
	}
	return a.code
}
