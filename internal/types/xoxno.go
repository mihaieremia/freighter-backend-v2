// ABOUTME: Response types for XOXNO lending: the per-position detail.
package types

// XoxnoPositionDetail is the XOXNO-specific detail for one position NFT row
// in the account positions response. A position lives in one risk spoke and
// holds one supply and one debt leg per (hub, asset).
type XoxnoPositionDetail struct {
	// AccountID is the position NFT token id (the controller's account_id).
	AccountID string `json:"account_id"`
	SpokeID   int32  `json:"spoke_id"`
	// SpokeName is the spoke's display name; null for a spoke this build does
	// not know, which happens when governance adds one.
	SpokeName *string `json:"spoke_name"`
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
	HubID int32 `json:"hub_id"`
	// HubName is the liquidity hub's display name. A position lives in one
	// spoke but its legs can sit in different hubs, so the name belongs to
	// the leg rather than to the position around it.
	HubName string `json:"hub_name"`
	AssetID string `json:"asset_id"`
	// Symbol/Name/Decimals are nullable registry metadata from the market.
	Symbol   *string `json:"symbol"`
	Name     *string `json:"name"`
	Decimals *int32  `json:"decimals"`
	// Tokens is the leg's balance as an integer string in the asset's base
	// units (supply rounded down, debt rounded up). Null
	// when the token's decimals are unknown.
	Tokens   *string  `json:"tokens"`
	USDValue *float64 `json:"usd_value"`
	// WithdrawableTokens is how much of a SUPPLY leg can leave the position
	// without pushing it under water, in the asset's base units. It equals
	// Tokens for a position carrying no debt. Null when the leg or the
	// position cannot be priced, in which case no safe bound is knowable and
	// withdrawals must wait for valuation. Always null on a
	// borrow leg, which is a debt rather than a balance.
	WithdrawableTokens *string `json:"withdrawable_tokens"`
	// APY is the market's current rate on this side.
	APY      *float64 `json:"apy"`
	PriceUSD *float64 `json:"price_usd"`
}
