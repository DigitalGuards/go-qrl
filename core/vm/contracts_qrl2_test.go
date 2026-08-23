// Copyright 2026 The go-qrl Authors
// This file is part of the go-qrl library.
//
// The go-qrl library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-qrl library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.

package vm

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/theQRL/go-qrl/common"
	"github.com/theQRL/go-qrl/params"
	mldsa87 "github.com/theQRL/go-qrllib/crypto/ml_dsa_87"
)

func TestShake256Hash(t *testing.T) {
	want, err := hex.DecodeString("46b9dd2b0ba88d13233b3feb743eeb243fcd52ea62b81b82b50c27646ed5762fd75dc4ddd8c0f200cb05019d67b592f6fc821c49479ab48640292eacb3b7c4be")
	if err != nil {
		t.Fatal(err)
	}

	precompile := &shake256hash{}
	got, err := precompile.Run(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("unexpected SHAKE256 output: got %x, want %x", got, want)
	}
	if len(got) != 64 {
		t.Fatalf("unexpected SHAKE256 output length: got %d, want 64", len(got))
	}
}

func TestShake256Gas(t *testing.T) {
	precompile := &shake256hash{}
	tests := []struct {
		length int
		gas    uint64
	}{
		{length: 0, gas: 240},
		{length: 1, gas: 288},
		{length: 63, gas: 288},
		{length: 64, gas: 288},
		{length: 65, gas: 336},
	}
	for _, test := range tests {
		if got := precompile.RequiredGas(make([]byte, test.length)); got != test.gas {
			t.Errorf("length %d: got %d gas, want %d", test.length, got, test.gas)
		}
	}
}

func TestMLDSA87Verify(t *testing.T) {
	digest := bytes.Repeat([]byte{0x42}, mldsa87DigestSize)
	context := []byte("QNS-SIGN-v1")
	input := signedMLDSA87Input(t, digest, context)
	precompile := &mldsa87Verify{}

	got, err := precompile.Run(input)
	if err != nil {
		t.Fatal(err)
	}
	want := make([]byte, mldsa87BooleanWordSize)
	want[len(want)-1] = 1
	if !bytes.Equal(got, want) {
		t.Fatalf("unexpected verification result: got %x, want %x", got, want)
	}

	for _, test := range []struct {
		name   string
		offset int
	}{
		{name: "digest", offset: 0},
		{name: "signature", offset: mldsa87DigestSize},
		{name: "public key", offset: mldsa87DigestSize + mldsa87.CRYPTO_BYTES},
		{name: "context", offset: mldsa87FixedInputLength},
	} {
		t.Run(test.name, func(t *testing.T) {
			modified := bytes.Clone(input)
			modified[test.offset] ^= 0x01
			got, err := precompile.Run(modified)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, make([]byte, mldsa87BooleanWordSize)) {
				t.Fatalf("invalid %s returned true", test.name)
			}
		})
	}
}

func TestMLDSA87VerifyMalformedInput(t *testing.T) {
	precompile := &mldsa87Verify{}
	for _, input := range [][]byte{
		nil,
		make([]byte, mldsa87FixedInputLength-1),
		make([]byte, mldsa87FixedInputLength),
		make([]byte, mldsa87FixedInputLength+mldsa87MaxContextSize+1),
	} {
		got, err := precompile.Run(input)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, make([]byte, mldsa87BooleanWordSize)) {
			t.Fatalf("malformed input of length %d returned true", len(input))
		}
	}
}

func TestMLDSA87VerifyContextBounds(t *testing.T) {
	digest := bytes.Repeat([]byte{0x24}, mldsa87DigestSize)
	for _, test := range []struct {
		name    string
		context []byte
	}{
		{name: "empty", context: nil},
		{name: "maximum", context: bytes.Repeat([]byte{0xa5}, mldsa87MaxContextSize)},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := signedMLDSA87Input(t, digest, test.context)
			got, err := (&mldsa87Verify{}).Run(input)
			if err != nil {
				t.Fatal(err)
			}
			if got[len(got)-1] != 1 {
				t.Fatalf("valid signature with %s-length context returned false", test.name)
			}
		})
	}
}

func TestMLDSA87VerifyGas(t *testing.T) {
	precompile := &mldsa87Verify{}
	for _, input := range [][]byte{nil, make([]byte, mldsa87FixedInputLength), make([]byte, mldsa87FixedInputLength+mldsa87MaxContextSize)} {
		if got := precompile.RequiredGas(input); got != params.MLDSA87VerifyGas {
			t.Fatalf("got %d gas, want %d", got, params.MLDSA87VerifyGas)
		}
	}
	if _, _, err := RunPrecompiledContract(precompile, nil, params.MLDSA87VerifyGas-1); err != ErrOutOfGas {
		t.Fatalf("got %v, want %v", err, ErrOutOfGas)
	}
}

func TestQRL2PrecompileRegistry(t *testing.T) {
	for _, slot := range []byte{3, 6} {
		address := common.BytesToAddress([]byte{slot})
		if _, ok := PrecompiledContractsZond[address]; !ok {
			t.Fatalf("precompile slot %d is not active", slot)
		}
	}
}

func BenchmarkPrecompiledShake256(b *testing.B) {
	input := make([]byte, 64)
	precompile := &shake256hash{}
	gas := precompile.RequiredGas(input)
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := RunPrecompiledContract(precompile, input, gas); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPrecompiledMLDSA87Verify(b *testing.B) {
	input := signedMLDSA87Input(b, bytes.Repeat([]byte{0x42}, mldsa87DigestSize), []byte("QNS-SIGN-v1"))
	precompile := &mldsa87Verify{}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, _, err := RunPrecompiledContract(precompile, input, params.MLDSA87VerifyGas); err != nil {
			b.Fatal(err)
		}
	}
}

func signedMLDSA87Input(t testing.TB, digest, context []byte) []byte {
	t.Helper()
	var seed [mldsa87.SEED_BYTES]byte
	for i := range seed {
		seed[i] = byte(i)
	}
	key, err := mldsa87.NewMLDSA87FromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := key.Sign(context, digest)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := key.GetPK()

	input := make([]byte, 0, mldsa87FixedInputLength+len(context))
	input = append(input, digest...)
	input = append(input, signature[:]...)
	input = append(input, publicKey[:]...)
	input = append(input, context...)
	return input
}
