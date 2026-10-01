// Package stakingroots implements an experimental native beacon-root history.
// It has no chain registration, activation rule, or assigned protocol addresses.
package stakingroots

import (
	"encoding/binary"
	"errors"
	"math"

	"github.com/theQRL/go-qrl/common"
	"github.com/theQRL/go-qrl/core/vm"
)

// ExperimentalHistoryLength is a research fixture, pending protocol selection.
const ExperimentalHistoryLength uint64 = 8191

// Runtime builds native QRVM code for a timestamp/root ring. Reads take exactly
// one 64-byte big-endian uint64 word and return exactly 32 bytes. The system
// caller supplies exactly 32 root bytes; TIMESTAMP supplies the index timestamp.
// Every caller comparison uses all 64 address bytes.
func Runtime(systemCaller common.Address, capacity uint64) ([]byte, error) {
	if systemCaller == (common.Address{}) || capacity == 0 || capacity > math.MaxUint64/2 {
		return nil, errors.New("invalid experimental beacon-root runtime configuration")
	}
	a := newAssembler()
	a.op(vm.CALLVALUE)
	a.jump("invalid")
	a.op(vm.CALLER)
	a.push(systemCaller[:])
	a.op(vm.EQ)
	a.jump("write")

	// Read: check exact native word width, nonzero timestamp and uint64 bounds.
	a.op(vm.CALLDATASIZE)
	a.number(64)
	a.op(vm.EQ, vm.ISZERO)
	a.jump("invalid")
	a.number(0)
	a.op(vm.CALLDATALOAD, vm.DUP1, vm.ISZERO)
	a.jump("invalid")
	a.op(vm.DUP1)
	a.number(64)
	a.op(vm.SHR)
	a.jump("invalid")

	// Stack: timestamp, index. A matching timestamp authenticates the ring slot.
	a.op(vm.DUP1)
	a.number(capacity)
	a.op(vm.SWAP1, vm.MOD, vm.DUP1, vm.SLOAD, vm.DUP3, vm.EQ, vm.ISZERO)
	a.jump("invalid")
	a.number(capacity)
	a.op(vm.ADD, vm.SLOAD)
	a.number(0)
	a.op(vm.MSTORE)
	// The stored root occupies the high 32 bytes of the native memory word.
	a.number(32)
	a.number(0)
	a.op(vm.RETURN)

	a.label("write")
	a.op(vm.CALLDATASIZE)
	a.number(32)
	a.op(vm.EQ, vm.ISZERO)
	a.jump("invalid")
	a.op(vm.TIMESTAMP, vm.ISZERO)
	a.jump("invalid")
	a.op(vm.TIMESTAMP)
	a.number(capacity)
	a.op(vm.TIMESTAMP, vm.MOD, vm.SSTORE)
	a.number(0)
	a.op(vm.CALLDATALOAD)
	a.number(capacity)
	a.op(vm.TIMESTAMP, vm.MOD)
	a.number(capacity)
	a.op(vm.ADD, vm.SSTORE, vm.STOP)

	a.label("invalid")
	a.number(0)
	a.number(0)
	a.op(vm.REVERT)
	return a.finish(), nil
}

type fixup struct {
	offset int
	label  string
}

// assembler uses this client's native opcode constants, including PUSH64.
type assembler struct {
	code   []byte
	labels map[string]int
	fixups []fixup
}

func newAssembler() *assembler { return &assembler{labels: make(map[string]int)} }

func (a *assembler) op(ops ...vm.OpCode) {
	for _, op := range ops {
		a.code = append(a.code, byte(op))
	}
}

func (a *assembler) push(value []byte) {
	if len(value) == 0 || len(value) > 64 {
		panic("invalid assembler push width")
	}
	a.op(vm.PUSH1 + vm.OpCode(len(value)-1))
	a.code = append(a.code, value...)
}

func (a *assembler) number(value uint64) {
	var word [8]byte
	binary.BigEndian.PutUint64(word[:], value)
	i := 0
	for i < len(word)-1 && word[i] == 0 {
		i++
	}
	a.push(word[i:])
}

func (a *assembler) jump(label string) {
	a.op(vm.PUSH2)
	a.fixups = append(a.fixups, fixup{offset: len(a.code), label: label})
	a.code = append(a.code, 0, 0)
	a.op(vm.JUMPI)
}

func (a *assembler) label(label string) {
	if _, exists := a.labels[label]; exists {
		panic("duplicate assembler label")
	}
	a.labels[label] = len(a.code)
	a.op(vm.JUMPDEST)
}

func (a *assembler) finish() []byte {
	for _, fix := range a.fixups {
		offset, ok := a.labels[fix.label]
		if !ok || offset > math.MaxUint16 {
			panic("invalid assembler jump target")
		}
		binary.BigEndian.PutUint16(a.code[fix.offset:fix.offset+2], uint16(offset))
	}
	return a.code
}
