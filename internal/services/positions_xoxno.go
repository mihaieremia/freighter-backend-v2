// ABOUTME: XOXNO lending half of the positions service: maps an account's
// ABOUTME: position NFTs into PoolPosition rows, priced with the market catalog.
package services

import (
	"fmt"
	"math"
	"math/big"

	wbtypes "github.com/stellar/wallet-backend/pkg/wbclient/types"

	"github.com/stellar/freighter-backend-v2/internal/types"
)

type xoxnoMarketKey struct {
	HubID int32
	Asset string
}

func indexXoxnoMarkets(markets []wbtypes.XoxnoLendingMarket) map[xoxnoMarketKey]wbtypes.XoxnoLendingMarket {
	byKey := make(map[xoxnoMarketKey]wbtypes.XoxnoLendingMarket, len(markets))
	for _, m := range markets {
		byKey[xoxnoMarketKey{m.HubID, m.Asset}] = m
	}
	return byKey
}

// mapXoxnoPositions shapes each position NFT into one PoolPosition row. Net
// APY is the position's earnings minus interest over its supplied USD; null
// when any leg is unpriced or its market is unknown.
func mapXoxnoPositions(network string, accounts []wbtypes.XoxnoLendingAccount, markets map[xoxnoMarketKey]wbtypes.XoxnoLendingMarket) []types.PoolPosition {
	rows := make([]types.PoolPosition, 0, len(accounts))
	for _, a := range accounts {
		detail := &types.XoxnoPositionDetail{
			AccountID:    a.AccountID,
			SpokeID:      a.SpokeID,
			PositionMode: a.PositionMode,
			HealthFactor: a.HealthFactor,
			Supply:       []types.XoxnoLegRow{},
			Borrow:       []types.XoxnoLegRow{},
		}
		// supplyBase carries each appended supply row's base units, index-aligned
		// with detail.Supply, so applyWithdrawableTokens never has to parse the
		// wire string back into a number.
		supplyBase := make([]*big.Int, 0, len(a.Positions))
		earn, interest := 0.0, 0.0
		apyKnown := true
		for _, leg := range a.Positions {
			m, hasMarket := markets[xoxnoMarketKey{leg.HubID, leg.Asset}]
			row := types.XoxnoLegRow{
				HubID:   leg.HubID,
				HubName: xoxnoHubName(network, leg.HubID),
				AssetID: leg.Asset,
			}
			if hasMarket {
				row.Symbol, row.Name, row.Decimals, row.PriceUSD = m.TokenSymbol, m.TokenName, m.TokenDecimals, m.PriceUsd
			}
			if supplied := parseWholeTokens(leg.SupplyAmount); supplied.Sign() != 0 {
				supply := row
				held := baseUnits(supplied, row.Decimals, roundDown)
				supply.Tokens, supply.USDValue = baseUnitsString(held), leg.SupplyUsd
				if hasMarket {
					apy := m.SupplyApy
					supply.APY = &apy
				}
				detail.Supply = append(detail.Supply, supply)
				supplyBase = append(supplyBase, held)
				if leg.SupplyUsd == nil || !hasMarket {
					apyKnown = false
				} else {
					earn += *leg.SupplyUsd * m.SupplyApy
				}
			}
			if legHasDebt(leg) {
				borrowed := parseWholeTokens(leg.BorrowAmount)
				borrow := row
				borrow.Tokens, borrow.USDValue = baseUnitsString(baseUnits(borrowed, row.Decimals, roundUpForDebt)), leg.BorrowUsd
				if hasMarket {
					apy := m.BorrowApy
					borrow.APY = &apy
				}
				detail.Borrow = append(detail.Borrow, borrow)
				if leg.BorrowUsd == nil || !hasMarket {
					apyKnown = false
				} else {
					interest += *leg.BorrowUsd * m.BorrowApy
				}
			}
		}
		// The spoke is the position's market identity — its risk parameters
		// and asset list — so it names the row. An unnamed spoke falls back
		// to the account id rather than rendering a bare number.
		detail.SpokeName = xoxnoSpokeName(network, a.SpokeID)
		name := fmt.Sprintf("XOXNO Position #%s", a.AccountID)
		if detail.SpokeName != nil {
			name = *detail.SpokeName
		}
		row := types.PoolPosition{
			Protocol:    "xoxno",
			ID:          a.AccountID,
			Name:        name,
			SuppliedUSD: a.TotalSuppliedUsd,
			BorrowedUSD: a.TotalBorrowedUsd,
			Xoxno:       detail,
		}
		if a.TotalSuppliedUsd != nil && a.TotalBorrowedUsd != nil {
			net := *a.TotalSuppliedUsd - *a.TotalBorrowedUsd
			row.NetUSD = &net
			if apyKnown && *a.TotalSuppliedUsd > 0 {
				apy := (earn - interest) / *a.TotalSuppliedUsd
				if !math.IsInf(apy, 0) && !math.IsNaN(apy) {
					row.NetAPY = &apy
				}
			}
		}
		applyWithdrawableTokens(detail, supplyBase, a, markets)
		rows = append(rows, row)
	}
	return rows
}

