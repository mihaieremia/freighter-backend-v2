package swap

import (
	"math/big"
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stellar/freighter-backend-v2/internal/utils"
)

func addrVal(a xdr.ScAddress) xdr.ScVal {
	return xdr.ScVal{Type: xdr.ScValTypeScvAddress, Address: &a}
}

func i128Val(n int64) xdr.ScVal {
	return xdr.ScVal{Type: xdr.ScValTypeScvI128, I128: &xdr.Int128Parts{Hi: 0, Lo: xdr.Uint64(n)}}
}

func rootNode(t *testing.T, subs ...xdr.SorobanAuthorizedInvocation) xdr.SorobanAuthorizedInvocation {
	payload := xdr.ScBytes(goodPayload(t))
	return xdr.SorobanAuthorizedInvocation{
		Function: xdr.SorobanAuthorizedFunction{
			Type: xdr.SorobanAuthorizedFunctionTypeSorobanAuthorizedFunctionTypeContractFn,
			ContractFn: &xdr.InvokeContractArgs{
				ContractAddress: mustAddr(utils.ScAddressFromContractString(testContract(2))),
				FunctionName:    routerFunction,
				Args: []xdr.ScVal{
					addrVal(mustAddr(utils.ScAddressFromAccountString(testSender))),
					i128Val(100_0000000),
					{Type: xdr.ScValTypeScvBytes, Bytes: &payload},
				},
			},
		},
		SubInvocations: subs,
	}
}

func transferNode(token, from string, amount int64) xdr.SorobanAuthorizedInvocation {
	return xdr.SorobanAuthorizedInvocation{Function: xdr.SorobanAuthorizedFunction{
		Type: xdr.SorobanAuthorizedFunctionTypeSorobanAuthorizedFunctionTypeContractFn,
		ContractFn: &xdr.InvokeContractArgs{
			ContractAddress: mustAddr(utils.ScAddressFromContractString(token)),
			FunctionName:    "transfer",
			Args:            []xdr.ScVal{addrVal(mustAddr(utils.ScAddressFromAccountString(from))), addrVal(mustAddr(utils.ScAddressFromContractString(testContract(2)))), i128Val(amount)},
		},
	}}
}

// testPayload builds the ScVal-encoded strategy payload the router expects:
// a struct {amounts, assets, ops} whose ops header names token_in, token_out and
// min_out by registry index.
type payloadOpts struct {
	version  byte
	referral byte
	// badIndex points token_in past the end of the asset registry; extraField
	// adds a struct field the router does not define.
	badIndex   bool
	extraField bool
	tokenIn    string
	tokenOut   string
	minOut     int64
}

func buildPayload(t *testing.T, o payloadOpts) []byte {
	t.Helper()
	header := []byte{o.version, 0, 1, 0, 0, 0, 0, o.referral, 0, 0}
	if o.badIndex {
		header[routeHdrTokenIn] = 9
	}
	assets := []xdr.ScVal{addrVal(mustAddr(utils.ScAddressFromContractString(o.tokenIn))), addrVal(mustAddr(utils.ScAddressFromContractString(o.tokenOut)))}
	amounts := []xdr.ScVal{i128Val(o.minOut)}
	vec := func(v []xdr.ScVal) xdr.ScVal {
		sv := xdr.ScVec(v)
		p := &sv
		return xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &p}
	}
	sym := func(s string) xdr.ScVal {
		x := xdr.ScSymbol(s)
		return xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &x}
	}
	ops := xdr.ScBytes(header)
	m := xdr.ScMap{
		{Key: sym("amounts"), Val: vec(amounts)},
		{Key: sym("assets"), Val: vec(assets)},
		{Key: sym("ops"), Val: xdr.ScVal{Type: xdr.ScValTypeScvBytes, Bytes: &ops}},
	}
	if o.extraField {
		m = append(m, xdr.ScMapEntry{Key: sym("zzz"), Val: xdr.ScVal{Type: xdr.ScValTypeScvVoid}})
	}
	mp := &m
	out, err := (xdr.ScVal{Type: xdr.ScValTypeScvMap, Map: &mp}).MarshalBinary()
	require.NoError(t, err)
	return out
}

