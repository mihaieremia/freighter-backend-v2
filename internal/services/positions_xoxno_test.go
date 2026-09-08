// ABOUTME: Tests for the XOXNO half of the positions service: leg rows, net
// ABOUTME: figures, base-unit scaling, and the assembled account response.
package services

import (
	"context"
	"fmt"
	"math/big"
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

// failingMarkets is a types.XoxnoCatalogService whose every read fails.
type failingMarkets struct{ err error }

func (failingMarkets) Name() string { return "failing-markets" }

func (f failingMarkets) GetMarkets(context.Context, string) ([]wbtypes.XoxnoLendingMarket, error) {
	return nil, f.err
}

func (f failingMarkets) GetEarnOptions(context.Context, string) (*types.EarnOptionsCatalog, error) {
	return nil, f.err
}

// One position: supplies 1000 USDC in hub 1 (5%), borrows 300.5 USDC in hub 2
// (6%); net APY = (1000×0.05 − 300.5×0.06) / 1000 = 0.03197. Amounts are
// whole-token decimal strings, as upstream reports them (price 1 → $ = tokens).
func xoxnoAccountFixture() wbtypes.XoxnoLendingAccount {
	hf := 2.5
	return wbtypes.XoxnoLendingAccount{
		AccountID: "42", Owner: xoxnoTestAddress, SpokeID: 1, PositionMode: 1,
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
		rows := mapXoxnoPositions(types.PUBLIC, []wbtypes.XoxnoLendingAccount{xoxnoAccountFixture()}, markets)
		require.Len(t, rows, 1)
		row := rows[0]
		assert.Equal(t, "xoxno", row.Protocol)
		assert.Equal(t, "42", row.ID)
		// Spoke 1 on pubnet is "Blue Chip": the spoke is the position's market
		// identity, so it names the row rather than the account id.
		assert.Equal(t, "Blue Chip", row.Name)
		assert.Equal(t, "Blue Chip", *row.Xoxno.SpokeName)
		// The hub names the leg, not the position: legs of one position can
		// sit in different hubs.
		assert.Equal(t, "Core", row.Xoxno.Supply[0].HubName)
		assert.InDelta(t, 699.5, *row.NetUSD, 1e-9)
		assert.InDelta(t, 0.03197, *row.NetAPY, 1e-12)
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
		rows := mapXoxnoPositions(types.PUBLIC, []wbtypes.XoxnoLendingAccount{a}, markets)
		assert.Nil(t, rows[0].NetUSD)
		assert.Nil(t, rows[0].NetAPY)
		assert.Nil(t, rows[0].Xoxno.Supply[0].USDValue)
	})

	t.Run("unknown market keeps the leg but drops apy and base units", func(t *testing.T) {
		rows := mapXoxnoPositions(types.PUBLIC, []wbtypes.XoxnoLendingAccount{xoxnoAccountFixture()}, nil)
		assert.Nil(t, rows[0].NetAPY)
		assert.Nil(t, rows[0].Xoxno.Supply[0].APY)
		assert.Nil(t, rows[0].Xoxno.Supply[0].Symbol)
		assert.Nil(t, rows[0].Xoxno.Supply[0].Tokens, "decimals unknown")
		assert.InDelta(t, 699.5, *rows[0].NetUSD, 1e-9)
	})

	t.Run("empty legs keep non-nil slices", func(t *testing.T) {
		rows := mapXoxnoPositions(types.PUBLIC, []wbtypes.XoxnoLendingAccount{{AccountID: "7"}}, markets)
		require.Len(t, rows, 1)
		assert.NotNil(t, rows[0].Xoxno.Supply)
		assert.NotNil(t, rows[0].Xoxno.Borrow)
		assert.Nil(t, rows[0].NetUSD)
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
		got := baseUnitsString(baseUnits(parseWholeTokens(tc.tokens), &seven, roundDown))
		require.NotNil(t, got, tc.tokens)
		assert.Equal(t, tc.want, *got, tc.tokens)
	}
	assert.Nil(t, baseUnitsString(baseUnits(parseWholeTokens("1"), nil, roundDown)))
	assert.Equal(t, 0, parseWholeTokens("garbage").Sign(), "unparseable is zero")
}

func TestGetAccountsPositionsAssemblesXoxno(t *testing.T) {
	second := xoxnoAccountFixture()
	second.AccountID = "43"
	second.TotalSuppliedUsd, second.TotalBorrowedUsd = f64(100), f64(0)
	second.Positions = []wbtypes.XoxnoLendingPosition{
		{HubID: 2, Asset: "CUSDC", SupplyAmount: "100", BorrowAmount: "0", SupplyUsd: f64(100), BorrowUsd: f64(0)},
	}
	mockWB := &utils.MockWalletBackendService{
		GetXoxnoLendingPositionsResult: []wbtypes.XoxnoLendingAccount{xoxnoAccountFixture(), second},
	}
	svc := NewPositionsService(mockWB, fixedMarkets(xoxnoMarketsFixture()), 10, nil)

	results, err := svc.GetAccountsPositions(context.Background(), []string{xoxnoTestAddress}, types.TESTNET)
	require.NoError(t, err)
	require.Len(t, results, 1)
	got := results[0]
	assert.Equal(t, xoxnoTestAddress, got.Address)
	require.Len(t, got.Positions, 2)
	assert.Equal(t, "xoxno", got.Positions[0].Protocol)
	assert.Equal(t, "42", got.Positions[0].ID)
	assert.Equal(t, "43", got.Positions[1].ID)
	// Header: total 699.5 + 100; APY weighted by supplied:
	// (0.03197×1000 + 0.03×100) / 1100.
	require.NotNil(t, got.TotalValueUSD)
	assert.InDelta(t, 799.5, *got.TotalValueUSD, 1e-9)
	require.NotNil(t, got.NetAPY)
	assert.InDelta(t, (0.03197*1000+0.03*100)/1100, *got.NetAPY, 1e-12)
}

func TestXoxnoDebtRoundingAndUnknownValue(t *testing.T) {
	for _, decimals := range []int32{0, 6, 7, 18, 27} {
		t.Run(fmt.Sprint(decimals), func(t *testing.T) {
			unit := new(big.Rat).SetFrac(big.NewInt(1), new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil))
			fraction := new(big.Rat).Mul(unit, big.NewRat(10001, 10))
			require.Equal(t, "1001", *baseUnitsString(baseUnits(fraction, &decimals, roundUpForDebt)))
			require.Equal(t, "1000", *baseUnitsString(baseUnits(fraction, &decimals, roundDown)))
			require.Equal(t, "1", *baseUnitsString(baseUnits(new(big.Rat), &decimals, roundUpForDebt)))
		})
	}
	a := xoxnoAccountFixture()
	a.TotalBorrowedUsd = nil
	a.Positions[1].BorrowAmount = "0.0000000001"
	rows := mapXoxnoPositions(types.PUBLIC, []wbtypes.XoxnoLendingAccount{a}, indexXoxnoMarkets(xoxnoMarketsFixture()))
	require.Len(t, rows[0].Xoxno.Borrow, 1)
	require.Equal(t, "1", *rows[0].Xoxno.Borrow[0].Tokens)
	require.Nil(t, rows[0].Xoxno.Supply[0].WithdrawableTokens)
	require.Nil(t, rows[0].NetAPY)
	rows = mapXoxnoPositions(types.PUBLIC, []wbtypes.XoxnoLendingAccount{a}, nil)
	require.Len(t, rows[0].Xoxno.Borrow, 1)
	require.Nil(t, rows[0].Xoxno.Borrow[0].Tokens)
	require.Nil(t, rows[0].Xoxno.Supply[0].WithdrawableTokens)
}

