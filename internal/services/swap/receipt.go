package swap

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	stellarNetwork "github.com/stellar/go-stellar-sdk/network"
	"github.com/stellar/go-stellar-sdk/xdr"
	xoxno "github.com/xoxno/sdk-go"

	"github.com/stellar/freighter-backend-v2/internal/metrics"
	"github.com/stellar/freighter-backend-v2/internal/types"
	"github.com/stellar/freighter-backend-v2/internal/utils"
)

type receiptService struct {
	networks   map[string]Network
	horizon    *horizonClient
	expert     types.StellarExpertService
	svcMetrics *metrics.Service
}

func NewReceiptService(cfg Config, expert types.StellarExpertService, m *metrics.Service) types.SwapReceiptService {
	return &receiptService{cfg.Networks, newHorizonClient(cfg.HorizonPubnetURL, cfg.HorizonTestnetURL), expert, m}
}

func (s *receiptService) Name() string { return "swap-receipt" }

func (s *receiptService) GetSwapReceipt(ctx context.Context, net, hash, viewer string, operationIndex int) (_ *types.SwapReceipt, err error) {
	defer recordCall(s.svcMetrics, s.Name(), "GetSwapReceipt", net, time.Now(), &err)
	receipt := &types.SwapReceipt{Network: net, TransactionHash: hash, Viewer: viewer, OperationIndex: operationIndex, Status: "unavailable"}
	cfg, ok := s.networks[net]
	if !ok && net != types.PUBLIC {
		return receipt, nil
	}
	base, err := s.horizon.baseURL(net)
	if err != nil {
		return nil, err
	}
	var tx struct {
		Hash        string `json:"hash"`
		Successful  bool   `json:"successful"`
		EnvelopeXDR string `json:"envelope_xdr"`
		ResultXDR   string `json:"result_xdr"`
	}
	client := *s.horizon.httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	status, err := getJSON(ctx, &client, base+"/transactions/"+url.PathEscape(hash), &tx)
	if err != nil {
		return nil, receiptUpstreamError(err)
	}
	if status == http.StatusNotFound {
		return receipt, nil
	}
	if status != http.StatusOK {
		return nil, statusError("horizon receipt", status)
	}
	if hashErr := verifyReceiptHash(net, hash, tx.Hash, tx.EnvelopeXDR); hashErr != nil {
		return nil, receiptUpstreamError(hashErr)
	}
	if !tx.Successful || tx.ResultXDR == "" {
		return receipt, nil
	}
	meta, err := s.expert.GetTransactionMeta(ctx, net, hash)
	if err != nil {
		return nil, receiptUpstreamError(err)
	}
	if meta == "" {
		return receipt, nil
	}
	var result *xoxno.SwapReceipt
	if utils.IsValidContractID(cfg.Router) {
		result, err = xoxno.ReadReceipt(tx.EnvelopeXDR, tx.ResultXDR, meta, cfg.Router, viewer, operationIndex)
	}
	if err == nil && result == nil && net == types.PUBLIC {
		result, err = readLifiReceipt(tx.EnvelopeXDR, tx.ResultXDR, meta, viewer, operationIndex)
	}
	if err != nil {
		return nil, receiptUpstreamError(err)
	}
	if result == nil || result.AmountOut == nil {
		return receipt, nil
	}
	receipt.Status, receipt.TokenOut, receipt.ReceivedAtoms = "confirmed", result.TokenOut, result.AmountOut.String()
	return receipt, nil
}

// The envelope hash binds explorer events to the requested confirmed transaction,
// including an outer fee-bump hash. A supplied Horizon hash must agree too.
func verifyReceiptHash(net, requested, returned, envelope string) error {
	if returned != "" && !strings.EqualFold(requested, returned) {
		return fmt.Errorf("horizon receipt hash mismatch")
	}
	passphrase, err := networkPassphrase(net)
	if err != nil {
		return err
	}
	var env xdr.TransactionEnvelope
	if decodeErr := xdr.SafeUnmarshalBase64(envelope, &env); decodeErr != nil {
		return fmt.Errorf("decoding horizon receipt envelope: %w", decodeErr)
	}
	computed, err := stellarNetwork.HashTransactionInEnvelope(env, passphrase)
	if err != nil {
		return err
	}
	if !strings.EqualFold(requested, hex.EncodeToString(computed[:])) {
		return fmt.Errorf("horizon receipt envelope hash mismatch")
	}
	return nil
}

func receiptUpstreamError(err error) error {
	return &metrics.UpstreamError{Kind: "http_error", Err: err}
}
