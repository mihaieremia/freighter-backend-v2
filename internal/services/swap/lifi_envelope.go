package swap

import (
	"errors"
	"fmt"
	"math/big"
	"reflect"

	xoxno "github.com/xoxno/sdk-go"

	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// Official deployment: docs.li.fi/introduction/lifi-architecture/smart-contract-addresses
// Soroswap target verified against its mainnet deployment and LI.FI get_interface.
const (
	lifiRouter       = "CDCNXZHYWRDHNAQEY5EX3WCF7BCWVJBS64ZBVUGRTQBFTHUKG6NAXFTR"
	lifiSoroswap     = "CAYP3UWLJM7ZPTUKL6R6BFGTRWLZ46LRKOXTERI2K6BIJAWGYY62TXTO"
	lifiAquarius     = "CBQDHNBFBZYE4MKPWBSJOPIYLW4SFSXAXUTSXJN76GNKYVYPCKWC6QUK"
	lifiFeeRecipient = "GAZYY5SJZNAVWZWVKJ2E74W2WKZP5BACLVV6FVHC5NZWXPKTQEDOYUR4"
	lifiFeeBPS       = 25
	lifiMaxLifetime  = 600
	lifiMaxRoutes    = 15
	lifiMinAmount    = 10
)

// verifyLifiEnvelope binds both wrapper and Soroswap to the reviewed trade.
// Keep LI.FI's simulated sequence and expiry intact; validate rather than stamp.
func verifyLifiEnvelope(encoded string, want envelopeExpectation, sequence, now int64) (*xdr.Transaction, error) {
	if want.SrcAtoms == nil || want.SrcAtoms.Sign() <= 0 || want.MinOut == nil || want.MinOut.Sign() <= 0 || want.SrcToken == want.DstToken {
		return nil, errors.New("invalid LI.FI trade expectation")
	}
	env, op, err := xoxno.ValidateUnsignedSorobanEnvelope(encoded, want.Sender, maxEnvelopeFee)
	if err != nil {
		return nil, err
	}
	tx := &env.V1.Tx
	if int64(tx.SeqNum) != sequence || tx.Cond.Type != xdr.PreconditionTypePrecondTime || tx.Cond.TimeBounds == nil {
		return nil, errors.New("LI.FI sequence or preconditions differ from the account")
	}
	bounds := tx.Cond.TimeBounds
	if bounds.MinTime != 0 || int64(bounds.MaxTime) <= now+20 || uint64(bounds.MaxTime) > uint64(now+lifiMaxLifetime) {
		return nil, errors.New("LI.FI expiry is outside the signing window")
	}
	call := op.HostFunction.InvokeContract
	args, fee, err := checkLifiCall(call, want, uint64(bounds.MaxTime))
	if err != nil {
		return nil, err
	}
	if err := checkLifiAuthorization(op.Auth, call, args, fee, want); err != nil {
		return nil, err
	}
	return tx, nil
}

// Bind the fee transfer, net-input Soroswap call and supported venue authorizations.
func checkLifiAuthorization(auth []xdr.SorobanAuthorizationEntry, call *xdr.InvokeContractArgs, args []xdr.ScVal, fee *big.Int, want envelopeExpectation) error {
	if len(auth) != 1 || auth[0].Credentials.Type != xdr.SorobanCredentialsTypeSorobanCredentialsSourceAccount {
		return errors.New("LI.FI requires exactly one source-account authorization")
	}
	root := auth[0].RootInvocation
	if !sameAuthorizedCall(root, call) || len(root.SubInvocations) != 2 {
		return errors.New("LI.FI authorization root or fee/swap count differs")
	}
	if err := checkLifiTransfer(root.SubInvocations[0], want, lifiFeeRecipient, fee); err != nil {
		return err
	}
	swap := root.SubInvocations[1]
	netInput := new(big.Int).Sub(want.SrcAtoms, fee)
	fn := swap.Function.ContractFn
	if swap.Function.Type != xdr.SorobanAuthorizedFunctionTypeSorobanAuthorizedFunctionTypeContractFn || fn == nil || addressOf(fn) != lifiSoroswap || string(fn.FunctionName) != "swap_exact_tokens_for_tokens" || len(fn.Args) != len(args) {
		return errors.New("LI.FI does not authorize the expected Soroswap swap")
	}
	for i, arg := range fn.Args {
		if i == 2 {
			if amount, err := scValI128(arg); err != nil || amount.Cmp(netInput) != 0 {
				return errors.New("LI.FI Soroswap net input differs")
			}
		} else if !reflect.DeepEqual(arg, args[i]) {
			return errors.New("LI.FI Soroswap arguments differ")
		}
	}
	routes, _ := scVec(args[4]) // Already validated by checkLifiCall.
	if len(routes) != len(swap.SubInvocations) {
		return errors.New("LI.FI route/auth count differs")
	}
	parts := make([]uint32, len(routes))
	amounts := make([]*big.Int, len(routes))
	// ponytail: support Aquarius authorization only; add other AMM ABIs when verified.
	for i, venue := range swap.SubInvocations {
		part, amount, err := checkLifiAquariusAuthorization(venue, routes[i], want)
		if err != nil {
			return fmt.Errorf("LI.FI Aquarius route %d: %w", i, err)
		}
		parts[i], amounts[i] = part, amount
	}
	return checkLifiDistribution(parts, amounts, netInput)
}

func checkLifiAquariusAuthorization(venue xdr.SorobanAuthorizedInvocation, route xdr.ScVal, want envelopeExpectation) (uint32, *big.Int, error) {
	fn := venue.Function.ContractFn
	if venue.Function.Type != xdr.SorobanAuthorizedFunctionTypeSorobanAuthorizedFunctionTypeContractFn || fn == nil ||
		addressOf(fn) != lifiAquarius || string(fn.FunctionName) != "swap_chained" || len(fn.Args) != 5 ||
		!addressEquals(fn.Args[0], want.Sender) || !addressEquals(fn.Args[2], want.SrcToken) || len(venue.SubInvocations) != 1 {
		return 0, nil, errors.New("unsupported venue authorization")
	}
	parts, err := checkLifiAquariusRoute(route, fn.Args[1], want)
	if err != nil {
		return 0, nil, err
	}
	amount, err := lifiU128(fn.Args[3])
	minimum, minErr := lifiU128(fn.Args[4])
	if err != nil || amount.Cmp(big.NewInt(lifiMinAmount)) < 0 || minErr != nil || minimum.Sign() != 0 {
		return 0, nil, errors.New("invalid venue amount or minimum")
	}
	if err := checkLifiTransfer(venue.SubInvocations[0], want, lifiAquarius, amount); err != nil {
		return 0, nil, err
	}
	return parts, amount, nil
}

// Soroswap floors each weighted allocation and sends the remainder to the last route.
func checkLifiDistribution(parts []uint32, amounts []*big.Int, input *big.Int) error {
	var totalParts uint64
	for _, part := range parts {
		totalParts += uint64(part)
	}
	if totalParts > uint64(^uint32(0)) {
		return errors.New("LI.FI distribution parts overflow u32")
	}
	remaining := new(big.Int).Set(input)
	for i, part := range parts {
		expected := new(big.Int).Set(remaining)
		if i != len(parts)-1 {
			expected.Mul(input, new(big.Int).SetUint64(uint64(part)))
			if expected.BitLen() > 127 {
				return errors.New("LI.FI distribution multiplication overflows i128")
			}
			expected.Quo(expected, new(big.Int).SetUint64(totalParts))
		}
		if amounts[i].Cmp(expected) != 0 {
			return fmt.Errorf("LI.FI route %d amount differs from its distribution", i)
		}
		remaining.Sub(remaining, expected)
	}
	return nil
}

func checkLifiCall(call *xdr.InvokeContractArgs, want envelopeExpectation, deadline uint64) ([]xdr.ScVal, *big.Int, error) {
	if addressOf(call) != lifiRouter || string(call.FunctionName) != "swap" || len(call.Args) != 2 || !addressEquals(call.Args[1], want.Sender) {
		return nil, nil, errors.New("unexpected LI.FI router call")
	}
	fields, err := strictScMap(call.Args[0], "args", "fees", "interface", "min_amount_out", "token_in", "token_out", "tracking_id")
	if err != nil {
		return nil, nil, err
	}
	if _, trackingErr := scValI128(fields["tracking_id"]); trackingErr != nil {
		return nil, nil, errors.New("LI.FI tracking ID is not an i128")
	}
	iface := fields["interface"]
	minimum, err := scValI128(fields["min_amount_out"])
	if iface.Type != xdr.ScValTypeScvSymbol || iface.Sym == nil || string(*iface.Sym) != "soroswap_aggregator" ||
		!addressEquals(fields["token_in"], want.SrcToken) || !addressEquals(fields["token_out"], want.DstToken) ||
		err != nil || minimum.Sign() <= 0 || minimum.Cmp(want.MinOut) < 0 {
		return nil, nil, errors.New("LI.FI payload tokens, interface or minimum differ")
	}
	args, err := scVec(fields["args"])
	if err != nil || len(args) != 7 {
		return nil, nil, errors.New("LI.FI Soroswap arguments missing")
	}
	input, err := scValI128(args[2])
	minOut, minErr := scValI128(args[3])
	if !addressEquals(args[0], want.SrcToken) || !addressEquals(args[1], want.DstToken) ||
		err != nil || input.Cmp(want.SrcAtoms) != 0 || minErr != nil || minOut.Cmp(minimum) != 0 ||
		!addressEquals(args[5], want.Sender) || args[6].Type != xdr.ScValTypeScvU64 || args[6].U64 == nil || uint64(*args[6].U64) != deadline {
		return nil, nil, errors.New("LI.FI Soroswap trade differs from the quote")
	}
	route, err := scVec(args[4])
	if err != nil || len(route) == 0 || len(route) > lifiMaxRoutes {
		return nil, nil, errors.New("LI.FI route missing")
	}
	fees, err := scVec(fields["fees"])
	if err != nil || len(fees) != 1 {
		return nil, nil, errors.New("unexpected LI.FI fee count")
	}
	feeFields, err := strictScMap(fees[0], "fee_bps", "fee_destination")
	if err != nil {
		return nil, nil, err
	}
	bps, err := scValI128(feeFields["fee_bps"])
	if err != nil || bps.Cmp(big.NewInt(lifiFeeBPS)) != 0 || !addressEquals(feeFields["fee_destination"], lifiFeeRecipient) {
		return nil, nil, errors.New("unexpected LI.FI fee or recipient")
	}
	fee := new(big.Int).Quo(new(big.Int).Mul(want.SrcAtoms, big.NewInt(lifiFeeBPS)), big.NewInt(10_000))
	return args, fee, nil
}

func scVec(v xdr.ScVal) ([]xdr.ScVal, error) {
	if v.Type != xdr.ScValTypeScvVec || v.Vec == nil || *v.Vec == nil {
		return nil, errors.New("not a vector")
	}
	return []xdr.ScVal(**v.Vec), nil
}

func addressOf(fn *xdr.InvokeContractArgs) string {
	if fn == nil {
		return ""
	}
	s, _ := fn.ContractAddress.String()
	return s
}

func addressEquals(v xdr.ScVal, s string) bool {
	a, err := scValAddress(v)
	return err == nil && a == s
}

func checkLifiTransfer(node xdr.SorobanAuthorizedInvocation, want envelopeExpectation, recipient string, amount *big.Int) error {
	fn := node.Function.ContractFn
	if node.Function.Type != xdr.SorobanAuthorizedFunctionTypeSorobanAuthorizedFunctionTypeContractFn || fn == nil ||
		addressOf(fn) != want.SrcToken || string(fn.FunctionName) != "transfer" || len(fn.Args) != 3 || len(node.SubInvocations) != 0 ||
		!addressEquals(fn.Args[0], want.Sender) || !addressEquals(fn.Args[1], recipient) {
		return errors.New("unexpected LI.FI token transfer")
	}
	got, err := scValI128(fn.Args[2])
	if err != nil || got.Sign() < 0 || got.Cmp(amount) != 0 {
		return errors.New("LI.FI transfer amount differs")
	}
	return nil
}

// Bind each Aquarius authorization to its exact distribution path and pools.
func checkLifiAquariusRoute(distribution, authorizedHops xdr.ScVal, want envelopeExpectation) (uint32, error) {
	fields, err := strictScMap(distribution, "bytes", "parts", "path", "protocol_id")
	if err != nil {
		return 0, err
	}
	protocol, parts := fields["protocol_id"], fields["parts"]
	if protocol.Type != xdr.ScValTypeScvU32 || protocol.U32 == nil || *protocol.U32 != 2 || parts.Type != xdr.ScValTypeScvU32 || parts.U32 == nil || *parts.U32 == 0 {
		return 0, errors.New("unsupported LI.FI route protocol")
	}
	path, err := scVec(fields["path"])
	if err != nil || len(path) < 2 || !addressEquals(path[0], want.SrcToken) || !addressEquals(path[len(path)-1], want.DstToken) {
		return 0, errors.New("LI.FI path endpoints differ")
	}
	for _, token := range path {
		if _, addressErr := scValAddress(token); addressErr != nil {
			return 0, errors.New("LI.FI path token is not an address")
		}
	}
	pools, err := scVec(fields["bytes"])
	if err != nil || len(pools) != len(path)-1 {
		return 0, errors.New("LI.FI path pools differ")
	}
	hops, err := scVec(authorizedHops)
	if err != nil || len(hops) != len(pools) {
		return 0, errors.New("LI.FI authorized path differs")
	}
	for i, pool := range pools {
		if pool.Type != xdr.ScValTypeScvBytes || pool.Bytes == nil || len(*pool.Bytes) != 32 {
			return 0, errors.New("invalid LI.FI pool ID")
		}
		hop, err := scVec(hops[i])
		if err != nil || len(hop) != 3 {
			return 0, errors.New("invalid LI.FI Aquarius hop")
		}
		pair, err := scVec(hop[0])
		if err != nil || len(pair) != 2 {
			return 0, errors.New("invalid LI.FI Aquarius token pair")
		}
		samePair := (reflect.DeepEqual(pair[0], path[i]) && reflect.DeepEqual(pair[1], path[i+1])) || (reflect.DeepEqual(pair[1], path[i]) && reflect.DeepEqual(pair[0], path[i+1]))
		if !samePair || !reflect.DeepEqual(hop[1], pool) || !reflect.DeepEqual(hop[2], path[i+1]) {
			return 0, errors.New("LI.FI Aquarius hop differs from route")
		}
	}
	return uint32(*parts.U32), nil
}

func lifiU128(v xdr.ScVal) (*big.Int, error) {
	p, ok := v.GetU128()
	if !ok {
		return nil, errors.New("value is not a u128")
	}
	n := new(big.Int).SetUint64(uint64(p.Hi))
	n.Lsh(n, 64)
	return n.Add(n, new(big.Int).SetUint64(uint64(p.Lo))), nil
}

const maxEnvelopeFee = 20_000_000

type envelopeExpectation struct {
	Sender   string
	Router   string
	SrcToken string
	SrcAtoms *big.Int
	// DstToken and MinOut bind the route payload to the quote.
	DstToken string
	MinOut   *big.Int
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

// Historical receipts validate display identity, not current quote expiry or fees.
func readLifiReceipt(envelope, result, meta, viewer string, index int) (*xoxno.SwapReceipt, error) {
	invocation, err := xoxno.ReadConfirmedInvocation(envelope, result, meta, index)
	if err != nil || invocation == nil {
		return nil, err
	}
	call := invocation.Call
	if addressOf(call) != lifiRouter || string(call.FunctionName) != "swap" || len(call.Args) != 2 || !addressEquals(call.Args[1], viewer) {
		return nil, nil
	}
	fields, err := strictScMap(call.Args[0], "args", "fees", "interface", "min_amount_out", "token_in", "token_out", "tracking_id")
	if err != nil {
		return nil, nil
	}
	iface := fields["interface"]
	if iface.Type != xdr.ScValTypeScvSymbol || iface.Sym == nil || string(*iface.Sym) != "soroswap_aggregator" {
		return nil, nil
	}
	in, inErr := scValAddress(fields["token_in"])
	out, outErr := scValAddress(fields["token_out"])
	args, argsErr := scVec(fields["args"])
	if inErr != nil || outErr != nil || !strkey.IsValidContractAddress(in) || !strkey.IsValidContractAddress(out) || in == out || argsErr != nil || len(args) != 7 || !addressEquals(args[0], in) || !addressEquals(args[1], out) || !addressEquals(args[5], viewer) {
		return nil, nil
	}
	input, err := scValI128(args[2])
	if err != nil || input.Sign() <= 0 {
		return nil, nil
	}
	amount, err := invocation.ReceivedTokenAmount(out, viewer, "")
	if err != nil || amount == nil {
		return nil, err
	}
	return &xoxno.SwapReceipt{TokenIn: in, TokenOut: out, AmountIn: input, AmountOut: amount}, nil
}

// atoms parses a positive base-10 integer, or returns nil.
func atoms(s string) *big.Int {
	if v, ok := new(big.Int).SetString(s, 10); ok && v.Sign() > 0 {
		return v
	}
	return nil
}

// invalidQuote wraps errInvalidQuote with the reason.
func invalidQuote(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{errInvalidQuote}, args...)...)
}

// checkSlippage returns the quoted output and minimum output after requiring
// 0 < minimum <= output and a minimum no lower than the requested slippage allows.
func checkSlippage(output, minimum string, slippagePercent float64) (out, minOut *big.Int, err error) {
	if out = atoms(output); out == nil {
		return nil, nil, invalidQuote("quote output %q is not positive", output)
	}
	if minOut = atoms(minimum); minOut == nil || minOut.Cmp(out) > 0 {
		return nil, nil, invalidQuote("quote minimum output %q is not in (0, output]", minimum)
	}
	if minOut.Cmp(minAmountOut(out, slippagePercent)) < 0 {
		return nil, nil, invalidQuote("quote minimum output %q is below the requested slippage", minimum)
	}
	return out, minOut, nil
}