func TestXoxnoNonzeroSharesWithZeroDisplayRemainDebt(t *testing.T) {
	a := xoxnoAccountFixture()
	a.TotalBorrowedUsd = nil
	a.Positions[1].BorrowAmount = "0"
	a.Positions[1].BorrowScaledRay = "1"
	a.Positions[1].BorrowUsd = nil
	rows := mapXoxnoPositions(types.PUBLIC, []wbtypes.XoxnoLendingAccount{a}, indexXoxnoMarkets(xoxnoMarketsFixture()))
	require.Len(t, rows[0].Xoxno.Borrow, 1)
	require.Equal(t, "1", *rows[0].Xoxno.Borrow[0].Tokens)
	require.Nil(t, rows[0].Xoxno.Supply[0].WithdrawableTokens)
	require.Nil(t, rows[0].NetAPY)
}

func TestXoxnoContractRoundedDebtRemainder(t *testing.T) {
	// buildAccount returns ceil(1e20*(RAY+1)/RAY), in RAY whole tokens.
	a := xoxnoAccountFixture()
	a.Positions[1].BorrowAmount = "0.000000100000000000000000001"
	rows := mapXoxnoPositions(types.PUBLIC, []wbtypes.XoxnoLendingAccount{a}, indexXoxnoMarkets(xoxnoMarketsFixture()))
	require.Equal(t, "2", *rows[0].Xoxno.Borrow[0].Tokens)
}
