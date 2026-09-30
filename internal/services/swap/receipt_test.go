package swap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stellar/freighter-backend-v2/internal/metrics"
	"github.com/stellar/freighter-backend-v2/internal/services"
	"github.com/stellar/freighter-backend-v2/internal/types"
	"github.com/stellar/freighter-backend-v2/internal/utils"
	stellarNetwork "github.com/stellar/go-stellar-sdk/network"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func receiptFixture(t *testing.T, net string, feeBump bool) (hash, envelope, result, meta string) {
	t.Helper()
	envelope = buildEnvelope(t, envOpts{router: testContract(2), function: routerFunction, sender: testSender, amount: 1000, fee: 5000, signed: true, extraOp: true})
	var env xdr.TransactionEnvelope
	require.NoError(t, xdr.SafeUnmarshalBase64(envelope, &env))
	if feeBump {
		inner := env.V1
		env = xdr.TransactionEnvelope{Type: xdr.EnvelopeTypeEnvelopeTypeTxFeeBump, FeeBump: &xdr.FeeBumpTransactionEnvelope{Tx: xdr.FeeBumpTransaction{FeeSource: inner.Tx.SourceAccount, Fee: 10000, InnerTx: xdr.FeeBumpTransactionInnerTx{Type: xdr.EnvelopeTypeEnvelopeTypeTx, V1: inner}}}}
	}
	envelope, err := xdr.MarshalBase64(env)
	require.NoError(t, err)
	passphrase, err := networkPassphrase(net)
	require.NoError(t, err)
	h, err := stellarNetwork.HashTransactionInEnvelope(env, passphrase)
	require.NoError(t, err)
	hash = hex.EncodeToString(h[:])
	sym := xdr.ScSymbol("transfer")
	event := func(amount int64) xdr.ContractEvent {
		return xdr.ContractEvent{Type: xdr.ContractEventTypeContract, ContractId: mustAddr(utils.ScAddressFromContractString(testContract(4))).ContractId, Body: xdr.ContractEventBody{V: 0, V0: &xdr.ContractEventV0{Topics: []xdr.ScVal{{Type: xdr.ScValTypeScvSymbol, Sym: &sym}, addrVal(mustAddr(utils.ScAddressFromContractString(testContract(2)))), addrVal(mustAddr(utils.ScAddressFromAccountString(testSender)))}, Data: i128Val(amount)}}}
	}
	returnValue := i128Val(1234)
	metaOps := []xdr.OperationMetaV2{{Events: []xdr.ContractEvent{event(777)}}, {Events: []xdr.ContractEvent{event(1234)}}}
	ops := make([]xdr.OperationResult, len(metaOps))
	for i, op := range metaOps {
		preimage, err := (xdr.InvokeHostFunctionSuccessPreImage{ReturnValue: returnValue, Events: op.Events}).MarshalBinary()
		require.NoError(t, err)
		success := xdr.Hash(sha256.Sum256(preimage))
		ops[i] = xdr.OperationResult{Code: xdr.OperationResultCodeOpInner, Tr: &xdr.OperationResultTr{Type: xdr.OperationTypeInvokeHostFunction, InvokeHostFunctionResult: &xdr.InvokeHostFunctionResult{Code: xdr.InvokeHostFunctionResultCodeInvokeHostFunctionSuccess, Success: &success}}}
	}
	res := xdr.TransactionResult{Result: xdr.TransactionResultResult{Code: xdr.TransactionResultCodeTxSuccess, Results: &ops}}
	if feeBump {
		res.Result = xdr.TransactionResultResult{Code: xdr.TransactionResultCodeTxFeeBumpInnerSuccess, InnerResultPair: &xdr.InnerTransactionResultPair{Result: xdr.InnerTransactionResult{Result: xdr.InnerTransactionResultResult{Code: xdr.TransactionResultCodeTxSuccess, Results: &ops}}}}
	}
	result, err = xdr.MarshalBase64(res)
	require.NoError(t, err)
	meta, err = xdr.MarshalBase64(xdr.TransactionMeta{V: 4, V4: &xdr.TransactionMetaV4{Operations: metaOps, SorobanMeta: &xdr.SorobanTransactionMetaV2{ReturnValue: &returnValue}}})
	require.NoError(t, err)
	return
}

