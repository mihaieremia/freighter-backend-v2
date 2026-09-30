package swap

import (
	"encoding/json"
	"math/big"
	"os"
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"
)

func TestXoxnoCapturedEnvelopes(t *testing.T) {
	for _, fixture := range []struct{ file, input, output, minimum string }{
		{"xoxno.json", "100000000", "CCW67TSZV3SSS2HXMBQ5JFGCKJNXKZM7UQUWUZPUTHXSTZLEO7SJMI75", "22719551"},
		{"xoxno-soroban.json", "1000000000", "CBI7UCH5KGSVQRO5H4SUCZUTZABCITZLRHQQZTWL2TK4RZ72TAR6IHRV", "22225325112351813029"},
	} {
		t.Run(fixture.file, func(t *testing.T) {
			var data struct {
				EnvelopeXDR string `json:"envelopeXdr"`
			}
			readEnvelopeJSON(t, fixture.file, &data)
			for _, mutation := range []string{"valid", "negative resource fee", "excess resource fee", "duplicate field", "extra field", "unordered fields", "shared spend", "excess shared spend"} {
				t.Run(mutation, func(t *testing.T) {
					env, err := decodeEnvelope(data.EnvelopeXDR)
					require.NoError(t, err)
					tx := &env.V1.Tx
					op := tx.Operations[0].Body.InvokeHostFunctionOp
					call := op.HostFunction.InvokeContract
					want := envelopeExpectation{
						Sender: "GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN", Router: addressOf(call),
						SrcToken: "CAS3J7GYLGXMF6TDJBBYYSE3HQ6BBSMLNUQ34T6TZMYMW2EVH34XOWMA", DstToken: fixture.output,
						SrcAtoms: atoms(fixture.input), MinOut: atoms(fixture.minimum),
					}
					switch mutation {
					case "negative resource fee":
						tx.Ext.SorobanData.ResourceFee = -1
					case "excess resource fee":
						tx.Ext.SorobanData.ResourceFee = xdr.Int64(tx.Fee) + 1
					case "duplicate field", "extra field", "unordered fields":
						var payload xdr.ScVal
						require.NoError(t, xdr.SafeUnmarshal(*call.Args[2].Bytes, &payload))
						mutateStruct(&payload, mutation)
						encoded, err := payload.MarshalBinary()
						require.NoError(t, err)
						b := xdr.ScBytes(encoded)
						call.Args[2].Bytes = &b
					case "shared spend", "excess shared spend":
						other, err := decodeEnvelope(data.EnvelopeXDR)
						require.NoError(t, err)
						op.Auth = append(op.Auth, other.V1.Tx.Operations[0].Body.InvokeHostFunctionOp.Auth[0])
						for i, fraction := range []int64{60, 40} {
							amount := new(big.Int).Quo(new(big.Int).Mul(want.SrcAtoms, big.NewInt(fraction)), big.NewInt(100))
							if mutation == "excess shared spend" && i == 1 {
								amount.Add(amount, big.NewInt(1))
							}
							op.Auth[i].RootInvocation.SubInvocations[0].Function.ContractFn.Args[2] = bigI128(amount)
						}
					}
					op.Auth[0].RootInvocation.Function.ContractFn = call
					encoded, err := xdr.MarshalBase64(env)
					require.NoError(t, err)
					out, _, err := prepareEnvelope(encoded, want, 777, 1700000000)
					if mutation != "valid" && mutation != "shared spend" {
						require.Error(t, err)
						return
					}
					require.NoError(t, err)
					stamped, err := decodeEnvelope(out)
					require.NoError(t, err)
					tx.SeqNum, tx.Cond = stamped.V1.Tx.SeqNum, stamped.V1.Tx.Cond
					require.Equal(t, env, stamped, "only sequence and expiry may change")
				})
			}
		})
	}
}

