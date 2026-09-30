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

// prepareEnvelope verifies that an unsigned aggregator envelope only calls
// router.execute_strategy(sender, amountIn, payload) from the sender's account,
// moving at most amountIn of the source token out of the sender. It stamps the
// sequence number and expiry so the client can sign it as is, and returns the
// envelope's fee in stroops.
func prepareEnvelope(envelopeB64 string, want envelopeExpectation, seq int64, maxTime int64) (string, uint32, error) {
	env, err := decodeEnvelope(envelopeB64)
	if err != nil {
		return "", 0, err
	}
	tx := &env.V1.Tx
	if err := checkTransaction(tx, want.Sender); err != nil {
		return "", 0, err
	}
	invoke, err := singleInvocation(tx.Operations[0])
	if err != nil {
		return "", 0, err
	}
	if err := checkRouterCall(invoke.HostFunction.InvokeContract, want); err != nil {
		return "", 0, err
	}

	// Every authorization entry must be signed by the transaction source and
	// spend at most the input amount in total.
	if len(invoke.Auth) == 0 {
		return "", 0, errors.New("envelope has no sender authorization")
	}
	spent := new(big.Int)
	for _, entry := range invoke.Auth {
		if entry.Credentials.Type != xdr.SorobanCredentialsTypeSorobanCredentialsSourceAccount {
			return "", 0, errors.New("authorization entry needs a signature the wallet does not produce")
		}
		if err := checkAuthTree(entry.RootInvocation, invoke.HostFunction.InvokeContract, want, spent); err != nil {
			return "", 0, err
		}
	}

	tx.SeqNum = xdr.SequenceNumber(seq)
	tx.Cond = xdr.Preconditions{
		Type:       xdr.PreconditionTypePrecondTime,
		TimeBounds: &xdr.TimeBounds{MinTime: 0, MaxTime: xdr.TimePoint(maxTime)},
	}
	out, err := xdr.MarshalBase64(env)
	if err != nil {
		return "", 0, fmt.Errorf("encoding envelope: %w", err)
	}
	return out, uint32(tx.Fee), nil
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

// checkRouterCall requires router.execute_strategy(sender, amountIn, payload)
// with a payload that matches the quote.
func checkRouterCall(call *xdr.InvokeContractArgs, want envelopeExpectation) error {
	if got, err := call.ContractAddress.String(); err != nil || got != want.Router {
		return errors.New("invocation targets a contract other than the router")
	}
	if string(call.FunctionName) != routerFunction {
		return fmt.Errorf("invocation calls %q, want %q", call.FunctionName, routerFunction)
	}
	if len(call.Args) != 3 {
		return fmt.Errorf("invocation has %d arguments, want 3", len(call.Args))
	}
	if addr, err := scValAddress(call.Args[0]); err != nil || addr != want.Sender {
		return errors.New("invocation sender argument is not the sender")
	}
	if amt, err := scValI128(call.Args[1]); err != nil || amt.Cmp(want.SrcAtoms) != 0 {
		return errors.New("invocation input amount differs from the requested amount")
	}
	if call.Args[2].Type != xdr.ScValTypeScvBytes || call.Args[2].Bytes == nil {
		return errors.New("invocation route payload is not bytes")
	}
	return checkRoutePayload(*call.Args[2].Bytes, want.SrcToken, want.DstToken, want.MinOut)
}

// checkAuthTree allows only the approved router invocation and direct source
// token transfers from the sender into that router. Unknown calls fail closed.
func checkAuthTree(node xdr.SorobanAuthorizedInvocation, call *xdr.InvokeContractArgs, want envelopeExpectation, spent *big.Int) error {
	if !sameAuthorizedCall(node, call) {
		return errors.New("authorization root differs from the approved router invocation")
	}
	for _, sub := range node.SubInvocations {
		fn := sub.Function.ContractFn
		if sub.Function.Type != xdr.SorobanAuthorizedFunctionTypeSorobanAuthorizedFunctionTypeContractFn || fn == nil || string(fn.FunctionName) != "transfer" || len(sub.SubInvocations) != 0 {
			return errors.New("authorization contains a call other than a source token transfer")
		}
		if err := checkSenderTransfer(fn, want, spent); err != nil {
			return err
		}
	}
	return nil
}

func sameAuthorizedCall(node xdr.SorobanAuthorizedInvocation, call *xdr.InvokeContractArgs) bool {
	return node.Function.Type == xdr.SorobanAuthorizedFunctionTypeSorobanAuthorizedFunctionTypeContractFn && reflect.DeepEqual(node.Function.ContractFn, call)
}

// checkSenderTransfer adds an allowed sender-to-router source transfer to spent.
func checkSenderTransfer(fn *xdr.InvokeContractArgs, want envelopeExpectation, spent *big.Int) error {
	if len(fn.Args) != 3 {
		return errors.New("authorization transfer does not have three arguments")
	}
	from, err := scValAddress(fn.Args[0])
	if err != nil {
		return errors.New("authorization transfer sender is unreadable")
	}
	if from != want.Sender {
		return errors.New("authorization transfer is not from the sender")
	}
	if to, err := scValAddress(fn.Args[1]); err != nil || to != want.Router {
		return errors.New("authorization transfer is not into the router")
	}
	if token, err := fn.ContractAddress.String(); err != nil || token != want.SrcToken {
		return errors.New("authorization moves a token other than the source token")
	}
	amt, err := scValI128(fn.Args[2])
	if err != nil || amt.Sign() < 0 {
		return errors.New("authorization transfer amount is unreadable")
	}
	if spent.Add(spent, amt).Cmp(want.SrcAtoms) > 0 {
		return errors.New("authorization moves more than the requested input")
	}
	return nil
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
