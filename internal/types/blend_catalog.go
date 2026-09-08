// ABOUTME: Response types for the market-catalog endpoints: the Blend pools
// ABOUTME: view and the protocol-neutral earn-options view (Blend, XOXNO).
package types

import "context"

// BlendPoolsCatalog is the response body for the pools endpoint: the
// pool-wide market view (no account data), serving the Pool Details screen.
// Number conventions match the positions endpoint: USD/APY are nullable
// JSON numbers (null = no fresh oracle price), token metadata is nullable
// registry data.
type BlendPoolsCatalog struct {
	// Pools is every Blend pool known to the indexer, unfiltered. Always
	// non-nil.
	Pools []BlendCatalogPool `json:"pools"`
}

// BlendCatalogPool is one pool in the market catalog.
type BlendCatalogPool struct {
	// ID is the pool's contract address.
	ID string `json:"id"`
	// Name is null when the metadata registry has no entry.
	Name *string `json:"name"`
	// Status is the pool's operational status as the upstream enum name
	// (ADMIN_ACTIVE, ACTIVE, ADMIN_ON_ICE, ON_ICE, ADMIN_FROZEN, FROZEN,
	// SETUP; the first four accept deposits, the first two also allow
	// borrowing). Null until the pool's config has been ingested.
	Status *string `json:"status"`
	// SuppliedUSD/BorrowedUSD are pool-wide totals with strict null
	// propagation upstream: one unpriced reserve nulls the pool total.
	SuppliedUSD *float64 `json:"supplied_usd"`
	BorrowedUSD *float64 `json:"borrowed_usd"`
	// InterestAPY is the supplied-USD-weighted supply rate (interest only).
	// NetAPY additionally includes BLND emissions; it is a supply-side
	// yield, not netted against the pool's borrow side.
	InterestAPY *float64 `json:"interest_apy"`
	NetAPY      *float64 `json:"net_apy"`
	// BackstopUSD is the pool's backstop-LP balance, priced at the Comet LP
	// rate — the first-loss capital backing this pool, not a user's deposit.
	BackstopUSD *float64 `json:"backstop_usd"`
	// BackstopRate is the share of borrower interest routed to the backstop,
	// as 7-decimal fixed point (4750000 = 47.5%). Null until observed.
	BackstopRate *int32 `json:"backstop_rate"`
	// InRewardZone reports whether the pool is in the backstop's reward zone
	// and therefore earns BLND emissions.
	InRewardZone bool `json:"in_reward_zone"`
	// Reserves lists the pool's assets with current market rates.
	Reserves []BlendCatalogReserve `json:"reserves"`
}

// BlendCatalogReserve is one (pool, asset) market row.
type BlendCatalogReserve struct {
	AssetID  string  `json:"asset_id"`
	Symbol   *string `json:"symbol"`
	Name     *string `json:"name"`
	Decimals *int32  `json:"decimals"`
	// Enabled is the reserve's own on/off flag, independent of pool status.
	Enabled bool `json:"enabled"`
	// Utilization is borrowed/supplied, clamped at 100% upstream.
	Utilization *float64 `json:"utilization"`
	SupplyAPY   *float64 `json:"supply_apy"`
	BorrowAPY   *float64 `json:"borrow_apy"`
	// EmissionsSupplyAPR is the BLND emission rate on the supply side.
	EmissionsSupplyAPR *float64 `json:"emissions_supply_apr"`
	SuppliedUSD        *float64 `json:"supplied_usd"`
	BorrowedUSD        *float64 `json:"borrowed_usd"`
	PriceUSD           *float64 `json:"price_usd"`
}

// EarnOptionsCatalog is the response body for the per-protocol earn-options
// endpoints: "where can I earn this asset", serving the Earn select-token and
// select-pool screens. Every protocol renders the same shape: an option per
// asset, one pool per venue offering it. For Blend it is derived from the
// pools catalog (disabled reserves and deposit-rejecting pools excluded,
// pools filtered through the operator allowlist when configured); for XOXNO
// a "pool" is one hub market with ID "<hubId>:<asset>".
type EarnOptionsCatalog struct {
	// Options has one entry per earnable asset. Always non-nil; assets whose
	// every pool was removed by the allowlist are dropped.
	Options []EarnAssetOption `json:"options"`
}

// EarnAssetOption is one earnable asset and the pools offering it.
type EarnAssetOption struct {
	AssetID  string  `json:"asset_id"`
	Symbol   *string `json:"symbol"`
	Name     *string `json:"name"`
	Decimals *int32  `json:"decimals"`
	// Pools is ordered by supplied USD descending (unpriced last). The
	// emissions-inclusive earn headline is SupplyAPY + EmissionsSupplyAPR.
	Pools []EarnPool `json:"pools"`
}

// EarnPool is one pool's offer for an asset.
type EarnPool struct {
	ID                 string   `json:"id"`
	Name               *string  `json:"name"`
	SupplyAPY          *float64 `json:"supply_apy"`
	EmissionsSupplyAPR *float64 `json:"emissions_supply_apr"`
	SuppliedUSD        *float64 `json:"supplied_usd"`
}

// EarnCatalogService serves a protocol's address-independent earn view.
type EarnCatalogService interface {
	Service
	GetEarnOptions(ctx context.Context, network string) (*EarnOptionsCatalog, error)
}

// BlendCatalogService serves the address-independent Blend market views.
type BlendCatalogService interface {
	EarnCatalogService
	GetPools(ctx context.Context, network string) (*BlendPoolsCatalog, error)
}
