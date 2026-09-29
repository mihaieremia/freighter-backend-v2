package types

// Swap token kinds. The wire values are part of the client contract.
const (
	SwapTokenNative  = "native"
	SwapTokenClassic = "classic"
	SwapTokenSoroban = "soroban"
)

// SwapToken is a token XOXNO lists for swapping that the aggregator can route.
type SwapToken struct {
	// ID is the token's contract id. A classic asset is named by its Stellar Asset
	// Contract.
	ID string `json:"id"`
	// Kind is native, classic, or soroban. A classic asset is swapped as one (it
	// needs a trustline); a Soroban token is not.
	Kind string `json:"kind"`
	// Asset is the "CODE:ISSUER" of a classic asset.
	Asset    string  `json:"asset,omitempty"`
	Code     string  `json:"code"`
	Name     string  `json:"name"`
	Decimals int     `json:"decimals"`
	IconURL  string  `json:"iconUrl,omitempty"`
	PriceUSD float64 `json:"priceUsd"`
}

// CatalogToken is an entry of the XOXNO token catalog: every registered token
// with its logo and, when a trustworthy one is known, its USD price. Whether a
// token is a classic asset is not said; it follows from the contract id.
type CatalogToken struct {
	ID       string `json:"id"`
	Code     string `json:"code"`
	Name     string `json:"name"`
	Decimals int    `json:"decimals"`
	IconURL  string `json:"iconUrl,omitempty"`
	// PriceUSD is 0 when no price backed by enough liquidity is known.
	PriceUSD float64 `json:"priceUsd"`
	// Swappable is true when the aggregator can route the token.
	Swappable bool `json:"swappable"`
}
