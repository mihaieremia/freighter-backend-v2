// ABOUTME: Response types for the XOXNO earn-options endpoint
// ABOUTME: (GET /protocols/xoxno/earn-options).
package types

// EarnOptionsCatalog is the response body for the XOXNO earn-options
// endpoint: "where can I earn this asset", serving the Earn select-token and
// select-pool screens. It is an option per asset, one pool per venue offering
// it; a "pool" is one hub market with ID "<hubId>:<asset>".
type EarnOptionsCatalog struct {
	// Options has one entry per earnable asset. Always non-nil.
	Options []EarnAssetOption `json:"options"`
}

// EarnAssetOption is one earnable asset and the pools offering it.
type EarnAssetOption struct {
	AssetID  string  `json:"asset_id"`
	Symbol   *string `json:"symbol"`
	Name     *string `json:"name"`
	Decimals *int32  `json:"decimals"`
	// Pools is ordered by supplied USD descending (unpriced last).
	Pools []EarnPool `json:"pools"`
}

// EarnPool is one pool's offer for an asset.
type EarnPool struct {
	ID string `json:"id"`
	// Name is the hub's display name; xoxnoHubName always yields one, falling
	// back to the hub id, so this is never empty.
	Name string `json:"name"`
	// SupplyAPY is the market's current supply rate as a fraction. Always
	// known: it comes from the market itself, not from an oracle, unlike
	// SuppliedUSD, which is null when unpriced.
	SupplyAPY   float64  `json:"supply_apy"`
	SuppliedUSD *float64 `json:"supplied_usd"`
	// Spokes are the risk spokes that accept a deposit of this asset, by id.
	//
	// The supply rate belongs to the market, not to the spoke, so every entry
	// here earns the same; they differ in how the deposit may later be
	// borrowed against, which is why the whole list travels rather than one
	// pre-picked answer.
	Spokes []EarnSpoke `json:"spokes,omitempty"`
}

// EarnSpoke is one risk spoke a deposit can be made into.
type EarnSpoke struct {
	ID   int32   `json:"id"`
	Name *string `json:"name"`
	// LoanToValueBps and LiquidationThresholdBps are what actually differ
	// between spokes, and the only grounds a person has for choosing one.
	LoanToValueBps          int32 `json:"loan_to_value_bps"`
	LiquidationThresholdBps int32 `json:"liquidation_threshold_bps"`
}
