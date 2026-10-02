package catalyst

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"github.com/theQRL/go-qrl/beacon/engine"
	"github.com/theQRL/go-qrl/common"
	"github.com/theQRL/go-qrl/core"
	"github.com/theQRL/go-qrl/core/stakingroots"
	"github.com/theQRL/go-qrl/core/types"
	"github.com/theQRL/go-qrl/miner"
	"github.com/theQRL/go-qrl/node"
	"github.com/theQRL/go-qrl/p2p"
	"github.com/theQRL/go-qrl/params"
	"github.com/theQRL/go-qrl/qrl"
	"github.com/theQRL/go-qrl/qrl/downloader"
	"github.com/theQRL/go-qrl/qrl/qrlconfig"
	"github.com/theQRL/go-qrl/qrl/tracers"
	"github.com/theQRL/go-qrl/rpc"
)

func rootAPI(t *testing.T, activation *uint64) *ConsensusAPI {
	t.Helper()
	config := *params.TestChainConfig
	config.QRLBeaconRootsTime = activation
	genesis := &core.Genesis{Config: &config, Timestamp: 9000, GasLimit: 30_000_000, Alloc: core.GenesisAlloc{testAddr: {Balance: testBalance}}}
	n, err := node.New(&node.Config{P2P: p2p.Config{ListenAddr: "127.0.0.1:0", NoDiscovery: true, MaxPeers: 0}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Close() })
	mcfg := miner.DefaultConfig
	mcfg.PendingFeeRecipient = testAddr
	service, err := qrl.New(n, &qrlconfig.Config{Genesis: genesis, SyncMode: downloader.FullSync, TrieTimeout: time.Minute, TrieDirtyCache: 16, TrieCleanCache: 16, Miner: mcfg})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Start(); err != nil {
		t.Fatal(err)
	}
	service.SetSynced()
	return newConsensusAPIWithoutHeartbeat(service)
}

