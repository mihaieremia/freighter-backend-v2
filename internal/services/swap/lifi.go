package swap

import (
	"context"
	"errors"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/stellar/freighter-backend-v2/internal/metrics"
	"github.com/stellar/freighter-backend-v2/internal/types"
	"github.com/stellar/freighter-backend-v2/internal/utils"
)

const lifiChainID int64 = 1201081091099710

// LI.FI supplies an already simulated, sequence-bound envelope. Never rebuild it.
type lifiSource struct {
	baseURL    string
	apiKey     string
	accounts   *horizonClient
	httpClient *http.Client
	now        func() time.Time
	svcMetrics *metrics.Service
}

func newLifiSource(key string, accounts *horizonClient, m *metrics.Service) *lifiSource {
	return &lifiSource{baseURL: "https://li.quest/v1", apiKey: key, accounts: accounts,
		httpClient: &http.Client{Timeout: xoxnoHTTPTimeout}, now: time.Now, svcMetrics: m}
}
func (s *lifiSource) Name() string { return types.SwapSourceLifi }

// Reverse sizing stays with Horizon/XOXNO; LI.FI competes in the forward round.
func (s *lifiSource) QuoteInput(context.Context, types.SwapQuoteRequest) (*big.Int, error) {
	return nil, errUnsupported
}

type lifiToken struct {
	Address  string `json:"address"`
	ChainID  int64  `json:"chainId"`
	Decimals *int   `json:"decimals"`
}
type lifiQuoteResponse struct {
	Type          string `json:"type"`
	Tool          string `json:"tool"`
	ExecutionType string `json:"executionType"`
	Action        struct {
		FromToken   lifiToken `json:"fromToken"`
		ToToken     lifiToken `json:"toToken"`
		FromChainID int64     `json:"fromChainId"`
		ToChainID   int64     `json:"toChainId"`
		FromAddress string    `json:"fromAddress"`
		ToAddress   string    `json:"toAddress"`
		FromAmount  string    `json:"fromAmount"`
	} `json:"action"`
	Estimate struct {
		Tool            string `json:"tool"`
		FromAmount      string `json:"fromAmount"`
		ToAmount        string `json:"toAmount"`
		ToAmountMin     string `json:"toAmountMin"`
		SkipApproval    bool   `json:"skipApproval"`
		ApprovalAddress string `json:"approvalAddress"`
	} `json:"estimate"`
	TransactionRequest struct {
		Data string `json:"data"`
	} `json:"transactionRequest"`
}

func (s *lifiSource) Quote(ctx context.Context, req types.SwapQuoteRequest) (_ *candidate, err error) {
	defer recordQuoteCall(s.svcMetrics, types.SwapSourceLifi, req.Network, time.Now(), &err)
	if req.Network != types.PUBLIC {
		return nil, errUnsupported
	}
	passphrase, err := networkPassphrase(req.Network)
	if err != nil {
		return nil, err
	}
	src, err := parseAsset(req.SourceAsset)
	if err != nil {
		return nil, err
	}
	dst, err := parseAsset(req.DestAsset)
	if err != nil {
		return nil, err
	}
	srcID, err := src.contractIDFor(passphrase)
	if err != nil {
		return nil, err
	}
	dstID, err := dst.contractIDFor(passphrase)
	if err != nil {
		return nil, err
	}
	input, err := utils.ParseDecimalAmount(req.SourceAmount, req.SourceDecimals)
	if err != nil {
		return nil, err
	}
	acct, err := s.accounts.account(ctx, req.Network, req.Sender)
	if errors.Is(err, errHorizonAccountNotFound) {
		return nil, errUnsupported
	}
	if err != nil {
		return nil, err
	}
	// LI.FI has no supported quote-only trustline flow. Other sources can open it first.
	if dst.isClassic() && !holdsAsset(acct, dst) {
		return nil, errUnsupported
	}
	params := url.Values{
		"fromChain": {strconv.FormatInt(lifiChainID, 10)}, "toChain": {strconv.FormatInt(lifiChainID, 10)},
		"fromToken": {srcID}, "toToken": {dstID}, "fromAmount": {input.String()},
		"fromAddress": {req.Sender}, "toAddress": {req.Sender},
		"slippage":       {strconv.FormatFloat(req.SlippagePercent/100, 'f', -1, 64)},
		"allowExchanges": {"soroswap"},
		"allowBridges":   {"none"},
		"order":          {"CHEAPEST"},
	}
	headers := http.Header{}
	if s.apiKey != "" {
		headers.Set("x-lifi-api-key", s.apiKey)
	}
	var q lifiQuoteResponse
	status, err := getJSON(ctx, s.httpClient, s.baseURL+"/quote?"+params.Encode(), &q, headers)
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusUnprocessableEntity:
		return nil, types.ErrSwapNoRoute
	case http.StatusBadRequest:
		return nil, errUnsupported
	default:
		return nil, statusError("lifi quote", status)
	}
	tokenMatches := func(token lifiToken, id string, decimals int) bool {
		return token.Address == id && token.ChainID == lifiChainID && token.Decimals != nil && *token.Decimals == decimals
	}
	if q.Type != "lifi" || q.Tool != "soroswap" || q.ExecutionType != "transaction" ||
		q.Action.FromChainID != lifiChainID || q.Action.ToChainID != lifiChainID ||
		q.Action.FromAddress != req.Sender || q.Action.ToAddress != req.Sender ||
		!tokenMatches(q.Action.FromToken, srcID, req.SourceDecimals) || !tokenMatches(q.Action.ToToken, dstID, req.DestDecimals) ||
		q.Action.FromAmount != input.String() || q.Estimate.FromAmount != input.String() ||
		q.Estimate.Tool != "soroswap" || !q.Estimate.SkipApproval || q.Estimate.ApprovalAddress != lifiRouter {
		return nil, invalidQuote("LI.FI quote does not match the same-chain request")
	}
	out, minimum, err := checkSlippage(&xoxnoQuoteResponse{AmountOut: q.Estimate.ToAmount, AmountOutMin: q.Estimate.ToAmountMin}, req.SlippagePercent)
	if err != nil {
		return nil, err
	}
	fee, err := verifyLifiEnvelope(q.TransactionRequest.Data, envelopeExpectation{
		Sender: req.Sender, Router: lifiRouter, SrcToken: srcID, SrcAtoms: input, DstToken: dstID, MinOut: minimum,
	}, acct.Sequence+1, s.now().Unix())
	if err != nil {
		return nil, invalidQuote("LI.FI envelope: %v", err)
	}
	return &candidate{Source: types.SwapSourceLifi, DestAmount: out, DestAmountMin: minimum, DestDecimals: req.DestDecimals,
		Transaction: &types.SwapTransaction{EnvelopeXDR: q.TransactionRequest.Data, RouterContract: lifiRouter, NetworkPassphrase: passphrase, Simulated: true},
		NetworkFee:  big.NewInt(int64(fee)),
	}, nil
}
