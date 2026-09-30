package swap

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stellar/freighter-backend-v2/internal/types"
	"github.com/stellar/freighter-backend-v2/internal/utils"
)

func decodeEnvelope(encoded string) (xdr.TransactionEnvelope, error) {
	var env xdr.TransactionEnvelope
	err := xdr.SafeUnmarshalBase64(encoded, &env)
	return env, err
}

// Captured unsigned LI.FI mainnet quote; public sender, no signature or secret.
func lifiFixture(t *testing.T) (lifiQuoteResponse, envelopeExpectation, int64, int64) {
	t.Helper()
	data, err := os.ReadFile("testdata/lifi.json")
	require.NoError(t, err)
	var q lifiQuoteResponse
	require.NoError(t, json.Unmarshal(data, &q))
	env, err := decodeEnvelope(q.TransactionRequest.Data)
	require.NoError(t, err)
	return q, envelopeExpectation{
			Sender: q.Action.FromAddress, Router: lifiRouter,
			SrcToken: q.Action.FromToken.Address, SrcAtoms: atoms(q.Action.FromAmount),
			DstToken: q.Action.ToToken.Address, MinOut: atoms(q.Estimate.ToAmountMin),
		},
		int64(env.V1.Tx.SeqNum), int64(env.V1.Tx.Cond.TimeBounds.MaxTime) - 180
}

func TestLifiEnvelope(t *testing.T) {
	q, want, sequence, now := lifiFixture(t)
	fee, err := verifyLifiEnvelope(q.TransactionRequest.Data, want, sequence, now)
	require.NoError(t, err)
	assert.Positive(t, fee.Fee)
	for name, mutate := range map[string]func(*xdr.Transaction){
		"wrong source":   func(tx *xdr.Transaction) { tx.SourceAccount.Ed25519[0] ^= 1 },
		"stale sequence": func(tx *xdr.Transaction) { tx.SeqNum++ },
		"no expiry":      func(tx *xdr.Transaction) { tx.Cond.TimeBounds.MaxTime = 0 },
		"expired":        func(tx *xdr.Transaction) { tx.Cond.TimeBounds.MaxTime = xdr.TimePoint(now) },
		"excess fee":     func(tx *xdr.Transaction) { tx.Fee = maxEnvelopeFee + 1 },
		"root mismatch": func(tx *xdr.Transaction) {
			tx.Operations[0].Body.InvokeHostFunctionOp.Auth[0].RootInvocation.Function.ContractFn.FunctionName = "other"
		},
		"extra auth": func(tx *xdr.Transaction) {
			op := tx.Operations[0].Body.InvokeHostFunctionOp
			op.Auth = append(op.Auth, op.Auth[0])
		},
		"fee theft": func(tx *xdr.Transaction) {
			fn := tx.Operations[0].Body.InvokeHostFunctionOp.Auth[0].RootInvocation.SubInvocations[0].Function.ContractFn
			fn.Args[2] = i128Val(999999999)
		},
		"unknown venue": func(tx *xdr.Transaction) {
			tx.Operations[0].Body.InvokeHostFunctionOp.Auth[0].RootInvocation.SubInvocations[1].SubInvocations[0].Function.ContractFn.FunctionName = "approve"
		},
		"other token spend": func(tx *xdr.Transaction) {
			node := &tx.Operations[0].Body.InvokeHostFunctionOp.Auth[0].RootInvocation.SubInvocations[1].SubInvocations[0].SubInvocations[0]
			node.Function.ContractFn.ContractAddress = mustAddrContract(testContract(1))
		},
	} {
		t.Run(name, func(t *testing.T) {
			env, caseErr := decodeEnvelope(q.TransactionRequest.Data)
			require.NoError(t, caseErr)
			mutate(&env.V1.Tx)
			encoded, caseErr := xdr.MarshalBase64(env)
			require.NoError(t, caseErr)
			_, caseErr = verifyLifiEnvelope(encoded, want, sequence, now)
			require.Error(t, caseErr)
		})
	}
	stronger := want
	stronger.MinOut = new(big.Int).Add(want.MinOut, big.NewInt(1))
	_, err = verifyLifiEnvelope(q.TransactionRequest.Data, stronger, sequence, now)
	require.Error(t, err)
}

func mustAddrContract(s string) xdr.ScAddress {
	a, err := utils.ScAddressFromContractString(s)
	if err != nil {
		panic(err)
	}
	return *a
}

