// ABOUTME: XOXNO lending methods on the wallet-backend service: an account's
// ABOUTME: position NFTs and the market catalog, via the wbclient SDK.
package services

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/stellar/wallet-backend/pkg/wbclient"
	wbtypes "github.com/stellar/wallet-backend/pkg/wbclient/types"
)

// GetXoxnoLendingPositions always returns a non-nil slice on success.
func (w *walletBackendService) GetXoxnoLendingPositions(ctx context.Context, address, network string) (_ []wbtypes.XoxnoLendingAccount, err error) {
	start := time.Now()
	defer func() { w.recordWBCall("GetXoxnoLendingPositions", network, start, err) }()

	client := w.configureNetworkClient(network)
	if client == nil {
		return nil, fmt.Errorf("wallet backend client not configured for network: %s", network)
	}

	positions, err := client.GetAccountXoxnoLendingPositions(ctx, address)
	if err != nil {
		if errors.Is(err, wbclient.ErrAccountNotFound) {
			return []wbtypes.XoxnoLendingAccount{}, nil
		}
		return nil, classifyWBError(err)
	}
	if positions == nil {
		return []wbtypes.XoxnoLendingAccount{}, nil
	}
	return positions, nil
}

// GetXoxnoLendingMarkets always returns a non-nil slice on success.
func (w *walletBackendService) GetXoxnoLendingMarkets(ctx context.Context, network string) (_ []wbtypes.XoxnoLendingMarket, err error) {
	start := time.Now()
	defer func() { w.recordWBCall("GetXoxnoLendingMarkets", network, start, err) }()

	client := w.configureNetworkClient(network)
	if client == nil {
		return nil, fmt.Errorf("wallet backend client not configured for network: %s", network)
	}

	markets, err := client.GetXoxnoLendingMarkets(ctx)
	if err != nil {
		return nil, classifyWBError(err)
	}
	if markets == nil {
		return []wbtypes.XoxnoLendingMarket{}, nil
	}
	return markets, nil
}
