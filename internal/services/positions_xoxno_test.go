// ABOUTME: Tests for the XOXNO half of the positions service: leg rows, net
// ABOUTME: figures and the merge with Blend rows in one account response.
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

// fixedMarkets is a types.XoxnoCatalogService whose market list is fixed;
// the earn view is never read by the positions service.
type fixedMarkets []wbtypes.XoxnoLendingMarket

func (fixedMarkets) Name() string { return "fixed-markets" }

func (m fixedMarkets) GetMarkets(context.Context, string) ([]wbtypes.XoxnoLendingMarket, error) {
	return m, nil
}

func (fixedMarkets) GetEarnOptions(context.Context, string) (*types.EarnOptionsCatalog, error) {
	return &types.EarnOptionsCatalog{Options: []types.EarnAssetOption{}}, nil
}

// One position: supplies 1000 USDC in hub 1 (5%), borrows 300.5 USDC in hub 2
// (6%); net APY = (1000×0.05 − 300.5×0.06) / 1000 = 0.03197. Amounts are
// whole-token decimal strings, as upstream reports them (price 1 → $ = tokens).
func xoxnoAccountFixture() wbtypes.XoxnoLendingAccount {
	hf := 2.5
	return wbtypes.XoxnoLendingAccount{
		AccountID: "42", Owner: blendTestAddress, SpokeID: 1, PositionMode: 1,
		TotalSuppliedUsd: f64(1000), TotalBorrowedUsd: f64(300.5), HealthFactor: &hf,
		Positions: []wbtypes.XoxnoLendingPosition{
			{HubID: 1, Asset: "CUSDC", SupplyAmount: "1000", BorrowAmount: "0", SupplyUsd: f64(1000), BorrowUsd: f64(0)},
			{HubID: 2, Asset: "CUSDC", SupplyAmount: "0.0", BorrowAmount: "300.5", SupplyUsd: f64(0), BorrowUsd: f64(300.5)},
		},
	}
}

func TestMapXoxnoPositions(t *testing.T) {
	markets := indexXoxnoMarkets(xoxnoMarketsFixture())

	t.Run("maps legs, net figures and apy", func(t *testing.T) {
		rows := mapXoxnoPositions([]wbtypes.XoxnoLendingAccount{xoxnoAccountFixture()}, markets)
		require.Len(t, rows, 1)
		row := rows[0]
		assert.Equal(t, "xoxno", row.Protocol)
		assert.Equal(t, "42", row.ID)
		assert.Equal(t, "XOXNO Position #42", *row.Name)
		assert.InDelta(t, 699.5, *row.NetUSD, 1e-9)
		assert.InDelta(t, 0.03197, *row.NetAPY, 1e-12)
		assert.Nil(t, row.Blend)
		require.NotNil(t, row.Xoxno)
		assert.Equal(t, int32(1), row.Xoxno.PositionMode)
		assert.InDelta(t, 2.5, *row.Xoxno.HealthFactor, 0)
		require.Len(t, row.Xoxno.Supply, 1)
		require.Len(t, row.Xoxno.Borrow, 1)
		require.NotNil(t, row.Xoxno.Supply[0].Tokens)
		assert.Equal(t, "10000000000", *row.Xoxno.Supply[0].Tokens, "whole tokens × 10^7")
		require.NotNil(t, row.Xoxno.Borrow[0].Tokens)
		assert.Equal(t, "3005000000", *row.Xoxno.Borrow[0].Tokens)
		assert.Equal(t, "USDC", *row.Xoxno.Supply[0].Symbol)
		assert.InDelta(t, 0.05, *row.Xoxno.Supply[0].APY, 1e-12)
		assert.InDelta(t, 0.06, *row.Xoxno.Borrow[0].APY, 1e-12)
		assert.Equal(t, int32(2), row.Xoxno.Borrow[0].HubID)
	})

	t.Run("unpriced leg nulls net figures", func(t *testing.T) {
		a := xoxnoAccountFixture()
		a.TotalSuppliedUsd, a.TotalBorrowedUsd = nil, nil
		a.Positions[0].SupplyUsd = nil
		rows := mapXoxnoPositions([]wbtypes.XoxnoLendingAccount{a}, markets)
		assert.Nil(t, rows[0].NetUSD)
		assert.Nil(t, rows[0].NetAPY)
		assert.Nil(t, rows[0].Xoxno.Supply[0].USDValue)
	})

	t.Run("unknown market keeps the leg but drops apy and base units", func(t *testing.T) {
		rows := mapXoxnoPositions([]wbtypes.XoxnoLendingAccount{xoxnoAccountFixture()}, nil)
		assert.Nil(t, rows[0].NetAPY)
		assert.Nil(t, rows[0].Xoxno.Supply[0].APY)
		assert.Nil(t, rows[0].Xoxno.Supply[0].Symbol)
		assert.Nil(t, rows[0].Xoxno.Supply[0].Tokens, "decimals unknown")
		assert.InDelta(t, 699.5, *rows[0].NetUSD, 1e-9)
	})
}

