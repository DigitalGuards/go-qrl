# Experimental recipient-authorized exit queue

Prior art: [EIP-7002](https://eips.ethereum.org/EIPS/eip-7002) (execution layer triggerable withdrawals) for the queue and [EIP-7685](https://eips.ethereum.org/EIPS/eip-7685) (general purpose execution layer requests) for the request commitment. QRL-specific choices: a validator index plus the SSZ root of its ML-DSA-87 public key identifies the validator, only full exits are covered, and the flat fee and small bounds replace EIP-7002's exponential fee market.

This package implements a native QRVM admission/drain queue and a candidate execution-request codec. It has no canonical block-path caller, protocol address or activated request type.

Ordinary calls pay a fee and submit exactly 40 bytes: uint64 validator index in big endian, followed by a 32-byte SSZ public-key root. The runtime records the full 64-byte immediate caller. Only the configured system caller can drain, with empty input and zero value. Output records contain source64, index8 in little endian and key-root32.

The experimental ring holds eight records and drains at most two per invocation. A protocol integration must invoke the drain exactly once per activated block, after ordinary transactions and withdrawal credits. The helper does not enforce block scheduling. Fee one, pending eight, output two and the system gas budget are fixtures awaiting qualification. All payments, including overpayment, remain in the queue account; the prototype exposes no refund, collection or fee adjustment.

`ValidateCommitment` compares claimed request groups against execution-derived records and their nested SHA-256 commitment. Hashing a supplied envelope alone is insufficient to authenticate its callers. Qrysm must process exactly those committed records using its canonical validator registry.

Integration still needs versioned Engine and beacon-body transport, full/blinded and generated codecs, EL header commitments, persistence/sync, activation rules and code identity assignments. Invalid eligibility is consumed by the consensus handler. Structural or execution-commitment errors invalidate the block.

## Local checks

```sh
GOMAXPROCS=2 go test -p 2 ./core/stakingroots ./core/stakingrequests
GOMAXPROCS=2 go vet -p 2 ./core/stakingroots ./core/stakingrequests
GOMAXPROCS=2 go build -p 2 ./core/stakingroots ./core/stakingrequests
```

Tests exercise actual QRVM caller provenance through a forwarding contract, outer reversion, fees, FIFO rollover, capacity, snapshots, output encoding and independently checked SSZ vectors. Persistent queue recovery, Engine exchange, network reorgs and resource measurements remain pending.
