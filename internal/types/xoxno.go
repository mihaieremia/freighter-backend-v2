// ABOUTME: Response types for XOXNO lending: the earn-options catalog
// ABOUTME: (GET /protocols/xoxno/earn-options) and the per-position detail.
package types

import (
	"context"

	wbtypes "github.com/stellar/wallet-backend/pkg/wbclient/types"
)

// XoxnoPositionDetail is the XOXNO-specific detail for one position NFT row
// in the account positions response. A position lives in one risk spoke and
// holds one supply and one debt leg per (hub, asset).
type XoxnoPositionDetail struct {
	// AccountID is the position NFT token id (the controller's account_id).
	AccountID string `json:"account_id"`
	SpokeID   int32  `json:"spoke_id"`
	// PositionMode: 0 Normal, 1 Multiply, 2 Long, 3 Short.
	PositionMode int32 `json:"position_mode"`
	// HealthFactor is liquidation-threshold-weighted collateral over debt;
	// null with no debt or without prices.
	HealthFactor *float64      `json:"health_factor"`
	Supply       []XoxnoLegRow `json:"supply"`
	Borrow       []XoxnoLegRow `json:"borrow"`
}

// XoxnoLegRow is one asset the position supplies or borrows in a hub.
type XoxnoLegRow struct {
	HubID   int32  `json:"hub_id"`
	AssetID string `json:"asset_id"`
	// Symbol/Name/Decimals are nullable registry metadata from the market.
	Symbol   *string `json:"symbol"`
	Name     *string `json:"name"`
	Decimals *int32  `json:"decimals"`
	// Tokens is the leg's balance as an integer string in the asset's base
	// units, like Blend rows (upstream reports whole tokens; scaled by
	// Decimals here). Null when the token's decimals are unknown.
	Tokens   *string  `json:"tokens"`
	USDValue *float64 `json:"usd_value"`
	// APY is the market's current rate on this side.
	APY      *float64 `json:"apy"`
	PriceUSD *float64 `json:"price_usd"`
}

// XoxnoCatalogService serves the address-independent XOXNO market views:
// the earn catalog (an option per asset, one "pool" per XOXNO hub the asset
// is listed in, with ID "<hubId>:<asset>") and the raw market list the
// positions service prices legs with.
type XoxnoCatalogService interface {
	EarnCatalogService
	GetMarkets(ctx context.Context, network string) ([]wbtypes.XoxnoLendingMarket, error)
}
