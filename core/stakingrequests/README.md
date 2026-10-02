# Experimental recipient-authorized exit queue

Prior art: [EIP-7002](https://eips.ethereum.org/EIPS/eip-7002) (execution layer triggerable withdrawals) for the queue and [EIP-7685](https://eips.ethereum.org/EIPS/eip-7685) (general purpose execution layer requests) for the request commitment. QRL-specific choices: a validator index plus the SSZ root of its ML-DSA-87 public key identifies the validator, only full exits are covered, and the per-block parameters are small fixtures.

This package implements a native QRVM admission/drain queue and a candidate execution-request codec. It has no canonical block-path caller, protocol address or activated request type.

Ordinary calls pay at least the current fee and submit exactly 40 bytes: uint64 validator index in big endian, followed by a 32-byte SSZ public-key root. An empty call with zero value returns the current fee as one 64-byte word. The runtime records the full 64-byte immediate caller. Only the configured system caller can drain, with empty input and zero value. Output records contain source64, index8 in little endian and key-root32.

The fee follows EIP-7002. Storage holds the excess (slot 0), the requests added since the last drain (slot 1), the FIFO head and tail (slots 2 and 3) and three slots per record from slot 4. The queue is unbounded, so paid spam delays later requests but cannot block admission. The fee is `fake_exponential(1, excess, 17)`, and each drain sets `excess = max(0, excess + count - target)`, so demand above the target raises the fee exponentially: it doubles roughly every 12 requests of excess. `Fee` computes the same value in Go. A protocol integration must invoke the drain exactly once per activated block, after ordinary transactions and withdrawal credits, because the excess update assumes one drain per block. The helper does not enforce block scheduling. A drain bound of two, a target of one and the system gas budget are fixtures awaiting qualification; EIP-7002 uses 16 and 2. All payments, including overpayment, remain in the queue account; the prototype exposes no refund or collection. EIP-7002's excess inhibitor, which blocks submissions before fork activation, is not needed while the runtime has no protocol address.

`ValidateCommitment` compares claimed request groups against execution-derived records and their nested SHA-256 commitment. Hashing a supplied envelope alone is insufficient to authenticate its callers. Qrysm must process exactly those committed records using its canonical validator registry.

Integration still needs versioned Engine and beacon-body transport, full/blinded and generated codecs, EL header commitments, persistence/sync, activation rules and code identity assignments. Invalid eligibility is consumed by the consensus handler. Structural or execution-commitment errors invalidate the block.

## Local checks

```sh
GOMAXPROCS=2 go test -p 2 ./core/stakingroots ./core/stakingrequests
GOMAXPROCS=2 go vet -p 2 ./core/stakingroots ./core/stakingrequests
GOMAXPROCS=2 go build -p 2 ./core/stakingroots ./core/stakingrequests
```

Tests exercise actual QRVM caller provenance through a forwarding contract, outer reversion, fees, FIFO rollover, capacity, snapshots, output encoding and independently checked SSZ vectors. Persistent queue recovery, Engine exchange, network reorgs and resource measurements remain pending.