func goodPayload(t *testing.T) []byte {
	return buildPayload(t, payloadOpts{version: 1, tokenIn: testContract(3), tokenOut: testContract(4), minOut: 990})
}

type envOpts struct {
	payload   []byte
	router    string
	function  xdr.ScSymbol
	sender    string
	amount    int64
	fee       xdr.Uint32
	signed    bool
	memo      bool
	muxed     bool
	opSource  bool
	extraOp   bool
	unsoroban bool
	auth      []xdr.SorobanAuthorizationEntry
}

func buildEnvelope(t *testing.T, o envOpts) string {
	t.Helper()
	payloadBytes := xdr.ScBytes(o.payload)
	if o.payload == nil {
		payloadBytes = xdr.ScBytes(goodPayload(t))
	}
	call := &xdr.InvokeContractArgs{
		ContractAddress: mustAddr(utils.ScAddressFromContractString(o.router)),
		FunctionName:    o.function,
		Args: []xdr.ScVal{
			addrVal(mustAddr(utils.ScAddressFromAccountString(o.sender))),
			i128Val(o.amount),
			{Type: xdr.ScValTypeScvBytes, Bytes: &payloadBytes},
		},
	}
	if o.auth == nil {
		root := rootNode(t)
		root.Function.ContractFn = call
		o.auth = []xdr.SorobanAuthorizationEntry{{
			Credentials:    xdr.SorobanCredentials{Type: xdr.SorobanCredentialsTypeSorobanCredentialsSourceAccount},
			RootInvocation: root,
		}}
	}
	op := xdr.Operation{Body: xdr.OperationBody{
		Type: xdr.OperationTypeInvokeHostFunction,
		InvokeHostFunctionOp: &xdr.InvokeHostFunctionOp{
			HostFunction: xdr.HostFunction{Type: xdr.HostFunctionTypeHostFunctionTypeInvokeContract, InvokeContract: call},
			Auth:         o.auth,
		},
	}}
	tx := xdr.Transaction{
		SourceAccount: xdr.MustMuxedAddress(testSender),
		Fee:           o.fee,
		Operations:    []xdr.Operation{op},
		Cond:          xdr.Preconditions{Type: xdr.PreconditionTypePrecondNone},
		Memo:          xdr.Memo{Type: xdr.MemoTypeMemoNone},
	}
	if !o.unsoroban {
		tx.Ext = xdr.TransactionExt{V: 1, SorobanData: &xdr.SorobanTransactionData{}}
	}
	if o.muxed {
		tx.SourceAccount = xdr.MuxedAccount{Type: xdr.CryptoKeyTypeKeyTypeMuxedEd25519, Med25519: &xdr.MuxedAccountMed25519{Id: 1, Ed25519: *xdr.MustAddress(testSender).Ed25519}}
	}
	if o.opSource {
		src := xdr.MustMuxedAddress(testSender)
		tx.Operations[0].SourceAccount = &src
	}
	if o.memo {
		text := "hi"
		tx.Memo = xdr.Memo{Type: xdr.MemoTypeMemoText, Text: &text}
	}
	if o.extraOp {
		tx.Operations = append(tx.Operations, op)
	}
	env := xdr.TransactionEnvelope{Type: xdr.EnvelopeTypeEnvelopeTypeTx, V1: &xdr.TransactionV1Envelope{Tx: tx}}
	if o.signed {
		env.V1.Signatures = []xdr.DecoratedSignature{{}}
	}
	out, err := xdr.MarshalBase64(env)
	require.NoError(t, err)
	return out
}

func goodOpts() (envOpts, envelopeExpectation) {
	router := testContract(2)
	return envOpts{router: router, function: routerFunction, sender: testSender, amount: 100_0000000, fee: 500_000},
		envelopeExpectation{Sender: testSender, Router: router, SrcToken: testContract(3), SrcAtoms: big.NewInt(100_0000000), DstToken: testContract(4), MinOut: big.NewInt(990)}
}

