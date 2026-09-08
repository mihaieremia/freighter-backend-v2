// ABOUTME: Tests for the XOXNO wallet-backend service methods through a fake
// ABOUTME: GraphQL server: decoding and unknown-account normalization.
package services

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetXoxnoLendingPositions(t *testing.T) {
	ctx := context.Background()

	t.Run("decodes position NFTs through the SDK", func(t *testing.T) {
		svc := newBlendTestService(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"data": {"accountByAddress": {"xoxnoLendingPositions": [{
				"accountId": "42", "owner": "` + blendTestAddress + `", "spokeId": 1, "positionMode": 0,
				"totalSuppliedUsd": 10.5, "totalBorrowedUsd": 0, "healthFactor": null,
				"positions": [{"hubId": 1, "asset": "CUSDC", "supplyScaledRay": "1", "supplyAmount": "2",
					"borrowScaledRay": "0", "borrowAmount": "0", "supplyUsd": 10.5, "borrowUsd": 0,
					"entryLoanToValueBps": 7500, "entryLiquidationThresholdBps": 8000}]
			}]}}}`))
		})
		got, err := svc.GetXoxnoLendingPositions(ctx, blendTestAddress, "TESTNET")
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "42", got[0].AccountID)
		assert.InDelta(t, 10.5, *got[0].TotalSuppliedUsd, 0)
		assert.Nil(t, got[0].HealthFactor)
		assert.Equal(t, int32(8000), got[0].Positions[0].EntryLiquidationThresholdBps)
	})

	t.Run("unknown account normalizes to no positions", func(t *testing.T) {
		svc := newBlendTestService(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"data": {"accountByAddress": null}}`))
		})
		got, err := svc.GetXoxnoLendingPositions(ctx, blendTestAddress, "TESTNET")
		require.NoError(t, err)
		assert.Empty(t, got)
		assert.NotNil(t, got)
	})

	t.Run("unconfigured network errors", func(t *testing.T) {
		svc := newBlendTestService(t, func(w http.ResponseWriter, r *http.Request) {})
		_, err := svc.GetXoxnoLendingPositions(ctx, blendTestAddress, "FUTURENET")
		require.ErrorContains(t, err, "not configured")
	})
}

func TestGetXoxnoLendingMarkets(t *testing.T) {
	svc := newBlendTestService(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data": {"xoxnoLendingMarkets": [{
			"hubId": 1, "asset": "CUSDC", "tokenSymbol": "USDC", "tokenName": null, "tokenDecimals": 7,
			"supplied": "100", "borrowed": "30", "utilization": 0.3, "supplyApy": 0.05, "borrowApy": 0.08,
			"suppliedUsd": 40, "borrowedUsd": 12, "priceUsd": 4,
			"reserves": [{"spokeId": 1, "isCollateralizable": true, "isBorrowable": true, "paused": false, "frozen": false,
				"loanToValueBps": 7000, "liquidationThresholdBps": 8000, "liquidationBonusBps": 500, "supplyCap": "0", "borrowCap": "0"}]
		}]}}`))
	})
	got, err := svc.GetXoxnoLendingMarkets(context.Background(), "TESTNET")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "USDC", *got[0].TokenSymbol)
	assert.InDelta(t, 0.05, got[0].SupplyApy, 0)
	assert.Equal(t, int32(8000), got[0].Reserves[0].LiquidationThresholdBps)
}
