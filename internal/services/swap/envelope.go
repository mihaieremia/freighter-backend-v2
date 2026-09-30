package swap

import (
	"errors"
	"fmt"
	"math/big"
	"reflect"

	"github.com/stellar/go-stellar-sdk/xdr"
)

const (
	routerFunction = "execute_strategy"
	// maxEnvelopeFee caps the fee, in stroops, an envelope may charge the
	// sender: 2 XLM, far above the cost of a swap.
	maxEnvelopeFee = 20_000_000
)

// envelopeExpectation is what the wallet asked for. An aggregator envelope
// that does anything else is rejected.
type envelopeExpectation struct {
	Sender   string
	Router   string
	SrcToken string
	SrcAtoms *big.Int
	// DstToken and MinOut bind the route payload to the quote.
	DstToken string
	MinOut   *big.Int
}

// decodeEnvelope decodes a base64 envelope and requires an unsigned v1 transaction.
func decodeEnvelope(envelopeB64 string) (xdr.TransactionEnvelope, error) {
	var env xdr.TransactionEnvelope
	if err := xdr.SafeUnmarshalBase64(envelopeB64, &env); err != nil {
		return env, fmt.Errorf("decoding envelope: %w", err)
	}
	if env.Type != xdr.EnvelopeTypeEnvelopeTypeTx || env.V1 == nil {
		return env, errors.New("envelope is not a v1 transaction")
	}
	if len(env.V1.Signatures) != 0 {
		return env, errors.New("envelope is already signed")
	}
	return env, nil
}

// checkTransaction requires a memo-less transaction from the sender's plain
// account with a bounded fee, Soroban resource data and exactly one operation.
func checkTransaction(tx *xdr.Transaction, sender string) error {
	if tx.SourceAccount.Type != xdr.CryptoKeyTypeKeyTypeEd25519 {
		return errors.New("envelope source is not a plain account")
	}
	if tx.SourceAccount.Address() != sender {
		return errors.New("envelope source is not the sender")
	}
	if tx.Memo.Type != xdr.MemoTypeMemoNone {
		return errors.New("envelope carries a memo")
	}
	if tx.Fee == 0 || tx.Fee > maxEnvelopeFee {
		return fmt.Errorf("envelope fee %d is outside (0, %d]", tx.Fee, maxEnvelopeFee)
	}
	if tx.Ext.V != 1 || tx.Ext.SorobanData == nil {
		return errors.New("envelope has no soroban resource data")
	}
	if f := tx.Ext.SorobanData.ResourceFee; f < 0 || uint64(f) > uint64(tx.Fee) {
		return errors.New("invalid resource fee")
	}
	if len(tx.Operations) != 1 {
		return fmt.Errorf("envelope has %d operations, want 1", len(tx.Operations))
	}
	return nil
}

// singleInvocation requires the operation to be a contract invocation that
// keeps the transaction's source account.
func singleInvocation(op xdr.Operation) (*xdr.InvokeHostFunctionOp, error) {
	if op.SourceAccount != nil {
		return nil, errors.New("operation overrides the source account")
	}
	if op.Body.Type != xdr.OperationTypeInvokeHostFunction || op.Body.InvokeHostFunctionOp == nil {
		return nil, errors.New("operation is not a host function invocation")
	}
	invoke := op.Body.InvokeHostFunctionOp
	if invoke.HostFunction.Type != xdr.HostFunctionTypeHostFunctionTypeInvokeContract || invoke.HostFunction.InvokeContract == nil {
		return nil, errors.New("host function is not a contract invocation")
	}
	return invoke, nil
}

func sameAuthorizedCall(node xdr.SorobanAuthorizedInvocation, call *xdr.InvokeContractArgs) bool {
	return node.Function.Type == xdr.SorobanAuthorizedFunctionTypeSorobanAuthorizedFunctionTypeContractFn && reflect.DeepEqual(node.Function.ContractFn, call)
}

func scValAddress(v xdr.ScVal) (string, error) {
	a, ok := v.GetAddress()
	if !ok {
		return "", errors.New("value is not an address")
	}
	return a.String()
}

func scValI128(v xdr.ScVal) (*big.Int, error) {
	p, ok := v.GetI128()
	if !ok {
		return nil, errors.New("value is not an i128")
	}
	n := new(big.Int).SetInt64(int64(p.Hi))
	n.Lsh(n, 64)
	return n.Add(n, new(big.Int).SetUint64(uint64(p.Lo))), nil
}

// names must be in canonical symbol order, as required by Soroban structs.
func strictScMap(v xdr.ScVal, names ...string) (map[string]xdr.ScVal, error) {
	if v.Type != xdr.ScValTypeScvMap || v.Map == nil || *v.Map == nil || len(**v.Map) != len(names) {
		return nil, errors.New("unexpected struct field count")
	}
	fields := make(map[string]xdr.ScVal, len(names))
	for i, entry := range **v.Map {
		if entry.Key.Type != xdr.ScValTypeScvSymbol || entry.Key.Sym == nil || string(*entry.Key.Sym) != names[i] {
			return nil, errors.New("unexpected or unordered struct field")
		}
		fields[names[i]] = entry.Val
	}
	return fields, nil
}