func TestLifiQuoteNormalizesAndCompetes(t *testing.T) {
	defaults := NewQuoteService(Config{LifiEnabled: true}, nil).(*quoteService)
	assert.Equal(t, "https://li.quest/v1", defaults.sources[1].(*lifiSource).baseURL)
	q, want, sequence, now := lifiFixture(t)
	data, err := json.Marshal(q)
	require.NoError(t, err)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/quote", r.URL.Path)
		assert.Equal(t, "test-key", r.Header.Get("x-lifi-api-key"))
		assert.Equal(t, want.SrcAtoms.String(), r.URL.Query().Get("fromAmount"))
		assert.Equal(t, want.Sender, r.URL.Query().Get("toAddress"))
		assert.Equal(t, "soroswap", r.URL.Query().Get("allowExchanges"))
		assert.Equal(t, "none", r.URL.Query().Get("allowBridges"))
		assert.Equal(t, "CHEAPEST", r.URL.Query().Get("order"))
		_, _ = w.Write(data)
	}))
	defer upstream.Close()
	horizon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"sequence": big.NewInt(sequence - 1).String()})
	}))
	defer horizon.Close()
	configured := NewQuoteService(Config{
		LifiEnabled: true, LifiAPIURL: upstream.URL + "/v1/", LifiAPIKey: "test-key", HorizonPubnetURL: horizon.URL,
	}, nil).(*quoteService)
	s := configured.sources[1].(*lifiSource)
	s.now = func() time.Time { return time.Unix(now, 0) }
	req := types.SwapQuoteRequest{
		Network: types.PUBLIC, SourceAsset: want.SrcToken, DestAsset: want.DstToken,
		SourceAmount: "100", SourceDecimals: 7, DestDecimals: 7, Sender: want.Sender, SlippagePercent: 0.5, TimeoutSeconds: 180,
	}
	other := &fakeSwapSource{name: types.SwapSourceXoxno, cand: newCandidate(types.SwapSourceXoxno, 210_000000)}
	service := newQuoteService([]source{other, s}, time.Second, nil)
	got, err := service.GetBestQuote(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, types.SwapSourceLifi, got.Source)
	assert.Equal(t, "22.0938077", got.DestinationAmount)
	assert.Equal(t, "21.9833386", got.DestinationAmountMin)
	assert.Equal(t, q.TransactionRequest.Data, got.Transaction.EnvelopeXDR, "preserve simulated envelope")
	assert.True(t, got.Alternatives[1].Selected)
	other.cand = newCandidate(types.SwapSourceXoxno, 230_000000)
	got, err = service.GetBestQuote(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, types.SwapSourceXoxno, got.Source)
	assert.False(t, got.Alternatives[1].Selected)
	for name, mutate := range map[string]func(){
		"different recipient": func() { q.Action.ToAddress = testSender },
		"different chain":     func() { q.Action.ToChainID = 1 },
		"wrong decimals":      func() { n := 18; q.Action.ToToken.Decimals = &n },
		"slippage widened":    func() { q.Estimate.ToAmountMin = "1" },
		"approval":            func() { q.Estimate.SkipApproval = false },
	} {
		t.Run(name, func(t *testing.T) {
			q, _, _, _ = lifiFixture(t)
			mutate()
			data, _ = json.Marshal(q)
			_, quoteErr := s.Quote(context.Background(), req)
			require.ErrorIs(t, quoteErr, errInvalidQuote)
		})
	}
	req.Network = types.TESTNET
	_, err = s.Quote(context.Background(), req)
	require.ErrorIs(t, err, errUnsupported)
}

func TestLifiEnvelopeReverseDirection(t *testing.T) {
	data, err := os.ReadFile("testdata/lifi-reverse.json")
	require.NoError(t, err)
	var q lifiQuoteResponse
	require.NoError(t, json.Unmarshal(data, &q))
	env, err := decodeEnvelope(q.TransactionRequest.Data)
	require.NoError(t, err)
	_, err = verifyLifiEnvelope(q.TransactionRequest.Data, envelopeExpectation{Sender: q.Action.FromAddress, SrcToken: q.Action.FromToken.Address, DstToken: q.Action.ToToken.Address, SrcAtoms: atoms(q.Action.FromAmount), MinOut: atoms(q.Estimate.ToAmountMin)}, int64(env.V1.Tx.SeqNum), int64(env.V1.Tx.Cond.TimeBounds.MaxTime)-180)
	require.NoError(t, err)
}

func TestLifiRejectsMatchingMaliciousPayloadAndAuthorization(t *testing.T) {
	q, want, sequence, now := lifiFixture(t)
	for _, field := range []string{"token_in", "token_out", "min_amount_out", "interface", "fee_bps", "fee_destination", "amount", "recipient", "deadline", "duplicate"} {
		t.Run(field, func(t *testing.T) {
			env, err := decodeEnvelope(q.TransactionRequest.Data)
			require.NoError(t, err)
			op := env.V1.Tx.Operations[0].Body.InvokeHostFunctionOp
			call := op.HostFunction.InvokeContract
			m := **call.Args[0].Map
			for i, e := range m {
				name := string(*e.Key.Sym)
				if name == field {
					switch field {
					case "token_in", "token_out":
						m[i].Val = addrVal(mustAddrContract(testContract(1)))
					case "min_amount_out":
						m[i].Val = i128Val(1)
					case "interface":
						v := xdr.ScSymbol("bridge")
						m[i].Val = xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &v}
					}
				}
				if name == "fees" && (field == "fee_bps" || field == "fee_destination") {
					fees := **e.Val.Vec
					fm := **fees[0].Map
					for j, f := range fm {
						if string(*f.Key.Sym) == field {
							if field == "fee_bps" {
								fm[j].Val = i128Val(50)
							} else {
								fm[j].Val = addrVal(mustAddr(utils.ScAddressFromAccountString(testSender)))
							}
						}
					}
				}
				if name == "args" {
					a := **e.Val.Vec
					if field == "amount" {
						a[2] = i128Val(1)
					}
					if field == "recipient" {
						a[5] = addrVal(mustAddr(utils.ScAddressFromAccountString(testSender)))
					}
					if field == "deadline" {
						v := xdr.Uint64(1)
						a[6] = xdr.ScVal{Type: xdr.ScValTypeScvU64, U64: &v}
					}
				}
			}
			if field == "duplicate" {
				m[1].Key = m[0].Key
			}
			op.Auth[0].RootInvocation.Function.ContractFn = call // attacker changes both layers consistently
			encoded, err := xdr.MarshalBase64(env)
			require.NoError(t, err)
			_, err = verifyLifiEnvelope(encoded, want, sequence, now)
			require.Error(t, err)
		})
	}
}
