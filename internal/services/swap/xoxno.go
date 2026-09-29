package swap

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/stellar/freighter-backend-v2/internal/metrics"
	"github.com/stellar/freighter-backend-v2/internal/types"
	"github.com/stellar/freighter-backend-v2/internal/utils"
)

const xoxnoHTTPTimeout = 15 * time.Second

// Network is the per-network configuration of the aggregator source.
// A network with an empty QuoteURL is not quoted.
type Network struct {
	QuoteURL string
	// Router is the only contract a returned transaction may invoke. It is
	// pinned here so a compromised quote server cannot redirect a signature.
	Router string
	// TokenListURL is XOXNO's Stellar token list for the network, the curated set
	// the swap tokens route offers. Not used to quote.
	TokenListURL string
}

// xoxnoSource quotes through the XOXNO aggregator, which routes across
// Soroban AMMs and the classic DEX in one Soroban transaction.
type xoxnoSource struct {
	networks   map[string]Network
	accounts   *horizonClient
	httpClient *http.Client
	now        func() time.Time
	svcMetrics *metrics.Service
}

func newXoxnoSource(networks map[string]Network, accounts *horizonClient, svcMetrics *metrics.Service) *xoxnoSource {
	return &xoxnoSource{
		networks:   networks,
		accounts:   accounts,
		httpClient: &http.Client{Timeout: xoxnoHTTPTimeout},
		now:        time.Now,
		svcMetrics: svcMetrics,
	}
}

func (s *xoxnoSource) Name() string { return types.SwapSourceXoxno }

// xoxnoPair is a request's network settings and both assets as Stellar Asset
// Contract ids, which is how the aggregator names every token.
type xoxnoPair struct {
	cfg        Network
	passphrase string
	src, dst   string
	dstAsset   asset
}

func (s *xoxnoSource) resolve(req types.SwapQuoteRequest) (xoxnoPair, error) {
	cfg, ok := s.networks[req.Network]
	if !ok || cfg.QuoteURL == "" || cfg.Router == "" {
		return xoxnoPair{}, errUnsupported
	}
	passphrase, err := networkPassphrase(req.Network)
	if err != nil {
		return xoxnoPair{}, err
	}
	src, err := parseAsset(req.SourceAsset)
	if err != nil {
		return xoxnoPair{}, err
	}
	dst, err := parseAsset(req.DestAsset)
	if err != nil {
		return xoxnoPair{}, err
	}
	srcContract, err := src.contractIDFor(passphrase)
	if err != nil {
		return xoxnoPair{}, err
	}
	dstContract, err := dst.contractIDFor(passphrase)
	if err != nil {
		return xoxnoPair{}, err
	}
	return xoxnoPair{cfg: cfg, passphrase: passphrase, src: srcContract, dst: dstContract, dstAsset: dst}, nil
}

type xoxnoQuoteResponse struct {
	Mode         string   `json:"mode"`
	From         string   `json:"from"`
	To           string   `json:"to"`
	AmountIn     string   `json:"amountIn"`
	AmountOut    string   `json:"amountOut"`
	AmountOutMin string   `json:"amountOutMin"`
	DecimalsOut  *int     `json:"decimalsOut"`
	PriceImpact  *float64 `json:"priceImpact"`
	Hops         []struct {
		Dex     string `json:"dex"`
		Kind    string `json:"kind"`
		Address string `json:"address"`
		From    string `json:"from"`
		To      string `json:"to"`
	} `json:"hops"`
	Transaction *types.SwapTransaction `json:"transaction"`
}