func TestSwapReceipt_ConfirmedTransportIdentityAndSelectedOutput(t *testing.T) {
	for _, net := range []string{types.PUBLIC, types.TESTNET} {
		for _, feeBump := range []bool{false, true} {
			t.Run(net+map[bool]string{false: "/signed", true: "/fee-bump"}[feeBump], func(t *testing.T) {
				hash, env, result, meta := receiptFixture(t, net, feeBump)
				var horizonCalls, expertCalls atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/horizon/transactions/" + hash:
						horizonCalls.Add(1)
						// Derive a trusted hash even when Horizon omits its hash field.
						_ = json.NewEncoder(w).Encode(map[string]any{"successful": true, "envelope_xdr": env, "result_xdr": result})
					case "/explorer/tx/" + hash:
						expertCalls.Add(1)
						_ = json.NewEncoder(w).Encode(map[string]string{"meta": meta})
					default:
						t.Errorf("unexpected receipt request %s", r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				defer srv.Close()
				cfg := Config{HorizonPubnetURL: "http://unused.invalid", HorizonTestnetURL: "http://unused.invalid", Networks: map[string]Network{net: {Router: testContract(2)}}}
				pubExpert, testExpert := "http://unused.invalid", "http://unused.invalid"
				if net == types.PUBLIC {
					cfg.HorizonPubnetURL, pubExpert = srv.URL+"/horizon/", srv.URL+"/explorer"
				} else {
					cfg.HorizonTestnetURL, testExpert = srv.URL+"/horizon/", srv.URL+"/explorer"
				}
				svc := NewReceiptService(cfg, services.NewStellarExpertService(pubExpert, testExpert, "", "", nil), nil)
				got, err := svc.GetSwapReceipt(context.Background(), net, hash, testSender, 1)
				require.NoError(t, err)
				assert.Equal(t, &types.SwapReceipt{Network: net, TransactionHash: hash, Viewer: testSender, OperationIndex: 1, Status: "confirmed", TokenOut: testContract(4), ReceivedAtoms: "1234"}, got)
				assert.Equal(t, int32(1), horizonCalls.Load())
				assert.Equal(t, int32(1), expertCalls.Load())
			})
		}
	}
}

func TestSwapReceipt_UnavailableAndUntrustedResponses(t *testing.T) {
	hash, envelope, result, meta := receiptFixture(t, types.PUBLIC, false)
	for _, variant := range []string{"pending", "failed", "missing result", "meta pending", "missing meta", "missing events", "wrong expert event hash", "wrong expert return value", "missing return value", "wrong viewer", "missing operation", "wrong horizon hash", "wrong envelope hash", "malformed result", "malformed meta", "upstream unavailable", "redirect", "deadline"} {
		t.Run(variant, func(t *testing.T) {
			var horizonCalls, expertCalls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/transactions/") {
					horizonCalls.Add(1)
					if variant == "redirect" {
						w.Header().Set("Location", "/redirected")
						w.WriteHeader(http.StatusFound)
						return
					}
					if variant == "deadline" {
						<-r.Context().Done()
						return
					}
					if variant == "pending" || variant == "upstream unavailable" {
						w.WriteHeader(map[bool]int{true: 404, false: 503}[variant == "pending"])
						return
					}
					tx := map[string]any{"hash": hash, "successful": variant != "failed", "envelope_xdr": envelope, "result_xdr": result}
					if variant == "wrong horizon hash" {
						tx["hash"] = strings.Repeat("ab", 32)
					}
					if variant == "wrong envelope hash" {
						tx["envelope_xdr"] = buildEnvelope(t, envOpts{router: testContract(2), function: routerFunction, sender: testSender, amount: 1001, fee: 5000})
					}
					if variant == "missing result" {
						tx["result_xdr"] = ""
					}
					if variant == "malformed result" {
						tx["result_xdr"] = "bad"
					}
					_ = json.NewEncoder(w).Encode(tx)
					return
				}
				expertCalls.Add(1)
				if variant == "meta pending" {
					w.WriteHeader(404)
					return
				}
				m := meta
				if variant == "missing meta" {
					m = ""
				}
				if variant == "malformed meta" {
					m = "bad"
				}
				if variant == "missing events" || variant == "wrong expert event hash" || variant == "wrong expert return value" || variant == "missing return value" {
					var decoded xdr.TransactionMeta
					require.NoError(t, xdr.SafeUnmarshalBase64(m, &decoded))
					switch variant {
					case "missing events":
						decoded.V4.Operations[1].Events = nil
					case "wrong expert event hash":
						decoded.V4.Operations[1].Events[0].Body.V0.Data = i128Val(9999)
					case "wrong expert return value":
						value := i128Val(9999)
						decoded.V4.SorobanMeta.ReturnValue = &value
					case "missing return value":
						decoded.V4.SorobanMeta.ReturnValue = nil
					}
					var err error
					m, err = xdr.MarshalBase64(decoded)
					require.NoError(t, err)
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"meta": m})
			}))
			defer srv.Close()
			svc := NewReceiptService(Config{HorizonPubnetURL: srv.URL, Networks: map[string]Network{types.PUBLIC: {Router: testContract(2)}}}, services.NewStellarExpertService(srv.URL, "", "", "", nil), nil)
			viewer, index := testSender, 1
			if variant == "wrong viewer" {
				viewer = testIssuer
			}
			if variant == "missing operation" {
				index = 2
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			if variant == "deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
			}
			defer cancel()
			got, err := svc.GetSwapReceipt(ctx, types.PUBLIC, hash, viewer, index)
			switch variant {
			case "wrong horizon hash", "wrong envelope hash", "malformed result", "malformed meta", "upstream unavailable", "redirect":
				var up *metrics.UpstreamError
				require.ErrorAs(t, err, &up)
				assert.Nil(t, got)
			case "deadline":
				assert.True(t, errors.Is(err, context.DeadlineExceeded))
			default:
				require.NoError(t, err)
				assert.Equal(t, "unavailable", got.Status)
				assert.Empty(t, got.ReceivedAtoms)
			}
			assert.Equal(t, int32(1), horizonCalls.Load())
			assert.LessOrEqual(t, expertCalls.Load(), int32(1))
			if variant == "wrong horizon hash" || variant == "wrong envelope hash" || variant == "failed" || variant == "pending" || variant == "missing result" || variant == "deadline" || variant == "upstream unavailable" || variant == "redirect" {
				assert.Zero(t, expertCalls.Load())
			}
		})
	}
}
