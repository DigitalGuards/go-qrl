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
// Ordinary callers submit exactly 40 bytes and pay at least the current fee,
// or read the fee with empty input and zero value. The fee is EIP-7002's
// fake_exponential(MinimumFee, excess, FeeUpdateFraction), so sustained demand
// above TargetPerBlock raises it exponentially while the FIFO stays unbounded.
// The entire payment, including overpayment, stays in the queue account.
//
// Only systemCaller can drain, using empty input and zero value. The drain
// returns at most MaxPerBlock concatenated 104-byte records without a
// request-type byte, then sets excess to max(0, excess + count - target) and
// resets count. Storage follows EIP-7002: slot 0 excess, slot 1 requests added
// since the last drain, slots 2 and 3 queue head and tail, and three slots per
// record (source, index, key root) from slot 4. Consumed slots clear, and an
// empty queue resets head and tail to zero.
func Runtime(systemCaller common.Address) ([]byte, error) {
	return runtimeCode(systemCaller, MaxPerBlock)
}

// Storage slots of the queue runtime.
const (
	excessSlot  = 0
	countSlot   = 1
	headSlot    = 2
	tailSlot    = 3
	queueOffset = 4
)

// runtimeCode takes the drain bound as a parameter so tests can check that
// the generated drain stays correct when the MaxPerBlock fixture changes.
func runtimeCode(systemCaller common.Address, maxPerBlock int) ([]byte, error) {
	if systemCaller == (common.Address{}) {
		return nil, errors.New("system caller must be nonzero")
	}
	if maxPerBlock < 1 || maxPerBlock*RecordBytes > baseMemory {
		return nil, fmt.Errorf("drain bound %d outside 1..%d", maxPerBlock, baseMemory/RecordBytes)
	}
	a := &assembler{labels: make(map[string]int)}
	a.op(vm.CALLER)
	a.push(systemCaller[:])
	a.op(vm.EQ)
	a.jump("drain", true)

	a.fee()
	// Empty input with zero value returns the current fee as one word.
	a.op(vm.CALLDATASIZE)
	a.jump("submit", true)
	a.op(vm.CALLVALUE)
	a.jump("fail", true)
	a.num(0)
	a.op(vm.MSTORE)
	a.ret(64)

	a.label("submit")
	a.op(vm.CALLDATASIZE)
	a.num(SubmissionBytes)
	a.op(vm.EQ, vm.ISZERO)
	a.jump("fail", true)
	a.op(vm.CALLVALUE, vm.LT)
	a.jump("fail", true)
	a.increment(countSlot)

	// base = queueOffset + 3 * tail.
	a.num(tailSlot)
	a.op(vm.SLOAD)
	a.num(3)
	a.op(vm.MUL)
	a.num(queueOffset)
	a.op(vm.ADD)
	a.num(baseMemory)
	a.op(vm.MSTORE, vm.CALLER)
	a.base(0)
	a.op(vm.SSTORE)

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
	a.increment(tailSlot)
	a.op(vm.STOP)

	a.label("drain")
	a.op(vm.CALLDATASIZE)
	a.jump("fail", true)
	a.op(vm.CALLVALUE)
	a.jump("fail", true)
	// An empty queue before record i finishes with the i records already copied.
	for i := 0; i < maxPerBlock; i++ {
		a.num(headSlot)
		a.op(vm.SLOAD)
		a.num(tailSlot)
		a.op(vm.SLOAD, vm.EQ)
		a.jump(drainedLabel(i), true)
		a.num(headSlot)
		a.op(vm.SLOAD)
		a.num(3)
		a.op(vm.MUL)
		a.num(queueOffset)
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
		a.increment(headSlot)
	}
	a.num(maxPerBlock)
	a.jump("finish", false)
	for i := 0; i < maxPerBlock; i++ {
		a.label(drainedLabel(i))
		a.num(i)
		a.jump("finish", false)
	}

	// Stack: [records]. Reset an empty queue to slot zero, as EIP-7002 does.
	a.label("finish")
	a.num(headSlot)
	a.op(vm.SLOAD)
	a.num(tailSlot)
	a.op(vm.SLOAD, vm.EQ, vm.ISZERO)
	a.jump("excess", true)
	a.num(0)
	a.num(headSlot)
	a.op(vm.SSTORE)
	a.num(0)
	a.num(tailSlot)
	a.op(vm.SSTORE)

	// excess = max(0, excess + count - TargetPerBlock), then count = 0.
	a.label("excess")
	a.num(countSlot)
	a.op(vm.SLOAD)
	a.num(excessSlot)
	a.op(vm.SLOAD, vm.ADD, vm.DUP1)
	a.num(TargetPerBlock)
	a.op(vm.LT)
	a.jump("aboveTarget", true)
	a.op(vm.POP)
	a.num(0)
	a.jump("storeExcess", false)
	a.label("aboveTarget")
	a.num(TargetPerBlock)
	a.op(vm.SWAP1, vm.SUB)
	a.label("storeExcess")
	a.num(excessSlot)
	a.op(vm.SSTORE)
	a.num(0)
	a.num(countSlot)
	a.op(vm.SSTORE)
	a.num(RecordBytes)
	a.op(vm.MUL)
	a.num(0)
	a.op(vm.RETURN)

	a.label("fail")
	a.num(0)
	a.num(0)
	a.op(vm.REVERT)
	return a.finish(), nil
}

const baseMemory = 1024

func drainedLabel(records int) string {
	return fmt.Sprintf("drained%d", records)
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

// fee leaves fake_exponential(MinimumFee, excess, FeeUpdateFraction) on the
// stack, the EIP-4844 helper that EIP-7002 reuses. Stack during the loop, top
// first: accumulator, output, i. 64-byte words cannot overflow before the fee
// exceeds any payable amount, so admission is unreachable well before then.
func (a *assembler) fee() {
	a.num(1)
	a.num(0)
	a.num(MinimumFee * FeeUpdateFraction)
	a.label("feeLoop")
	a.op(vm.DUP1, vm.ISZERO)
	a.jump("feeDone", true)
	// output += accumulator
	a.op(vm.DUP1, vm.SWAP2, vm.ADD, vm.SWAP1)
	// accumulator = accumulator * excess / (FeeUpdateFraction * i)
	a.num(excessSlot)
	a.op(vm.SLOAD, vm.MUL, vm.DUP3)
	a.num(FeeUpdateFraction)
	a.op(vm.MUL, vm.SWAP1, vm.DIV)
	// i += 1
	a.op(vm.SWAP2)
	a.num(1)
	a.op(vm.ADD, vm.SWAP2)
	a.jump("feeLoop", false)
	a.label("feeDone")
	a.op(vm.POP)
	a.num(FeeUpdateFraction)
	a.op(vm.SWAP1, vm.DIV, vm.SWAP1, vm.POP)
}

// increment adds one to a storage slot.
func (a *assembler) increment(slot int) {
	a.num(slot)
	a.op(vm.SLOAD)
	a.num(1)
	a.op(vm.ADD)
	a.num(slot)
	a.op(vm.SSTORE)
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