// legHasDebt reports whether a leg still owes anything. The scaled ray is
// checked too: a share small enough to round to zero whole tokens is still a
// debt.
func legHasDebt(leg wbtypes.XoxnoLendingPosition) bool {
	return parseWholeTokens(leg.BorrowAmount).Sign() > 0 || parseWholeTokens(leg.BorrowScaledRay).Sign() > 0
}

// parseWholeTokens parses an upstream whole-token decimal string (up to 27
// fractional digits) exactly. Unparseable input is treated as zero so one
// bad leg cannot fail the whole response.
func parseWholeTokens(s string) *big.Rat {
	v, ok := new(big.Rat).SetString(s)
	if !ok {
		return new(big.Rat)
	}
	return v
}

// rounding picks how baseUnits resolves a fractional base unit.
type rounding int

const (
	// roundDown truncates toward zero: the display rule, for a balance.
	roundDown rounding = iota
	// roundUpForDebt takes the ceiling at the asset's decimals and never
	// yields less than one unit for a nonzero share.
	//
	// NOT a display rule, even though the same Tokens field is rendered: this
	// figure seeds a repayment. The contract's `resolve_repay`
	// (`common/src/rates/scaling.rs` in rs-lending-xlm) treats a repayment as
	// closing the debt only once the amount reaches `unscale_borrow_ceil` — the
	// debt rounded UP at the asset's decimals — so a display-rounded figure
	// would leave a dust debt open. The one-unit floor is the same rule at the
	// bottom of the range: a share that is nonzero but rounds to zero for
	// display still owes a unit.
	roundUpForDebt
)

// baseUnits scales a whole-token amount to the asset's base units
// (× 10^decimals), resolving a fractional unit per round. Nil when the
// decimals are unknown.
func baseUnits(tokens *big.Rat, decimals *int32, round rounding) *big.Int {
	if decimals == nil || *decimals < 0 {
		return nil
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(*decimals)), nil)
	scaled := new(big.Rat).Mul(tokens, new(big.Rat).SetInt(scale))
	units, remainder := new(big.Int), new(big.Int)
	units.QuoRem(scaled.Num(), scaled.Denom(), remainder)
	if round == roundUpForDebt {
		if remainder.Sign() > 0 {
			units.Add(units, big.NewInt(1))
		}
		if units.Sign() <= 0 {
			units.SetInt64(1)
		}
	}
	return units
}

// baseUnitsString is a baseUnits result rendered for the wire.
func baseUnitsString(base *big.Int) *string {
	if base == nil {
		return nil
	}
	s := base.String()
	return &s
}

// minBorrowCollateralUSD is the controller's LTV-weighted collateral floor:
// an indebted position may not be left holding less than this. It is protocol
// configuration, not indexed state, and only governance moves it — re-read it
// with the controller's `get_min_borrow_collateral_usd` view (WAD, so 1e18 is
// one dollar) if a withdrawal the UI offered starts being refused.
const minBorrowCollateralUSD = 5.0

