package types

import (
	"bytes"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/theQRL/go-qrl/common"
	"github.com/theQRL/go-qrl/rlp"
)

func TestBeaconRootHeaderEncoding(t *testing.T) {
	header := &Header{Number: big.NewInt(1), GasLimit: 123, Time: 456, BaseFee: big.NewInt(7), WithdrawalsHash: &EmptyWithdrawalsHash}
	legacy, err := rlp.EncodeToBytes([]any{header.ParentHash, header.Coinbase, header.Root, header.TxHash, header.ReceiptHash, header.Bloom, header.Number, header.GasLimit, header.GasUsed, header.Time, header.Extra, header.Random, header.BaseFee, header.WithdrawalsHash})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := rlp.EncodeToBytes(header)
	if err != nil || !bytes.Equal(encoded, legacy) {
		t.Fatalf("historical header encoding changed: %v", err)
	}
	var historical Header
	if err := rlp.DecodeBytes(legacy, &historical); err != nil || historical.ParentBeaconRoot != nil || historical.Hash() != header.Hash() {
		t.Fatalf("historical header round trip: %v", err)
	}
	legacyHash := header.Hash()
	header.ParentBeaconRoot = &common.Hash{0: 0x89, 31: 0x32}
	encoded, _ = rlp.EncodeToBytes(header)
	var restored Header
	if err := rlp.DecodeBytes(encoded, &restored); err != nil || restored.Hash() != header.Hash() || restored.Hash() == legacyHash {
		t.Fatalf("activated header round trip: %v", err)
	}
	copy := CopyHeader(header)
	copy.ParentBeaconRoot[0] ^= 1
	if *copy.ParentBeaconRoot == *header.ParentBeaconRoot {
		t.Fatal("header copy aliases parent root")
	}
	block := NewBlockWithHeader(header)
	root := block.BeaconRoot()
	root[0] ^= 1
	if *block.BeaconRoot() != *header.ParentBeaconRoot {
		t.Fatal("block root accessor aliases header")
	}
	data, _ := json.Marshal(header)
	if err := json.Unmarshal(data, &restored); err != nil || *restored.ParentBeaconRoot != *header.ParentBeaconRoot {
		t.Fatalf("header JSON round trip: %v", err)
	}
	var fields []rlp.RawValue
	if err := rlp.DecodeBytes(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	fields[len(fields)-1], _ = rlp.EncodeToBytes(make([]byte, 31))
	bad, _ := rlp.EncodeToBytes(fields)
	if err := rlp.DecodeBytes(bad, &restored); err == nil {
		t.Fatal("accepted malformed 31-byte parent root")
	}
}