func TestPrepareSwapEnvelope_StampsSequenceAndExpiry(t *testing.T) {
	t.Parallel()
	o, want := goodOpts()
	out, fee, err := prepareEnvelope(buildEnvelope(t, o), want, 777, 1_700_000_000)
	require.NoError(t, err)
	assert.Equal(t, uint32(500_000), fee)

	var env xdr.TransactionEnvelope
	require.NoError(t, xdr.SafeUnmarshalBase64(out, &env))
	assert.Equal(t, xdr.SequenceNumber(777), env.V1.Tx.SeqNum)
	require.NotNil(t, env.V1.Tx.Cond.TimeBounds)
	assert.Equal(t, xdr.TimePoint(1_700_000_000), env.V1.Tx.Cond.TimeBounds.MaxTime)
	assert.Equal(t, xdr.Uint32(500_000), env.V1.Tx.Fee, "the simulated fee is left alone")
	assert.Empty(t, env.V1.Signatures)
}

func TestPrepareSwapEnvelope_Rejects(t *testing.T) {
	t.Parallel()
	src, other := testContract(3), testContract(4)
	sourceAcctAuth := func(root xdr.SorobanAuthorizedInvocation) []xdr.SorobanAuthorizationEntry {
		return []xdr.SorobanAuthorizationEntry{{
			Credentials:    xdr.SorobanCredentials{Type: xdr.SorobanCredentialsTypeSorobanCredentialsSourceAccount},
			RootInvocation: root,
		}}
	}

	badPayload := func(mut func(p *payloadOpts)) func(o *envOpts) {
		return func(o *envOpts) {
			p := payloadOpts{version: 1, tokenIn: testContract(3), tokenOut: testContract(4), minOut: 990}
			mut(&p)
			o.payload = buildPayload(t, p)
		}
	}
	cases := map[string]func(o *envOpts){
		"payload other input token":  badPayload(func(p *payloadOpts) { p.tokenIn = testContract(8) }),
		"payload other output token": badPayload(func(p *payloadOpts) { p.tokenOut = testContract(8) }),
		"payload lower minimum":      badPayload(func(p *payloadOpts) { p.minOut = 989 }),
		"payload zero minimum":       badPayload(func(p *payloadOpts) { p.minOut = 0 }),
		"payload referral":           badPayload(func(p *payloadOpts) { p.referral = 1 }),
		"payload other version":      badPayload(func(p *payloadOpts) { p.version = 2 }),
		"payload index outside":      badPayload(func(p *payloadOpts) { p.badIndex = true }),
		"payload unknown field":      badPayload(func(p *payloadOpts) { p.extraField = true }),
		"payload not a struct":       func(o *envOpts) { o.payload = []byte{0, 0, 0, 1} },
		"payload garbage":            func(o *envOpts) { o.payload = []byte{1, 2, 3} },
		"wrong router":               func(o *envOpts) { o.router = testContract(8) },
		"wrong function":             func(o *envOpts) { o.function = "drain" },
		"wrong sender arg":           func(o *envOpts) { o.sender = testIssuer },
		"amount differs":             func(o *envOpts) { o.amount++ },
		"already signed":             func(o *envOpts) { o.signed = true },
		"memo":                       func(o *envOpts) { o.memo = true },
		"muxed source":               func(o *envOpts) { o.muxed = true },
		"operation source override":  func(o *envOpts) { o.opSource = true },
		"two operations":             func(o *envOpts) { o.extraOp = true },
		"fee too high":               func(o *envOpts) { o.fee = maxEnvelopeFee + 1 },
		"zero fee":                   func(o *envOpts) { o.fee = 0 },
		"no soroban data":            func(o *envOpts) { o.unsoroban = true },
		"address credentials": func(o *envOpts) {
			o.auth = []xdr.SorobanAuthorizationEntry{{
				Credentials: xdr.SorobanCredentials{
					Type:    xdr.SorobanCredentialsTypeSorobanCredentialsAddress,
					Address: &xdr.SorobanAddressCredentials{Address: mustAddr(utils.ScAddressFromAccountString(testSender)), Signature: xdr.ScVal{Type: xdr.ScValTypeScvVoid}},
				},
				RootInvocation: rootNode(t),
			}}
		},
		"other token moved":   func(o *envOpts) { o.auth = sourceAcctAuth(rootNode(t, transferNode(other, testSender, 1))) },
		"transfer over input": func(o *envOpts) { o.auth = sourceAcctAuth(rootNode(t, transferNode(src, testSender, 100_0000001))) },
		"transfers add up": func(o *envOpts) {
			o.auth = sourceAcctAuth(rootNode(t, transferNode(src, testSender, 60_0000000), transferNode(src, testSender, 60_0000000)))
		},
		"transfer sender unreadable": func(o *envOpts) {
			n := transferNode(src, testSender, 1)
			n.Function.ContractFn.Args[0] = i128Val(1)
			o.auth = sourceAcctAuth(rootNode(t, n))
		},
		"nested approve": func(o *envOpts) {
			n := transferNode(src, testSender, 1)
			n.Function.ContractFn.FunctionName = "approve"
			o.auth = sourceAcctAuth(rootNode(t, rootNode(t, n)))
		},
		"unknown authorized call": func(o *envOpts) {
			n := transferNode(other, testSender, 1)
			n.Function.ContractFn.FunctionName = "set_admin"
			n.Function.ContractFn.Args = []xdr.ScVal{addrVal(mustAddr(utils.ScAddressFromAccountString(testIssuer)))}
			o.auth = sourceAcctAuth(rootNode(t, n))
		},
		"other authorization root": func(o *envOpts) {
			n := rootNode(t)
			n.Function.ContractFn.ContractAddress = mustAddr(utils.ScAddressFromContractString(other))
			o.auth = sourceAcctAuth(n)
		},
		"authorization root different amount": func(o *envOpts) {
			n := rootNode(t)
			n.Function.ContractFn.Args[1] = i128Val(1)
			o.auth = sourceAcctAuth(n)
		},
		"malformed transfer": func(o *envOpts) {
			n := transferNode(src, testSender, 1)
			n.Function.ContractFn.Args = n.Function.ContractFn.Args[:2]
			o.auth = sourceAcctAuth(rootNode(t, n))
		},
		"transfer to other destination": func(o *envOpts) {
			n := transferNode(src, testSender, 1)
			n.Function.ContractFn.Args[1] = addrVal(mustAddr(utils.ScAddressFromContractString(testContract(9))))
			o.auth = sourceAcctAuth(rootNode(t, n))
		},
		"transfer from other sender": func(o *envOpts) {
			o.auth = sourceAcctAuth(rootNode(t, transferNode(src, testIssuer, 1)))
		},
		"nested transfer": func(o *envOpts) {
			n := transferNode(src, testSender, 1)
			n.SubInvocations = []xdr.SorobanAuthorizedInvocation{transferNode(src, testSender, 1)}
			o.auth = sourceAcctAuth(rootNode(t, n))
		},
		"no authorization": func(o *envOpts) { o.auth = []xdr.SorobanAuthorizationEntry{} },
	}
	exact := map[string]string{"transfer sender unreadable": "authorization transfer sender is unreadable"}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			o, want := goodOpts()
			mutate(&o)
			_, _, err := prepareEnvelope(buildEnvelope(t, o), want, 1, 2)
			if msg, ok := exact[name]; ok {
				assert.EqualError(t, err, msg)
				return
			}
			assert.Error(t, err)
		})
	}
}

func TestPrepareSwapEnvelope_Accepts(t *testing.T) {
	t.Parallel()
	src := testContract(3)
	cases := map[string]func(o *envOpts){
		"transfers up to the exact input": func(o *envOpts) {
			o.auth = []xdr.SorobanAuthorizationEntry{{
				Credentials: xdr.SorobanCredentials{Type: xdr.SorobanCredentialsTypeSorobanCredentialsSourceAccount},
				RootInvocation: rootNode(t,
					transferNode(src, testSender, 60_0000000),
					transferNode(src, testSender, 40_0000000),
				),
			}}
		},
		"a higher payload minimum": func(o *envOpts) {
			o.payload = buildPayload(t, payloadOpts{version: 1, tokenIn: src, tokenOut: testContract(4), minOut: 991})
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			o, want := goodOpts()
			mutate(&o)
			_, _, err := prepareEnvelope(buildEnvelope(t, o), want, 1, 2)
			assert.NoError(t, err)
		})
	}
}

func TestPrepareSwapEnvelope_GarbageInput(t *testing.T) {
	t.Parallel()
	_, want := goodOpts()
	_, _, err := prepareEnvelope("not-base64!!", want, 1, 2)
	assert.Error(t, err)
}
