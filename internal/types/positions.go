// ABOUTME: Response types for POST /api/v1/accounts/positions — the
// ABOUTME: frontend-shaped view of an account's DeFi lending positions.
package types

// AccountPositions is the response body for the account positions endpoint.
// One payload powers both the Position Home screen (header + per-pool rows)
// and the Position Details screen (per-asset rows inside each pool): an
// account holds positions in at most a handful of pools, so full detail is
// always returned.
//
// Number conventions follow the wallet-backend upstream: USD/APY values are
// nullable JSON numbers where null means "unavailable" (no fresh oracle
// price), never zero; a genuinely zero value is 0. On-chain token amounts
// are full-precision integer strings in the asset's smallest unit (scale by
// Decimals for display).
type AccountPositions struct {
	// Address is the account this entry describes; one entry per requested
	// address, in first-seen request order (duplicates collapsed).
	Address string `json:"address"`
	// TotalValueUSD is the account's net position value across pools
	// (Σ pool NetUSD). Strict null propagation: if any pool's value is
	// unavailable the total is null rather than a silent undercount —
	// matching upstream's own convention for pool totals. 0 when the
	// account has no positions.
	TotalValueUSD *float64 `json:"total_value_usd"`
	// NetAPY is the supplied-USD-weighted mean of the pools' net APYs
	// (matching the base the per-pool rate is defined over); null when any
	// input is unavailable or there is no supplied value to weight.
	NetAPY *float64 `json:"net_apy"`
	// Positions has one row per (protocol, pool). Always non-nil; empty when
	// the account has no DeFi positions (including accounts unknown to the
	// indexer — indistinguishable by design).
	Positions []PoolPosition `json:"positions"`
}

// PoolPosition is one pool row. The common fields render a Position Home row
// for any protocol; protocol-specific detail lives under a key named after
// the protocol ("xoxno"), so adding a protocol is additive.
type PoolPosition struct {
	Protocol string `json:"protocol"`
	// ID is the position's identifier within the protocol: the position NFT
	// token id for XOXNO.
	ID string `json:"id"`
	// Name is the pool's display name. Always set: an unnamed spoke falls back
	// to "XOXNO Position #<id>" rather than travelling empty.
	Name string `json:"name"`
	// NetUSD is supplied minus borrowed for this pool.
	NetUSD      *float64 `json:"net_usd"`
	SuppliedUSD *float64 `json:"supplied_usd"`
	BorrowedUSD *float64 `json:"borrowed_usd"`
	// NetAPY is the account's net rate in this pool: supply earnings minus
	// borrow interest over TOTAL SUPPLIED USD.
	NetAPY *float64             `json:"net_apy"`
	Xoxno  *XoxnoPositionDetail `json:"xoxno,omitempty"`
}