// legRiskWeights returns the effective LTV and liquidation threshold the
// controller would value a leg at, both as fractions.
//
// The threshold is the position's own entry stamp: refreshing it is gated on
// the account staying healthy, so a listing change does not reach an existing
// position by itself. The LTV is not — every solvency check restamps listed
// supply legs to the spoke's current listing first, so the listing wins and
// the entry stamp only stands in for an asset the spoke has since delisted.
// The controller then values collateral at the lower of the two, which is
// what this returns.
func legRiskWeights(
	spokeID int32,
	leg wbtypes.XoxnoLendingPosition,
	markets map[xoxnoMarketKey]wbtypes.XoxnoLendingMarket,
) (ltv float64, threshold float64) {
	ltvBps := leg.EntryLoanToValueBps
	if m, ok := markets[xoxnoMarketKey{leg.HubID, leg.Asset}]; ok {
		for _, r := range m.Reserves {
			if r.SpokeID == spokeID {
				ltvBps = r.LoanToValueBps
				break
			}
		}
	}
	threshold = float64(leg.EntryLiquidationThresholdBps) / 10_000
	ltv = float64(ltvBps) / 10_000
	if ltv > threshold {
		ltv = threshold
	}
	return ltv, threshold
}

// applyWithdrawableTokens fills each supply leg's WithdrawableTokens.
//
// After a withdrawal the controller puts an indebted position through three
// gates, and all three have to hold:
//
//   - its LTV-weighted collateral must still cover the debt;
//   - its liquidation-threshold-weighted collateral must too (health >= 1);
//   - its LTV-weighted collateral must still clear the protocol's floor.
//
// Taking `x` USD out of a leg drops the first and third by `x * ltv` and the
// second by `x * threshold`, so each gate bounds the leg on its own and the
// smallest bound is the answer. The two LTV-side gates share a shape and
// collapse into one: the collateral must outlast whichever of the debt and
// the floor is larger.
//
// The LTV gate is the binding one in practice, because the controller values
// collateral at the lower of a leg's LTV and its threshold. Bounding on the
// threshold alone — which is what the health factor shown beside the position
// is made of — names an amount above what the contract will accept, and the
// withdrawal fails at simulation.
//
// This is deliberately the protocol's own rule and not a safety margin on top
// of it. A margin is a product decision that belongs in the UI, which can hold
// some back from what it offers as "max"; reporting less than the truth here
// would instead leave value the API claims is unreachable.
//
// The pool has a gate of its own: every non-liquidation withdrawal must come
// out of the market's cash, so the bound is held down to that too — see
// marketCashBase.
//
// Interest accrues and prices move between this answer and the ledger that
// acts on it, so a leg's real bound drifts by the moment. The client uses the
// contract's own "withdraw everything" path for a full exit rather than this
// figure, and the contract rejects anything it cannot honour, so this bound
// informs the UI without ever being the thing that guards the position.
//
// supplyBase is detail.Supply's base-unit amounts, index-aligned with it.
func applyWithdrawableTokens(
	detail *types.XoxnoPositionDetail,
	supplyBase []*big.Int,
	account wbtypes.XoxnoLendingAccount,
	markets map[xoxnoMarketKey]wbtypes.XoxnoLendingMarket,
) {
	type weights struct{ ltv, threshold float64 }
	byAsset := make(map[xoxnoMarketKey]weights, len(account.Positions))
	for _, leg := range account.Positions {
		ltv, threshold := legRiskWeights(account.SpokeID, leg, markets)
		byAsset[xoxnoMarketKey{leg.HubID, leg.Asset}] = weights{ltv, threshold}
	}

	hasDebt := len(detail.Borrow) > 0
	// Re-derived from the raw legs rather than trusting detail.Borrow: a caller
	// that has not populated it yet would otherwise be offered a full withdrawal
	// on an indebted position.
	for _, leg := range account.Positions {
		hasDebt = hasDebt || legHasDebt(leg)
	}
	if account.TotalBorrowedUsd != nil && *account.TotalBorrowedUsd > 0 {
		hasDebt = true
	}
	if !hasDebt {
		for i := range detail.Supply {
			detail.Supply[i].WithdrawableTokens = withdrawableAtMost(supplyBase[i], detail.Supply[i], markets)
		}
		return
	}
	if account.TotalBorrowedUsd == nil || *account.TotalBorrowedUsd <= 0 || math.IsNaN(*account.TotalBorrowedUsd) || math.IsInf(*account.TotalBorrowedUsd, 0) {
		return
	}
	debt := *account.TotalBorrowedUsd

	ltvCollateral, weighted := 0.0, 0.0
	for _, leg := range detail.Supply {
		if leg.USDValue == nil {
			return // every leg stays null: an unpriced position has no knowable bound
		}
		w := byAsset[xoxnoMarketKey{leg.HubID, leg.AssetID}]
		ltvCollateral += *leg.USDValue * w.ltv
		weighted += *leg.USDValue * w.threshold
	}

	// The floor only applies to a position that still owes something, which
	// is the only branch that reaches here.
	ltvFloor := debt
	if minBorrowCollateralUSD > ltvFloor {
		ltvFloor = minBorrowCollateralUSD
	}
	ltvHeadroom := ltvCollateral - ltvFloor
	healthHeadroom := weighted - debt

	for i, leg := range detail.Supply {
		w := byAsset[xoxnoMarketKey{leg.HubID, leg.AssetID}]
		held := supplyBase[i]
		// The leg's own live USD is the divisor below, so it stands in for a
		// price guard: a leg worth nothing has no bound to express in tokens.
		heldUSD := new(big.Rat).SetFloat64(*leg.USDValue)
		if heldUSD == nil || heldUSD.Sign() <= 0 || w.ltv <= 0 || w.threshold <= 0 || held == nil {
			continue
		}

		if ltvHeadroom <= 0 || healthHeadroom <= 0 {
			zero := "0"
			detail.Supply[i].WithdrawableTokens = &zero
			continue
		}

		// The USD this leg may release under each gate, the tighter one
		// winning.
		releasable := ltvHeadroom / w.ltv
		if byHealth := healthHeadroom / w.threshold; byHealth < releasable {
			releasable = byHealth
		}
		// That USD becomes base units through the leg's own live value rather
		// than PriceUSD, which is the catalog's cached price: the headroom
		// above is built from this request's USD figures, and dividing live
		// USD by a stale price names an amount the contract would refuse.
		// held / USDValue is the same live price, exactly.
		limit := new(big.Rat).SetFloat64(releasable)
		if limit == nil {
			continue
		}
		limit.Quo(limit.Mul(limit, new(big.Rat).SetInt(held)), heldUSD)
		// Truncating is the round-down the bound needs: it may never exceed
		// what the contract would accept.
		scaled := new(big.Int).Quo(limit.Num(), limit.Denom())
		if scaled.Cmp(held) > 0 {
			scaled = held
		}
		detail.Supply[i].WithdrawableTokens = withdrawableAtMost(scaled, leg, markets)
	}
}

