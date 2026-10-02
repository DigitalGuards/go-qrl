# Experimental beacon-root history

Prior art: [EIP-4788](https://eips.ethereum.org/EIPS/eip-4788) (beacon block root in the EVM). This package ports its timestamp/root ring contract, system address and 8191-entry buffer to native QRVM code with 64-byte words and addresses.

This package implements a native QRVM history runtime and an isolated system-call helper. It has no chain registration, fork activation, assigned system address or Engine API integration.

`Runtime` assembles code using this client's opcode constants. Ordinary calls accept one exact 64-byte big-endian timestamp, limited to a nonzero uint64, and return 32 raw root bytes. The configured 64-byte system caller writes a 32-byte root using the execution block timestamp. Ring reads verify the stored timestamp before returning a root.

`WriteParentRoot` verifies the installed runtime and executes the write with bounded experimental gas. `ExecuteSystemCall` isolates storage originals, access warmth, refunds, logs and preimages, and rolls back persistent changes on failure. This revision has no transient-storage interface. A future interface must be included in that isolation.

The caller must supply authenticated protocol context. Integration still needs fork installation/collision rules, parent-root header and Engine commitments, payload cache separation, CL parent binding, genesis handling and a single pre-block operation in construction, import, historical replay and tracing. The helper cannot authenticate a beacon root by itself.

## Local checks

```sh
GOMAXPROCS=2 go test -p 2 ./core/stakingroots
GOMAXPROCS=2 go vet -p 2 ./core/stakingroots
GOMAXPROCS=2 go build -p 2 ./core/stakingroots
```

Tests execute real QRVM code against StateDB, including commit/reopen, snapshots, native widths, caller authorization, stale history, code identity, failure rollback and system accounting. They establish component behavior. Network operation, fork compatibility and gas qualification remain pending.
