// Package stakingrequests contains unregistered experimental primitives for
// withdrawal-recipient authorized exits. Constants and encodings are candidates
// for review; no fork, Engine API, or block transition activates this package.
package stakingrequests

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/theQRL/go-qrl/common"
)

const (
	// Type is an experiment identifier, pending protocol assignment.
	Type            byte = 1
	RecordBytes          = 64 + 8 + 32
	SubmissionBytes      = 8 + 32
	PublicKeyBytes       = 2592
	// These bounds and the admission fee are deliberately small test fixtures.
	MaxPending  = 8
	MaxPerBlock = 2
	MinimumFee  = 1
)

// Request identifies one full exit. Source must come from execution provenance.
type Request struct {
	Source         common.Address
	ValidatorIndex uint64
	PublicKeyRoot  common.Hash
}

// Encode returns the fixed-size candidate SSZ record.
func (r Request) Encode() []byte {
	out := make([]byte, RecordBytes)
	copy(out, r.Source[:])
	binary.LittleEndian.PutUint64(out[64:72], r.ValidatorIndex)
	copy(out[72:], r.PublicKeyRoot[:])
	return out
}

// Submission returns the raw input for the queue. The source is omitted.
func (r Request) Submission() []byte {
	out := make([]byte, SubmissionBytes)
	binary.BigEndian.PutUint64(out[:8], r.ValidatorIndex)
	copy(out[8:], r.PublicKeyRoot[:])
	return out
}

func Decode(raw []byte) (Request, error) {
	var request Request
	if len(raw) != RecordBytes {
		return request, fmt.Errorf("exit record: want %d bytes, got %d", RecordBytes, len(raw))
	}
	copy(request.Source[:], raw[:64])
	request.ValidatorIndex = binary.LittleEndian.Uint64(raw[64:72])
	copy(request.PublicKeyRoot[:], raw[72:])
	return request, nil
}

// DecodeBatch validates the exact raw output of a bounded system drain.
func DecodeBatch(raw []byte, maximum int) ([]Request, error) {
	if maximum < 1 || len(raw)%RecordBytes != 0 || len(raw)/RecordBytes > maximum {
		return nil, errors.New("invalid exit batch size or bound")
	}
	requests := make([]Request, len(raw)/RecordBytes)
	for i := range requests {
		requests[i], _ = Decode(raw[i*RecordBytes : (i+1)*RecordBytes])
	}
	return requests, nil
}

func Groups(requests []Request) [][]byte {
	if len(requests) == 0 {
		return nil
	}
	group := []byte{Type}
	for _, request := range requests {
		group = append(group, request.Encode()...)
	}
	return [][]byte{group}
}

// HashGroups uses the EIP-7685 construction after canonical envelope checks.
// Recognition and per-type bounds belong to ValidateGroups.
func HashGroups(groups [][]byte) (common.Hash, error) {
	hasher := sha256.New()
	previous := -1
	for _, group := range groups {
		if len(group) < 2 || int(group[0]) <= previous {
			return common.Hash{}, errors.New("empty, duplicate, or unordered request group")
		}
		previous = int(group[0])
		digest := sha256.Sum256(group)
		hasher.Write(digest[:])
	}
	return common.BytesToHash(hasher.Sum(nil)), nil
}

// ValidateGroups accepts the sole candidate request type and its caller bound.
func ValidateGroups(groups [][]byte, maximum int) ([]Request, error) {
	if maximum < 1 {
		return nil, errors.New("invalid exit batch bound")
	}
	if len(groups) == 0 {
		return nil, nil
	}
	if len(groups) != 1 || len(groups[0]) <= 1 || groups[0][0] != Type {
		return nil, errors.New("unexpected or empty request group")
	}
	return DecodeBatch(groups[0][1:], maximum)
}

// ValidateCommitment binds a claimed envelope to independently executed output.
// Comparing only the envelope with its hash would permit fabricated requests.
func ValidateCommitment(executed []Request, supplied [][]byte, commitment common.Hash, maximum int) error {
	if _, err := ValidateGroups(supplied, maximum); err != nil {
		return err
	}
	expected := Groups(executed)
	if len(expected) != len(supplied) {
		return errors.New("execution request groups differ")
	}
	for i := range expected {
		if !bytes.Equal(expected[i], supplied[i]) {
			return errors.New("execution request bytes differ")
		}
	}
	hash, err := HashGroups(supplied)
	if err != nil {
		return err
	}
	if hash != commitment {
		return errors.New("execution request commitment differs")
	}
	return nil
}

// PublicKeyRoot computes SSZ hash_tree_root(ByteVector[2592]). The vector has
// 81 chunks and pads to 128 leaves, with no list-length mix-in.
func PublicKeyRoot(publicKey []byte) (common.Hash, error) {
	if len(publicKey) != PublicKeyBytes {
		return common.Hash{}, errors.New("invalid validator public-key width")
	}
	var tree [256]common.Hash
	for i := 0; i < PublicKeyBytes/32; i++ {
		copy(tree[128+i][:], publicKey[i*32:(i+1)*32])
	}
	for i := 127; i > 0; i-- {
		var pair [64]byte
		copy(pair[:32], tree[2*i][:])
		copy(pair[32:], tree[2*i+1][:])
		tree[i] = sha256.Sum256(pair[:])
	}
	return tree[1], nil
}