// marketCashBase is the most this market could pay out, in the asset's base
// units. Every non-liquidation withdrawal passes `require_reserves`
// (`pool/src/ops/withdraw.rs` gate_and_debit in rs-lending-xlm), which panics
// `InsufficientLiquidity` when the payout is above the market's cash. Cash is
// not indexed, so supplied - borrowed stands in for it.
//
// The pool's other liquidity gate, `require_utilization_below_max`, is not
// covered: max_utilization is not indexed either, and there is nothing to
// derive it from. A market at its utilization ceiling can still refuse a
// withdrawal this bound offers.
//
// Nil when the market is unknown or its figures are unreadable: an unknown
// cash position caps nothing rather than reporting zero.
func marketCashBase(leg types.XoxnoLegRow, markets map[xoxnoMarketKey]wbtypes.XoxnoLendingMarket) *big.Int {
	m, ok := markets[xoxnoMarketKey{leg.HubID, leg.AssetID}]
	if !ok {
		return nil
	}
	supplied, ok := new(big.Rat).SetString(m.Supplied)
	if !ok {
		return nil
	}
	borrowed, ok := new(big.Rat).SetString(m.Borrowed)
	if !ok {
		return nil
	}
	cash := supplied.Sub(supplied, borrowed)
	if cash.Sign() < 0 {
		return new(big.Int)
	}
	return baseUnits(cash, leg.Decimals, roundDown)
}

// withdrawableAtMost renders a leg's bound, held down to the cash its market
// could actually pay out.
func withdrawableAtMost(bound *big.Int, leg types.XoxnoLegRow, markets map[xoxnoMarketKey]wbtypes.XoxnoLendingMarket) *string {
	if bound == nil {
		return nil
	}
	if cash := marketCashBase(leg, markets); cash != nil && cash.Cmp(bound) < 0 {
		bound = cash
	}
	return baseUnitsString(bound)
}
