// ABOUTME: Tests for the XOXNO catalog service: earn-option derivation from
// ABOUTME: the market list and the cache-less read path.
package services

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wbtypes "github.com/stellar/wallet-backend/pkg/wbclient/types"

	"github.com/stellar/freighter-backend-v2/internal/types"
	"github.com/stellar/freighter-backend-v2/internal/utils"
)

func xoxnoMarketsFixture() []wbtypes.XoxnoLendingMarket {
	usdc, xlm := "USDC", "XLM"
	seven := int32(7)
	return []wbtypes.XoxnoLendingMarket{
		{HubID: 2, Asset: "CUSDC", TokenSymbol: &usdc, TokenDecimals: &seven, SupplyApy: 0.03, BorrowApy: 0.06, SuppliedUsd: f64(500), PriceUsd: f64(1),
			Reserves: []wbtypes.XoxnoLendingReserve{{SpokeID: 1}}},
		{HubID: 1, Asset: "CUSDC", TokenSymbol: &usdc, TokenDecimals: &seven, SupplyApy: 0.05, BorrowApy: 0.08, SuppliedUsd: f64(1000), PriceUsd: f64(1),
			Reserves: []wbtypes.XoxnoLendingReserve{{SpokeID: 1, Paused: true}, {SpokeID: 2}}},
		{HubID: 1, Asset: "CXLM", TokenSymbol: &xlm, TokenDecimals: &seven, SupplyApy: 0.01, BorrowApy: 0.02, SuppliedUsd: nil, PriceUsd: nil,
			Reserves: []wbtypes.XoxnoLendingReserve{{SpokeID: 1}}},
		{HubID: 3, Asset: "CFROZEN", SupplyApy: 0.9, SuppliedUsd: f64(9),
			Reserves: []wbtypes.XoxnoLendingReserve{{SpokeID: 1, Frozen: true}}}, // no open spoke: excluded
		{HubID: 3, Asset: "CUNLISTED", SupplyApy: 0.9, SuppliedUsd: f64(9)}, // no spoke at all: excluded
	}
}

func TestXoxnoEarnOptionsDerivation(t *testing.T) {
	svc := NewXoxnoCatalogService(&utils.MockWalletBackendService{GetXoxnoLendingMarketsResult: xoxnoMarketsFixture()}, nil, 0, nil)

	got, err := svc.GetEarnOptions(context.Background(), types.TESTNET)
	require.NoError(t, err)
	require.Len(t, got.Options, 2, "frozen and unlisted markets are excluded")

	usdc := got.Options[0]
	assert.Equal(t, "CUSDC", usdc.AssetID)
	require.NotNil(t, usdc.Symbol)
	assert.Equal(t, "USDC", *usdc.Symbol)
	require.Len(t, usdc.Pools, 2)
	assert.Equal(t, "1:CUSDC", usdc.Pools[0].ID, "hubs ordered by supplied USD desc")
	assert.Equal(t, "XOXNO Hub 1", *usdc.Pools[0].Name)
	assert.InDelta(t, 0.05, *usdc.Pools[0].SupplyAPY, 1e-12)
	assert.Nil(t, usdc.Pools[0].EmissionsSupplyAPR, "no emissions program")
	assert.Equal(t, "2:CUSDC", usdc.Pools[1].ID)

	xlm := got.Options[1]
	assert.Equal(t, "CXLM", xlm.AssetID)
	assert.Nil(t, xlm.Pools[0].SuppliedUSD, "unpriced stays null")
}

func TestXoxnoCatalogPropagatesUpstreamError(t *testing.T) {
	svc := NewXoxnoCatalogService(&utils.MockWalletBackendService{GetXoxnoLendingMarketsError: assert.AnError}, nil, 0, nil)
	_, err := svc.GetEarnOptions(context.Background(), types.PUBLIC)
	require.ErrorIs(t, err, assert.AnError)
}
