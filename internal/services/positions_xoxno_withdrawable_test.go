// ABOUTME: Tests for the withdrawable bound on a XOXNO supply leg: the
// ABOUTME: controller's LTV, health and collateral-floor gates, and rounding.
package services

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stellar/freighter-backend-v2/internal/types"
	wbtypes "github.com/stellar/wallet-backend/pkg/wbclient/types"
)

func ptrF(v float64) *float64 { return &v }
func ptrS(v string) *string   { return &v }
func ptrI(v int32) *int32     { return &v }

// supplyBaseUnits stands in for the base units mapXoxnoPositions carries
// alongside the supply rows it builds.
func supplyBaseUnits(d *types.XoxnoPositionDetail) []*big.Int {
	base := make([]*big.Int, len(d.Supply))
	for i, leg := range d.Supply {
		if leg.Tokens != nil {
			base[i], _ = new(big.Int).SetString(*leg.Tokens, 10)
		}
	}
	return base
}

func TestApplyWithdrawableTokens(t *testing.T) {
	// One leg worth 100 USD: 100 tokens at 7 decimals, a dollar each.
	leg := func(usd float64, tokens string) types.XoxnoLegRow {
		return types.XoxnoLegRow{
			HubID: 1, AssetID: "CUSDC", Decimals: ptrI(7),
			Tokens: ptrS(tokens), USDValue: ptrF(usd), PriceUSD: ptrF(1),
		}
	}
	// Borrowing against it at a 75% LTV and an 80% liquidation threshold —
	// the gap between the two is what the contract's gates turn on.
	account := func(borrowed float64) wbtypes.XoxnoLendingAccount {
		return wbtypes.XoxnoLendingAccount{
			SpokeID:          1,
			TotalBorrowedUsd: &borrowed,
			Positions: []wbtypes.XoxnoLendingPosition{
				{
					HubID: 1, Asset: "CUSDC",
					EntryLoanToValueBps:          7500,
					EntryLiquidationThresholdBps: 8000,
				},
			},
		}
	}

	t.Run("a debt-free position can give back everything", func(t *testing.T) {
		d := &types.XoxnoPositionDetail{Supply: []types.XoxnoLegRow{leg(100, "1000000000")}}
		applyWithdrawableTokens(d, supplyBaseUnits(d), account(0), nil)
		require.NotNil(t, d.Supply[0].WithdrawableTokens)
		assert.Equal(t, "1000000000", *d.Supply[0].WithdrawableTokens)
	})

	t.Run("debt-free withdrawals need no USD prices or totals", func(t *testing.T) {
		row := leg(100, "1000000000")
		row.USDValue, row.PriceUSD = nil, nil
		d := &types.XoxnoPositionDetail{Supply: []types.XoxnoLegRow{row}}
		a := account(0)
		a.TotalBorrowedUsd, a.TotalSuppliedUsd = nil, nil
		applyWithdrawableTokens(d, supplyBaseUnits(d), a, nil)
		require.Equal(t, row.Tokens, d.Supply[0].WithdrawableTokens)
	})

	t.Run("the LTV gate binds before the health factor does", func(t *testing.T) {
		// 100 USDC carries 75 of borrowing power and 80 of liquidation
		// cover. Against a debt of 40 the health factor would release
		// (80-40)/0.8 = 50 USDC, but borrowing power only allows
		// (75-40)/0.75 = 46.67 — and that is the one the contract checks
		// first. Bounding on the health factor is what made "max" fail.
		d := &types.XoxnoPositionDetail{Supply: []types.XoxnoLegRow{leg(100, "1000000000")}}
		applyWithdrawableTokens(d, supplyBaseUnits(d), account(40), nil)
		require.NotNil(t, d.Supply[0].WithdrawableTokens)
		assert.Equal(t, "466666666", *d.Supply[0].WithdrawableTokens)
	})

	t.Run("the collateral floor binds when the debt is smaller than it", func(t *testing.T) {
		// A dollar of debt still leaves the position owing something, so it
		// must keep the protocol's 5 USD of borrowing power: (75-5)/0.75 =
		// 93.33 USDC, not the (75-1)/0.75 = 98.67 the debt alone allows.
		d := &types.XoxnoPositionDetail{Supply: []types.XoxnoLegRow{leg(100, "1000000000")}}
		applyWithdrawableTokens(d, supplyBaseUnits(d), account(1), nil)
		require.NotNil(t, d.Supply[0].WithdrawableTokens)
		assert.Equal(t, "933333333", *d.Supply[0].WithdrawableTokens)
	})

	t.Run("the spoke's current listing outranks the leg's entry LTV", func(t *testing.T) {
		// Every solvency check restamps a listed leg's LTV first, so a
		// listing tightened since the deposit is what the contract values
		// the collateral at: (50-40)/0.5 = 20 USDC.
		markets := map[xoxnoMarketKey]wbtypes.XoxnoLendingMarket{
			{HubID: 1, Asset: "CUSDC"}: {
				HubID: 1, Asset: "CUSDC",
				Reserves: []wbtypes.XoxnoLendingReserve{
					{SpokeID: 1, LoanToValueBps: 5000, LiquidationThresholdBps: 8000},
				},
			},
		}
		d := &types.XoxnoPositionDetail{Supply: []types.XoxnoLegRow{leg(100, "1000000000")}}
		applyWithdrawableTokens(d, supplyBaseUnits(d), account(40), markets)
		require.NotNil(t, d.Supply[0].WithdrawableTokens)
		assert.Equal(t, "200000000", *d.Supply[0].WithdrawableTokens)
	})

	t.Run("a listing above the threshold is valued at the threshold", func(t *testing.T) {
		// The contract values collateral at the lower of the two, so an LTV
		// raised past the position's own liquidation threshold releases no
		// more than the threshold would: (80-40)/0.8 = 50 USDC.
		markets := map[xoxnoMarketKey]wbtypes.XoxnoLendingMarket{
			{HubID: 1, Asset: "CUSDC"}: {
				HubID: 1, Asset: "CUSDC",
				Reserves: []wbtypes.XoxnoLendingReserve{
					{SpokeID: 1, LoanToValueBps: 9500, LiquidationThresholdBps: 8000},
				},
			},
		}
		d := &types.XoxnoPositionDetail{Supply: []types.XoxnoLegRow{leg(100, "1000000000")}}
		applyWithdrawableTokens(d, supplyBaseUnits(d), account(40), markets)
		require.NotNil(t, d.Supply[0].WithdrawableTokens)
		assert.Equal(t, "500000000", *d.Supply[0].WithdrawableTokens)
	})

	t.Run("another spoke's listing is not this position's", func(t *testing.T) {
		// The reserve rows carry every spoke that lists the asset; only the
		// position's own spoke says anything about this position.
		markets := map[xoxnoMarketKey]wbtypes.XoxnoLendingMarket{
			{HubID: 1, Asset: "CUSDC"}: {
				HubID: 1, Asset: "CUSDC",
				Reserves: []wbtypes.XoxnoLendingReserve{
					{SpokeID: 2, LoanToValueBps: 5000, LiquidationThresholdBps: 8000},
				},
			},
		}
		d := &types.XoxnoPositionDetail{Supply: []types.XoxnoLegRow{leg(100, "1000000000")}}
		applyWithdrawableTokens(d, supplyBaseUnits(d), account(40), markets)
		require.NotNil(t, d.Supply[0].WithdrawableTokens)
		assert.Equal(t, "466666666", *d.Supply[0].WithdrawableTokens)
	})

	t.Run("an underwater position can give back nothing", func(t *testing.T) {
		d := &types.XoxnoPositionDetail{Supply: []types.XoxnoLegRow{leg(100, "1000000000")}}
		applyWithdrawableTokens(d, supplyBaseUnits(d), account(90), nil)
		require.NotNil(t, d.Supply[0].WithdrawableTokens)
		assert.Equal(t, "0", *d.Supply[0].WithdrawableTokens)
	})

	t.Run("a position with no borrowing power left can give back nothing", func(t *testing.T) {
		// Debt of 76 is under the 80 of liquidation cover — the health
		// factor is still above 1 — but over the 75 of borrowing power, so
		// the contract would refuse any withdrawal at all.
		d := &types.XoxnoPositionDetail{Supply: []types.XoxnoLegRow{leg(100, "1000000000")}}
		applyWithdrawableTokens(d, supplyBaseUnits(d), account(76), nil)
		require.NotNil(t, d.Supply[0].WithdrawableTokens)
		assert.Equal(t, "0", *d.Supply[0].WithdrawableTokens)
	})

	t.Run("never offers more than the leg holds", func(t *testing.T) {
		// A second leg carries most of the cover, so the headroom alone
		// would release more of the first leg than it contains. The answer
		// is what the leg holds, not what the headroom would allow.
		other := leg(1000, "10000000000")
		other.AssetID = "CXLM"
		d := &types.XoxnoPositionDetail{
			Supply: []types.XoxnoLegRow{leg(100, "1000000000"), other},
		}
		acct := account(40)
		acct.Positions = append(acct.Positions, wbtypes.XoxnoLendingPosition{
			HubID: 1, Asset: "CXLM",
			EntryLoanToValueBps:          7500,
			EntryLiquidationThresholdBps: 8000,
		})

		applyWithdrawableTokens(d, supplyBaseUnits(d), acct, nil)

		require.NotNil(t, d.Supply[0].WithdrawableTokens)
		assert.Equal(t, "1000000000", *d.Supply[0].WithdrawableTokens)
	})

	t.Run("rounds down, never up, so the bound cannot exceed the truth", func(t *testing.T) {
		// The floor holds back 5 of the 75: (75-5)/0.75 = 93.3333... USDC,
		// which has no exact base-unit form. A bound that rounded up would
		// name an amount the contract would refuse.
		d := &types.XoxnoPositionDetail{Supply: []types.XoxnoLegRow{leg(100, "1000000000")}}
		applyWithdrawableTokens(d, supplyBaseUnits(d), account(0.01), nil)
		require.NotNil(t, d.Supply[0].WithdrawableTokens)
		assert.Equal(t, "933333333", *d.Supply[0].WithdrawableTokens)
	})

	t.Run("a price that moved since the catalog was cached does not inflate the bound", func(t *testing.T) {
		// 1000 XLM the catalog priced at 0.400 and the position values at
		// 0.408: the headroom is built from the live 408 USD, so turning it
		// back into tokens at the cached price would offer 353.33 XLM against
		// a true bound of 141.33/0.408 = 346.4, and the withdrawal would leave
		// the position under water.
		row := types.XoxnoLegRow{
			HubID: 1, AssetID: "CXLM", Decimals: ptrI(7),
			Tokens: ptrS("10000000000"), USDValue: ptrF(408), PriceUSD: ptrF(0.4),
		}
		d := &types.XoxnoPositionDetail{Supply: []types.XoxnoLegRow{row}}
		acct := account(200)
		acct.Positions[0].Asset = "CXLM"

		applyWithdrawableTokens(d, supplyBaseUnits(d), acct, nil)

		require.NotNil(t, d.Supply[0].WithdrawableTokens)
		assert.Equal(t, "3464052287", *d.Supply[0].WithdrawableTokens)
	})

	t.Run("never offers more than the market can pay out", func(t *testing.T) {
		// The pool debits the payout from its cash, so a market that has lent
		// out all but 40 of its 1000 tokens cannot honour more than that,
		// however healthy the position is.
		markets := map[xoxnoMarketKey]wbtypes.XoxnoLendingMarket{
			{HubID: 1, Asset: "CUSDC"}: {
				HubID: 1, Asset: "CUSDC", Supplied: "1000", Borrowed: "960",
			},
		}

		debtFree := &types.XoxnoPositionDetail{Supply: []types.XoxnoLegRow{leg(100, "1000000000")}}
		applyWithdrawableTokens(debtFree, supplyBaseUnits(debtFree), account(0), markets)
		require.NotNil(t, debtFree.Supply[0].WithdrawableTokens)
		assert.Equal(t, "400000000", *debtFree.Supply[0].WithdrawableTokens, "the whole balance is not withdrawable")

		// The risk gates alone would release 46.67 USDC here.
		indebted := &types.XoxnoPositionDetail{Supply: []types.XoxnoLegRow{leg(100, "1000000000")}}
		applyWithdrawableTokens(indebted, supplyBaseUnits(indebted), account(40), markets)
		require.NotNil(t, indebted.Supply[0].WithdrawableTokens)
		assert.Equal(t, "400000000", *indebted.Supply[0].WithdrawableTokens)
	})

	t.Run("reports nothing rather than a guess for an unpriced position", func(t *testing.T) {
		unpriced := leg(0, "1000000000")
		unpriced.USDValue = nil
		d := &types.XoxnoPositionDetail{Supply: []types.XoxnoLegRow{unpriced}}
		applyWithdrawableTokens(d, supplyBaseUnits(d), account(40), nil)
		assert.Nil(t, d.Supply[0].WithdrawableTokens)
	})
}