func TestToBaseUnits(t *testing.T) {
	seven := int32(7)
	for _, tc := range []struct {
		tokens string
		want   string
	}{
		{"14903.2441", "149032441000"},
		{"0.000000000000000000000000001", "0"}, // 27 fractional digits truncate toward zero
		{"1.99999999", "19999999"},
		{"-1.5", "-15000000"},
	} {
		got := toBaseUnits(parseWholeTokens(tc.tokens), &seven)
		require.NotNil(t, got, tc.tokens)
		assert.Equal(t, tc.want, *got, tc.tokens)
	}
	assert.Nil(t, toBaseUnits(parseWholeTokens("1"), nil))
	assert.Equal(t, 0, parseWholeTokens("garbage").Sign(), "unparseable is zero")
}

func TestGetAccountsPositionsMergesXoxno(t *testing.T) {
	name := "TestnetV2"
	mockWB := &utils.MockWalletBackendService{
		GetBlendPositionsResult: &wbtypes.BlendAccountPositions{
			Pools: []wbtypes.BlendPoolPosition{{PoolAddress: "CPOOL", PoolName: &name, UsdValue: f64(100), SuppliedUsd: f64(100), BorrowedUsd: f64(0), NetApy: f64(0.02)}},
		},
		GetXoxnoLendingPositionsResult: []wbtypes.XoxnoLendingAccount{xoxnoAccountFixture()},
	}
	svc := NewPositionsService(mockWB, fixedMarkets(xoxnoMarketsFixture()), 0, nil)

	results, err := svc.GetAccountsPositions(context.Background(), []string{blendTestAddress}, types.TESTNET)
	require.NoError(t, err)
	require.Len(t, results, 1)
	got := results[0]
	require.Len(t, got.Positions, 2)
	assert.Equal(t, "blend", got.Positions[0].Protocol)
	assert.Equal(t, "xoxno", got.Positions[1].Protocol)
	// Header: total 100 + 699.5; APY weighted by supplied: (0.02×100 + 0.03197×1000)/1100.
	require.NotNil(t, got.TotalValueUSD)
	assert.InDelta(t, 799.5, *got.TotalValueUSD, 1e-9)
	require.NotNil(t, got.NetAPY)
	assert.InDelta(t, (0.02*100+0.03197*1000)/1100, *got.NetAPY, 1e-12)

	t.Run("without a markets reader XOXNO is left out", func(t *testing.T) {
		svc := NewPositionsService(mockWB, nil, 0, nil)
		results, err := svc.GetAccountsPositions(context.Background(), []string{blendTestAddress}, types.TESTNET)
		require.NoError(t, err)
		require.Len(t, results[0].Positions, 1)
	})

	t.Run("an XOXNO upstream failure fails the request", func(t *testing.T) {
		failing := *mockWB
		failing.GetXoxnoLendingPositionsError = assert.AnError
		svc := NewPositionsService(&failing, fixedMarkets(nil), 0, nil)
		_, err := svc.GetAccountsPositions(context.Background(), []string{blendTestAddress}, types.TESTNET)
		require.ErrorIs(t, err, assert.AnError)
	})
}