func (s *xoxnoSource) Quote(ctx context.Context, req types.SwapQuoteRequest) (_ *candidate, err error) {
	defer recordQuoteCall(s.svcMetrics, types.SwapSourceXoxno, req.Network, time.Now(), &err)

	p, err := s.resolve(req)
	if err != nil {
		return nil, err
	}
	srcAtoms, err := utils.ParseDecimalAmount(req.SourceAmount, req.SourceDecimals)
	if err != nil {
		return nil, err
	}

	acct, err := s.accounts.account(ctx, req.Network, req.Sender)
	switch {
	case errors.Is(err, errHorizonAccountNotFound):
		return nil, errUnsupported
	case err != nil:
		return nil, err
	}

	// A Soroban swap is one operation, so it cannot open the trustline a classic
	// destination needs, and the aggregator cannot simulate a payout the account
	// cannot yet receive. Quote without a transaction and tell the client to open
	// the trustline first; it asks again once the trustline exists.
	needsTrustline := p.dstAsset.isClassic() && !holdsAsset(acct, p.dstAsset)

	quote, err := s.fetchQuote(ctx, p.cfg.QuoteURL, forwardQuery(req, p, srcAtoms, !needsTrustline))
	if err != nil {
		return nil, err
	}
	cand, err := validateXoxnoQuote(quote, p, srcAtoms, req.SlippagePercent, !needsTrustline)
	if err != nil {
		return nil, err
	}
	if needsTrustline {
		cand.RequiresTrustline = true
		return cand, nil
	}
	if err := s.attachTransaction(cand, quote.Transaction, req, p, srcAtoms, acct.Sequence); err != nil {
		return nil, err
	}
	return cand, nil
}

// forwardQuery is the aggregator's forward quote request for a fixed input.
func forwardQuery(req types.SwapQuoteRequest, p xoxnoPair, srcAtoms *big.Int, simulate bool) url.Values {
	return url.Values{
		"from":          {p.src},
		"to":            {p.dst},
		"amount_in":     {srcAtoms.String()},
		"slippage":      {strconv.FormatFloat(req.SlippagePercent/100, 'f', -1, 64)},
		"sender":        {req.Sender},
		"simulate":      {strconv.FormatBool(simulate)},
		"include_paths": {"false"},
	}
}

// attachTransaction verifies the aggregator's envelope against the request,
// stamps it for signing and sets it on cand together with its fee.
func (s *xoxnoSource) attachTransaction(cand *candidate, tx *types.SwapTransaction, req types.SwapQuoteRequest, p xoxnoPair, srcAtoms *big.Int, sequence int64) error {
	envelope, fee, err := prepareEnvelope(tx.EnvelopeXDR, envelopeExpectation{
		Sender:   req.Sender,
		Router:   p.cfg.Router,
		SrcToken: p.src,
		SrcAtoms: srcAtoms,
		DstToken: p.dst,
		MinOut:   cand.DestAmountMin,
	}, sequence+1, s.now().Unix()+req.TimeoutSeconds)
	if err != nil {
		return fmt.Errorf("%w: %v", errInvalidQuote, err)
	}
	tx.EnvelopeXDR = envelope
	cand.Transaction = tx
	cand.NetworkFee = big.NewInt(int64(fee))
	return nil
}

func (s *xoxnoSource) fetchQuote(ctx context.Context, baseURL string, q url.Values) (*xoxnoQuoteResponse, error) {
	var out xoxnoQuoteResponse
	status, err := getJSON(ctx, s.httpClient, baseURL+"/api/v1/quote?"+q.Encode(), &out)
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusOK:
		return &out, nil
	case http.StatusNotFound, http.StatusUnprocessableEntity:
		return nil, types.ErrSwapNoRoute
	case http.StatusBadRequest:
		return nil, errUnsupported
	default:
		return nil, statusError("xoxno quote", status)
	}
}

