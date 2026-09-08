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
// APY is the position's earnings minus interest over its supplied USD, the
// same base Blend's per-pool rate uses; null when any leg is unpriced.
func mapXoxnoPositions(accounts []wbtypes.XoxnoLendingAccount, markets map[xoxnoMarketKey]wbtypes.XoxnoLendingMarket) []types.PoolPosition {
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
		earn, interest := 0.0, 0.0
		apyKnown := true
		for _, leg := range a.Positions {
			m, hasMarket := markets[xoxnoMarketKey{leg.HubID, leg.Asset}]
			row := types.XoxnoLegRow{HubID: leg.HubID, AssetID: leg.Asset}
			if hasMarket {
				row.Symbol, row.Name, row.Decimals, row.PriceUSD = m.TokenSymbol, m.TokenName, m.TokenDecimals, m.PriceUsd
			}
			if supplied := parseWholeTokens(leg.SupplyAmount); supplied.Sign() != 0 {
				supply := row
				supply.Tokens, supply.USDValue = toBaseUnits(supplied, row.Decimals), leg.SupplyUsd
				if hasMarket {
					apy := m.SupplyApy
					supply.APY = &apy
				}
				detail.Supply = append(detail.Supply, supply)
				if leg.SupplyUsd == nil || !hasMarket {
					apyKnown = false
				} else {
					earn += *leg.SupplyUsd * m.SupplyApy
				}
			}
			if borrowed := parseWholeTokens(leg.BorrowAmount); borrowed.Sign() != 0 {
				borrow := row
				borrow.Tokens, borrow.USDValue = toBaseUnits(borrowed, row.Decimals), leg.BorrowUsd
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
		name := fmt.Sprintf("XOXNO Position #%s", a.AccountID)
		row := types.PoolPosition{
			Protocol:    "xoxno",
			ID:          a.AccountID,
			Name:        &name,
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
		rows = append(rows, row)
	}
	return rows
}

// parseWholeTokens parses an upstream whole-token decimal string (up to 27
// fractional digits) exactly. Unparseable input is zero, like parseRawAmount:
// one bad leg must not fail the whole response.
func parseWholeTokens(s string) *big.Rat {
	v, ok := new(big.Rat).SetString(s)
	if !ok {
		return new(big.Rat)
	}
	return v
}

// toBaseUnits scales a whole-token amount to the asset's base units
// (× 10^decimals) as an integer string, truncating toward zero. Nil when the
// decimals are unknown.
func toBaseUnits(tokens *big.Rat, decimals *int32) *string {
	if decimals == nil || *decimals < 0 {
		return nil
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(*decimals)), nil)
	scaled := new(big.Rat).Mul(tokens, new(big.Rat).SetInt(scale))
	base := new(big.Int).Quo(scaled.Num(), scaled.Denom()).String()
	return &base
}