func TestLifiABIValidation(t *testing.T) {
	q, want, sequence, now := lifiFixture(t)
	for _, mutation := range []string{"valid multihop", "non-address intermediate", "tracking ID type", "venue minimum", "duplicate field", "extra field", "unordered fields"} {
		t.Run(mutation, func(t *testing.T) {
			env, err := decodeEnvelope(q.TransactionRequest.Data)
			require.NoError(t, err)
			op := env.V1.Tx.Operations[0].Body.InvokeHostFunctionOp
			call := op.HostFunction.InvokeContract
			payload, err := strictScMap(call.Args[0], "args", "fees", "interface", "min_amount_out", "token_in", "token_out", "tracking_id")
			require.NoError(t, err)
			args, _ := scVec(payload["args"])
			swap := &op.Auth[0].RootInvocation.SubInvocations[1]
			venue := &swap.SubInvocations[0]
			switch mutation {
			case "tracking ID type":
				setStructField(&call.Args[0], "tracking_id", xdr.ScVal{Type: xdr.ScValTypeScvVoid})
			case "venue minimum":
				venue.Function.ContractFn.Args[4] = bigU128(big.NewInt(1))
			case "duplicate field", "extra field", "unordered fields":
				mutateStruct(&call.Args[0], mutation)
			case "valid multihop", "non-address intermediate":
				routes, _ := scVec(args[4])
				fields, err := strictScMap(routes[0], "bytes", "parts", "path", "protocol_id")
				require.NoError(t, err)
				path, _ := scVec(fields["path"])
				pools, _ := scVec(fields["bytes"])
				middle := addrVal(mustAddrContract("CBI7UCH5KGSVQRO5H4SUCZUTZABCITZLRHQQZTWL2TK4RZ72TAR6IHRV"))
				if mutation == "non-address intermediate" {
					middle = i128Val(0)
				}
				setStructField(&routes[0], "path", scVector(path[0], middle, path[1]))
				setStructField(&routes[0], "bytes", scVector(pools[0], pools[0]))
				venue.Function.ContractFn.Args[1] = scVector(
					scVector(scVector(path[0], middle), pools[0], middle),
					scVector(scVector(middle, path[1]), pools[0], path[1]),
				)
				swap.Function.ContractFn.Args[4] = args[4]
			}
			op.Auth[0].RootInvocation.Function.ContractFn = call
			encoded, err := xdr.MarshalBase64(env)
			require.NoError(t, err)
			_, err = verifyLifiEnvelope(encoded, want, sequence, now)
			if mutation == "valid multihop" {
				require.NoError(t, err, "synthetic ABI coverage, not proof of executable pools")
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestLifiDistributionCorpus(t *testing.T) {
	var cases []struct {
		Name, Input string
		Parts       []uint32
		Amounts     []string
		Accept      bool
	}
	readEnvelopeJSON(t, "lifi-distribution.json", &cases)
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			q, want, sequence, now := lifiFixture(t)
			env, err := decodeEnvelope(q.TransactionRequest.Data)
			require.NoError(t, err)
			op := env.V1.Tx.Operations[0].Body.InvokeHostFunctionOp
			call := op.HostFunction.InvokeContract
			payload, err := strictScMap(call.Args[0], "args", "fees", "interface", "min_amount_out", "token_in", "token_out", "tracking_id")
			require.NoError(t, err)
			args, _ := scVec(payload["args"])
			baseRoutes, _ := scVec(args[4])
			swap := &op.Auth[0].RootInvocation.SubInvocations[1]
			baseVenue := swap.SubInvocations[0]
			want.SrcAtoms = atoms(tc.Input)
			fee := new(big.Int).Quo(new(big.Int).Mul(want.SrcAtoms, big.NewInt(lifiFeeBPS)), big.NewInt(10000))
			args[2] = bigI128(want.SrcAtoms)
			op.Auth[0].RootInvocation.SubInvocations[0].Function.ContractFn.Args[2] = bigI128(fee)
			routes := make([]xdr.ScVal, len(tc.Parts))
			swap.SubInvocations = make([]xdr.SorobanAuthorizedInvocation, len(tc.Parts))
			for i, part := range tc.Parts {
				b, err := baseRoutes[0].MarshalBinary()
				require.NoError(t, err)
				require.NoError(t, xdr.SafeUnmarshal(b, &routes[i]))
				p := xdr.Uint32(part)
				setStructField(&routes[i], "parts", xdr.ScVal{Type: xdr.ScValTypeScvU32, U32: &p})
				b, err = baseVenue.MarshalBinary()
				require.NoError(t, err)
				require.NoError(t, xdr.SafeUnmarshal(b, &swap.SubInvocations[i]))
				amount, ok := new(big.Int).SetString(tc.Amounts[i], 10)
				require.True(t, ok)
				swap.SubInvocations[i].Function.ContractFn.Args[3] = bigU128(amount)
				swap.SubInvocations[i].SubInvocations[0].Function.ContractFn.Args[2] = bigI128(amount)
			}
			args[4] = scVector(routes...)
			swap.Function.ContractFn.Args = append([]xdr.ScVal(nil), args...)
			swap.Function.ContractFn.Args[2] = bigI128(new(big.Int).Sub(want.SrcAtoms, fee))
			op.Auth[0].RootInvocation.Function.ContractFn = call
			encoded, err := xdr.MarshalBase64(env)
			require.NoError(t, err)
			_, err = verifyLifiEnvelope(encoded, want, sequence, now)
			if tc.Accept {
				require.NoError(t, err, "synthetic allocation coverage")
			} else {
				require.Error(t, err)
			}
		})
	}
}

func readEnvelopeJSON(t *testing.T, file string, out any) {
	t.Helper()
	data, err := os.ReadFile("testdata/" + file)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, out))
}

func mutateStruct(v *xdr.ScVal, mutation string) {
	entries := **v.Map
	switch mutation {
	case "duplicate field":
		entries = append(entries, entries[0])
	case "extra field":
		symbol := xdr.ScSymbol("zzz")
		entries = append(entries, xdr.ScMapEntry{Key: xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &symbol}, Val: i128Val(0)})
	case "unordered fields":
		entries[0], entries[1] = entries[1], entries[0]
	}
	*v.Map = &entries
}

func setStructField(v *xdr.ScVal, name string, value xdr.ScVal) {
	for i, entry := range **v.Map {
		if string(*entry.Key.Sym) == name {
			(**v.Map)[i].Val = value
			return
		}
	}
	panic("missing fixture field " + name)
}

func scVector(values ...xdr.ScVal) xdr.ScVal {
	v := xdr.ScVec(values)
	p := &v
	return xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &p}
}

func bigI128(n *big.Int) xdr.ScVal {
	hi := new(big.Int).Rsh(new(big.Int).Set(n), 64).Int64()
	lo := new(big.Int).And(n, new(big.Int).SetUint64(^uint64(0))).Uint64()
	return xdr.ScVal{Type: xdr.ScValTypeScvI128, I128: &xdr.Int128Parts{Hi: xdr.Int64(hi), Lo: xdr.Uint64(lo)}}
}

func bigU128(n *big.Int) xdr.ScVal {
	hi := new(big.Int).Rsh(new(big.Int).Set(n), 64).Uint64()
	return xdr.ScVal{Type: xdr.ScValTypeScvU128, U128: &xdr.UInt128Parts{Hi: xdr.Uint64(hi), Lo: xdr.Uint64(n.Uint64())}}
}