// QuoteInput asks the aggregator what input it needs to deliver the requested
// amount. The reverse quote is only used to size the input: its route floors the
// output at exactly the target, which has no room for price movement, so the
// swap itself is quoted forward at this input with the user's slippage.
func (s *xoxnoSource) QuoteInput(ctx context.Context, req types.SwapQuoteRequest) (_ *big.Int, err error) {
	defer recordQuoteCall(s.svcMetrics, types.SwapSourceXoxno+"_input", req.Network, time.Now(), &err)

	p, err := s.resolve(req)
	if err != nil {
		return nil, err
	}
	destAtoms, err := utils.ParseDecimalAmount(req.DestAmount, req.DestDecimals)
	if err != nil {
		return nil, err
	}

	quote, err := s.fetchQuote(ctx, p.cfg.QuoteURL, url.Values{
		"from":       {p.src},
		"to":         {p.dst},
		"amount_out": {destAtoms.String()},
	})
	if err != nil {
		return nil, err
	}
	input, out := atoms(quote.AmountIn), atoms(quote.AmountOut)
	if quote.Mode != "reverse" || quote.From != p.src || quote.To != p.dst ||
		input == nil || out == nil || out.Cmp(destAtoms) < 0 {
		return nil, fmt.Errorf("%w: quote does not answer the requested output", errInvalidQuote)
	}
	return input, nil
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

// validateXoxnoQuote checks that the aggregator answered the question that was
// asked. Anything that does not match is dropped, never trusted.
func validateXoxnoQuote(q *xoxnoQuoteResponse, p xoxnoPair, srcAtoms *big.Int, slippagePercent float64, wantTx bool) (*candidate, error) {
	if q.Mode != "forward" || q.From != p.src || q.To != p.dst {
		return nil, invalidQuote("quote is for a different pair or mode")
	}
	if in := atoms(q.AmountIn); in == nil || in.Cmp(srcAtoms) != 0 {
		return nil, invalidQuote("quote input %q differs from requested input", q.AmountIn)
	}

	out, minOut, err := checkSlippage(q, slippagePercent)
	if err != nil {
		return nil, err
	}

	// A classic destination is always quoted with 7 decimals.
	if q.DecimalsOut == nil || *q.DecimalsOut < 0 || *q.DecimalsOut > utils.MaxAmountDecimals {
		return nil, invalidQuote("quote output decimals are missing or out of range")
	}
	if p.dstAsset.isClassic() && *q.DecimalsOut != types.ClassicDecimals {
		return nil, invalidQuote("classic asset quoted with %d decimals", *q.DecimalsOut)
	}

	if wantTx {
		if err := checkSimulatedTransaction(q.Transaction, p); err != nil {
			return nil, err
		}
	}

	return &candidate{
		Source:        types.SwapSourceXoxno,
		DestAmount:    out,
		DestAmountMin: minOut,
		DestDecimals:  *q.DecimalsOut,
		Route:         routeHops(q),
		PriceImpact:   q.PriceImpact,
	}, nil
}

// checkSlippage returns the quoted output and minimum output after requiring
// 0 < minimum <= output and a minimum no lower than the requested slippage allows.
func checkSlippage(q *xoxnoQuoteResponse, slippagePercent float64) (out, minOut *big.Int, err error) {
	if out = atoms(q.AmountOut); out == nil {
		return nil, nil, invalidQuote("quote output %q is not positive", q.AmountOut)
	}
	if minOut = atoms(q.AmountOutMin); minOut == nil || minOut.Cmp(out) > 0 {
		return nil, nil, invalidQuote("quote minimum output %q is not in (0, output]", q.AmountOutMin)
	}
	if minOut.Cmp(minAmountOut(out, slippagePercent)) < 0 {
		return nil, nil, invalidQuote("quote minimum output %q is below the requested slippage", q.AmountOutMin)
	}
	return out, minOut, nil
}

// checkSimulatedTransaction requires a simulated transaction for the pinned
// router and the requested network.
func checkSimulatedTransaction(tx *types.SwapTransaction, p xoxnoPair) error {
	if tx == nil || tx.EnvelopeXDR == "" || !tx.Simulated {
		return invalidQuote("quote carries no simulated transaction")
	}
	if tx.RouterContract != p.cfg.Router || tx.NetworkPassphrase != p.passphrase {
		return invalidQuote("transaction targets an unexpected router or network")
	}
	return nil
}

func routeHops(q *xoxnoQuoteResponse) []types.SwapRouteHop {
	hops := make([]types.SwapRouteHop, 0, len(q.Hops))
	for _, h := range q.Hops {
		hops = append(hops, types.SwapRouteHop{Venue: h.Dex, Kind: h.Kind, Pool: h.Address, From: h.From, To: h.To})
	}
	return hops
}