func TestBeaconRootEngineBuildImportAndReplay(t *testing.T) {
	zero := uint64(0)
	api := rootAPI(t, &zero)
	parent := api.qrl.BlockChain().CurrentBlock()
	root := common.Hash{0: 0x87, 31: 0x32}
	attrs := &engine.PayloadAttributes{Timestamp: parent.Time + 60, Withdrawals: []*types.Withdrawal{}, ParentBeaconBlockRoot: &root}
	choice := engine.ForkchoiceStateV1{HeadBlockHash: parent.Hash()}
	if _, err := api.ForkchoiceUpdatedV2(choice, attrs); err == nil {
		t.Fatal("V2 accepted active payload attributes")
	}
	missing := *attrs
	missing.ParentBeaconBlockRoot = nil
	if _, err := api.ForkchoiceUpdatedWithBeaconRootV1(choice, &missing); err == nil {
		t.Fatal("accepted missing parent root")
	}
	built, err := api.ForkchoiceUpdatedWithBeaconRootV1(choice, attrs)
	if err != nil || built.PayloadID == nil {
		t.Fatalf("build: %v, %+v", err, built)
	}
	if _, err := api.GetPayloadV2(*built.PayloadID); err == nil {
		t.Fatal("V2 returned activated payload")
	}
	envelope, err := api.GetPayloadWithBeaconRootV1(*built.PayloadID)
	if err != nil {
		t.Fatal(err)
	}
	payload := envelope.ExecutionPayload
	if len(payload.Transactions) != 0 || payload.GasUsed != 0 {
		t.Fatal("system write charged user gas or created a transaction")
	}
	if _, err := api.NewPayloadV2(*payload); err == nil {
		t.Fatal("V2 imported activated payload")
	}
	if _, err := api.NewPayloadWithBeaconRootV1(*payload, nil); err == nil {
		t.Fatal("accepted absent import root")
	}
	wrongRoot := common.Hash{5}
	wrong, err := api.NewPayloadWithBeaconRootV1(*payload, &wrongRoot)
	if err != nil || wrong.Status != engine.INVALID {
		t.Fatalf("wrong parent root: %v %+v", err, wrong)
	}
	block, err := engine.ExecutableDataToBlockWithBeaconRoot(*payload, &root)
	if err != nil {
		t.Fatal(err)
	}
	// A forged root with a matching header hash still has the wrong state root.
	forgedHeader := block.Header()
	forgedHeader.ParentBeaconRoot = &wrongRoot
	forged := *payload
	forged.BlockHash = forgedHeader.Hash()
	wrong, err = api.NewPayloadWithBeaconRootV1(forged, &wrongRoot)
	if err != nil || wrong.Status != engine.INVALID {
		t.Fatalf("forged root and matching hash: %v %+v", err, wrong)
	}
	status, err := api.NewPayloadWithBeaconRootV1(*payload, &root)
	if err != nil || status.Status != engine.VALID {
		t.Fatalf("import: %v %+v", err, status)
	}
	choice.HeadBlockHash = block.Hash()
	if _, err := api.ForkchoiceUpdatedV2(choice, nil); err != nil {
		t.Fatal(err)
	}
	// Direct transaction-state reconstruction must run the empty-block write.
	_, _, replay, release, err := api.qrl.APIBackend.StateAtTransaction(context.Background(), block, 0, 128)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	rootSlot := common.BigToHash(new(big.Int).SetUint64(payload.Timestamp%stakingroots.ExperimentalHistoryLength + stakingroots.ExperimentalHistoryLength))
	stored := replay.GetState(stakingroots.ExperimentalAddress(), rootSlot)
	if !bytes.Equal(stored[:32], root[:]) {
		t.Fatal("empty-block replay omitted root")
	}

	// Include a signed reader transaction whose query is this block's timestamp.
	attrs.Timestamp += 60
	root = common.Hash{0: 0x42, 31: 0x98}
	input := make([]byte, 64)
	binary.BigEndian.PutUint64(input[56:], attrs.Timestamp)
	address := stakingroots.ExperimentalAddress()
	tx := types.MustSignNewTx(testWallet, types.LatestSigner(api.qrl.BlockChain().Config()), &types.DynamicFeeTx{Nonce: 0, To: &address, Value: new(big.Int), Gas: 1_000_000, GasFeeCap: big.NewInt(2 * params.InitialBaseFee), GasTipCap: big.NewInt(1), Data: input})
	if errs := api.qrl.TxPool().Add([]*types.Transaction{tx}, true, true); len(errs) != 1 || errs[0] != nil {
		t.Fatalf("reader tx admission: %v", errs)
	}
	if err := api.qrl.TxPool().Sync(); err != nil {
		t.Fatal(err)
	}
	built, err = api.ForkchoiceUpdatedWithBeaconRootV1(choice, attrs)
	if err != nil || built.PayloadID == nil {
		t.Fatalf("reader build: %v", err)
	}
	envelope = api.localBlocks.get(*built.PayloadID, true)
	if len(envelope.ExecutionPayload.Transactions) != 1 {
		t.Fatal("reader transaction missing from built payload")
	}
	status, err = api.NewPayloadWithBeaconRootV1(*envelope.ExecutionPayload, &root)
	if err != nil || status.Status != engine.VALID {
		t.Fatalf("reader import: %v %+v", err, status)
	}
	choice.HeadBlockHash = envelope.ExecutionPayload.BlockHash
	if _, err := api.ForkchoiceUpdatedV2(choice, nil); err != nil {
		t.Fatal(err)
	}
	traceAPI := tracers.NewAPI(api.qrl.APIBackend)
	trace, err := traceAPI.TraceTransaction(context.Background(), tx.Hash(), nil)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(trace)
	if !bytes.Contains(data, []byte(common.Bytes2Hex(root[:]))) {
		t.Fatalf("transaction trace omitted root result: %s", data)
	}
	traces, err := traceAPI.TraceBlockByHash(context.Background(), choice.HeadBlockHash, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = json.Marshal(traces)
	if !bytes.Contains(data, []byte(common.Bytes2Hex(root[:]))) {
		t.Fatalf("block trace omitted root result: %s", data)
	}
}

func TestBeaconRootEngineForkBoundaryAndCache(t *testing.T) {
	activation := uint64(9060)
	api := rootAPI(t, &activation)
	parent := api.qrl.BlockChain().CurrentBlock()
	choice := engine.ForkchoiceStateV1{HeadBlockHash: parent.Hash()}
	root := common.Hash{1}
	attrs := &engine.PayloadAttributes{Timestamp: activation - 1, Withdrawals: []*types.Withdrawal{}, ParentBeaconBlockRoot: &root}
	if _, err := api.ForkchoiceUpdatedWithBeaconRootV1(choice, attrs); err == nil {
		t.Fatal("experimental method accepted inactive timestamp")
	}
	attrs.ParentBeaconBlockRoot = nil
	legacy, err := api.ForkchoiceUpdatedV2(choice, attrs)
	if err != nil || legacy.PayloadID == nil {
		t.Fatalf("legacy build before activation: %v", err)
	}
	if _, err := api.GetPayloadWithBeaconRootV1(*legacy.PayloadID); err == nil {
		t.Fatal("experimental get accepted legacy payload")
	}
	if _, err := api.GetPayloadV2(*legacy.PayloadID); err != nil {
		t.Fatal(err)
	}
	attrs.Timestamp, attrs.ParentBeaconBlockRoot = activation, &root
	first, err := api.ForkchoiceUpdatedWithBeaconRootV1(choice, attrs)
	if err != nil || first.PayloadID == nil {
		t.Fatalf("activation build: %v", err)
	}
	other := common.Hash{2}
	attrs.ParentBeaconBlockRoot = &other
	second, err := api.ForkchoiceUpdatedWithBeaconRootV1(choice, attrs)
	if err != nil || second.PayloadID == nil || *first.PayloadID == *second.PayloadID {
		t.Fatalf("payload cache did not bind beacon root: %v", err)
	}
	for _, item := range []struct {
		id   engine.PayloadID
		root common.Hash
	}{{*first.PayloadID, root}, {*second.PayloadID, other}} {
		envelope, err := api.GetPayloadWithBeaconRootV1(item.id)
		if err != nil {
			t.Fatal(err)
		}
		status, err := api.NewPayloadWithBeaconRootV1(*envelope.ExecutionPayload, &item.root)
		if err != nil || status.Status != engine.VALID {
			t.Fatalf("activation import: %v %+v", err, status)
		}
	}
}

func TestBeaconRootPendingViewRemainsAvailable(t *testing.T) {
	zero := uint64(0)
	api := rootAPI(t, &zero)
	block, _, statedb := api.qrl.APIBackend.Pending()
	if block == nil || statedb == nil || block.BeaconRoot() != nil {
		t.Fatal("pending simulation unavailable or claiming an authenticated root")
	}
	state, header, err := api.qrl.APIBackend.StateAndHeaderByNumber(context.Background(), rpc.PendingBlockNumber)
	if err != nil || state == nil || header == nil || header.ParentBeaconRoot != nil {
		t.Fatalf("pending RPC state: %v", err)
	}
	if _, err := api.qrl.Miner().BuildPayload(&miner.BuildPayloadArgs{Parent: api.qrl.BlockChain().CurrentBlock().Hash(), Timestamp: header.Time, Withdrawals: types.Withdrawals{}}); err == nil {
		t.Fatal("canonical builder accepted the pending simulation's absent root")
	}
	// The simulation's missing root prevents it from being imported as an
	// activated payload even when a caller supplies a root afterward.
	payload := engine.BlockToExecutableData(block, new(big.Int)).ExecutionPayload
	status, err := api.NewPayloadWithBeaconRootV1(*payload, new(common.Hash))
	if err != nil || status.Status != engine.INVALID {
		t.Fatalf("pending simulation accepted as canonical: %v %+v", err, status)
	}
}
