package swap

import (
	"errors"
	"math/big"
	"reflect"

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
)

// verifyLifiEnvelope binds both wrapper and Soroswap to the reviewed trade.
// Keep LI.FI's simulated sequence and expiry intact; validate rather than stamp.
func verifyLifiEnvelope(encoded string, want envelopeExpectation, sequence, now int64) (uint32, error) {
	if want.SrcAtoms == nil || want.SrcAtoms.Sign() <= 0 || want.MinOut == nil || want.MinOut.Sign() <= 0 || want.SrcToken == want.DstToken {
		return 0, errors.New("invalid LI.FI trade expectation")
	}
	env, err := decodeEnvelope(encoded)
	if err != nil {
		return 0, err
	}
	tx := &env.V1.Tx
	if err := checkTransaction(tx, want.Sender); err != nil {
		return 0, err
	}
	if int64(tx.SeqNum) != sequence || tx.Cond.Type != xdr.PreconditionTypePrecondTime || tx.Cond.TimeBounds == nil {
		return 0, errors.New("LI.FI sequence or preconditions differ from the account")
	}
	bounds := tx.Cond.TimeBounds
	if bounds.MinTime != 0 || int64(bounds.MaxTime) <= now+20 || uint64(bounds.MaxTime) > uint64(now+lifiMaxLifetime) {
		return 0, errors.New("LI.FI expiry is outside the signing window")
	}
	if f := tx.Ext.SorobanData.ResourceFee; f < 0 || uint64(f) > uint64(tx.Fee) {
		return 0, errors.New("invalid resource fee")
	}
	op, err := singleInvocation(tx.Operations[0])
	if err != nil {
		return 0, err
	}
	call := op.HostFunction.InvokeContract
	args, fee, err := checkLifiCall(call, want, uint64(bounds.MaxTime))
	if err != nil {
		return 0, err
	}
	if len(op.Auth) != 1 || op.Auth[0].Credentials.Type != xdr.SorobanCredentialsTypeSorobanCredentialsSourceAccount {
		return 0, errors.New("LI.FI requires exactly one source-account authorization")
	}
	root := op.Auth[0].RootInvocation
	if !sameAuthorizedCall(root, call) || len(root.SubInvocations) != 2 {
		return 0, errors.New("LI.FI authorization root or fee/swap count differs")
	}
	if err := checkLifiTransfer(root.SubInvocations[0], want, lifiFeeRecipient, fee); err != nil {
		return 0, err
	}
	swap := root.SubInvocations[1]
	netInput := new(big.Int).Sub(want.SrcAtoms, fee)
	netArgs := append([]xdr.ScVal(nil), args...)
	n := new(big.Int).Set(netInput)
	mask := new(big.Int).SetUint64(^uint64(0))
	netArgs[2] = xdr.ScVal{Type: xdr.ScValTypeScvI128, I128: &xdr.Int128Parts{Hi: xdr.Int64(new(big.Int).Rsh(n, 64).Int64()), Lo: xdr.Uint64(new(big.Int).And(netInput, mask).Uint64())}}
	fn := swap.Function.ContractFn
	if swap.Function.Type != xdr.SorobanAuthorizedFunctionTypeSorobanAuthorizedFunctionTypeContractFn || fn == nil || addressOf(fn) != lifiSoroswap || string(fn.FunctionName) != "swap_exact_tokens_for_tokens" || !reflect.DeepEqual(fn.Args, netArgs) || len(swap.SubInvocations) == 0 {
		return 0, errors.New("LI.FI does not authorize the expected Soroswap swap")
	}
	routes, _ := scVec(args[4])
	if len(routes) != len(swap.SubInvocations) {
		return 0, errors.New("LI.FI route/auth count differs")
	}
	spent := new(big.Int)
	// ponytail: support Aquarius authorization only; add other AMM ABIs when verified.
	for i, venue := range swap.SubInvocations {
		fn := venue.Function.ContractFn
		if venue.Function.Type != xdr.SorobanAuthorizedFunctionTypeSorobanAuthorizedFunctionTypeContractFn || fn == nil ||
			addressOf(fn) != lifiAquarius || string(fn.FunctionName) != "swap_chained" || len(fn.Args) != 5 ||
			!addressEquals(fn.Args[0], want.Sender) || !addressEquals(fn.Args[2], want.SrcToken) || len(venue.SubInvocations) != 1 {
			return 0, errors.New("unsupported LI.FI venue authorization")
		}
		if err := checkLifiAquariusRoute(routes[i], fn.Args[1], want); err != nil {
			return 0, err
		}
		amount, err := lifiU128(fn.Args[3])
		minimum, minErr := lifiU128(fn.Args[4])
		if err != nil || amount.Sign() <= 0 || minErr != nil || minimum.Sign() < 0 {
			return 0, errors.New("invalid venue amount")
		}
		if err := checkLifiTransfer(venue.SubInvocations[0], want, lifiAquarius, amount); err != nil {
			return 0, err
		}
		if spent.Add(spent, amount).Cmp(netInput) > 0 {
			return 0, errors.New("LI.FI spends more than input")
		}
	}
	if spent.Cmp(netInput) != 0 {
		return 0, errors.New("LI.FI spend does not match net input")
	}
	return uint32(tx.Fee), nil
}

func checkLifiCall(call *xdr.InvokeContractArgs, want envelopeExpectation, deadline uint64) ([]xdr.ScVal, *big.Int, error) {
	if addressOf(call) != lifiRouter || string(call.FunctionName) != "swap" || len(call.Args) != 2 || !addressEquals(call.Args[1], want.Sender) {
		return nil, nil, errors.New("unexpected LI.FI router call")
	}
	fields, err := strictScMap(call.Args[0], "args", "fees", "interface", "min_amount_out", "token_in", "token_out", "tracking_id")
	if err != nil {
		return nil, nil, err
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
	if err != nil || len(route) == 0 {
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

func strictScMap(v xdr.ScVal, names ...string) (map[string]xdr.ScVal, error) {
	if v.Type != xdr.ScValTypeScvMap || v.Map == nil || *v.Map == nil || len(**v.Map) != len(names) {
		return nil, errors.New("unexpected LI.FI struct")
	}
	fields := make(map[string]xdr.ScVal, len(names))
	for _, e := range **v.Map {
		if e.Key.Type != xdr.ScValTypeScvSymbol || e.Key.Sym == nil {
			return nil, errors.New("invalid LI.FI field name")
		}
		key := string(*e.Key.Sym)
		if _, exists := fields[key]; exists {
			return nil, errors.New("duplicate LI.FI field")
		}
		fields[key] = e.Val
	}
	for _, name := range names {
		if _, ok := fields[name]; !ok {
			return nil, errors.New("missing LI.FI field")
		}
	}
	return fields, nil
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
func sameAuthorizedCall(node xdr.SorobanAuthorizedInvocation, call *xdr.InvokeContractArgs) bool {
	return node.Function.Type == xdr.SorobanAuthorizedFunctionTypeSorobanAuthorizedFunctionTypeContractFn && reflect.DeepEqual(node.Function.ContractFn, call)
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
func checkLifiAquariusRoute(distribution, authorizedHops xdr.ScVal, want envelopeExpectation) error {
	fields, err := strictScMap(distribution, "bytes", "parts", "path", "protocol_id")
	if err != nil {
		return err
	}
	protocol, parts := fields["protocol_id"], fields["parts"]
	if protocol.Type != xdr.ScValTypeScvU32 || protocol.U32 == nil || *protocol.U32 != 2 || parts.Type != xdr.ScValTypeScvU32 || parts.U32 == nil || *parts.U32 == 0 {
		return errors.New("unsupported LI.FI route protocol")
	}
	path, err := scVec(fields["path"])
	if err != nil || len(path) < 2 || !addressEquals(path[0], want.SrcToken) || !addressEquals(path[len(path)-1], want.DstToken) {
		return errors.New("LI.FI path endpoints differ")
	}
	pools, err := scVec(fields["bytes"])
	if err != nil || len(pools) != len(path)-1 {
		return errors.New("LI.FI path pools differ")
	}
	hops, err := scVec(authorizedHops)
	if err != nil || len(hops) != len(pools) {
		return errors.New("LI.FI authorized path differs")
	}
	for i, pool := range pools {
		if pool.Type != xdr.ScValTypeScvBytes || pool.Bytes == nil || len(*pool.Bytes) != 32 {
			return errors.New("invalid LI.FI pool ID")
		}
		hop, err := scVec(hops[i])
		if err != nil || len(hop) != 3 {
			return errors.New("invalid LI.FI Aquarius hop")
		}
		pair, err := scVec(hop[0])
		if err != nil || len(pair) != 2 {
			return errors.New("invalid LI.FI Aquarius token pair")
		}
		samePair := (reflect.DeepEqual(pair[0], path[i]) && reflect.DeepEqual(pair[1], path[i+1])) || (reflect.DeepEqual(pair[1], path[i]) && reflect.DeepEqual(pair[0], path[i+1]))
		if !samePair || !reflect.DeepEqual(hop[1], pool) || !reflect.DeepEqual(hop[2], path[i+1]) {
			return errors.New("LI.FI Aquarius hop differs from route")
		}
	}
	return nil
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
