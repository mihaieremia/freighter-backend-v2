package swap

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stellar/freighter-backend-v2/internal/types"
)

// pricesFixture is the aggregator's /prices: FREE (no list price) is deep
// enough to fall back on, THIN (also unpriced by the list) is one dollar short,
// and USDC has a deep price that must lose to the list's.
func pricesFixture() map[string]aggregatorPrice {
	return map[string]aggregatorPrice{
		tokUSDC:          {USD: 9, DepthUSD: 1e6},
		testContract(8):  {USD: 0.5, DepthUSD: minFallbackPriceDepthUSD},
		testContract(10): {USD: 0.7, DepthUSD: minFallbackPriceDepthUSD - 1},
	}
}

func catalogByID(tokens []types.CatalogToken) map[string]types.CatalogToken {
	out := map[string]types.CatalogToken{}
	for _, t := range tokens {
		out[t.ID] = t
	}
	return out
}

func TestTokenCatalog_Contents(t *testing.T) {
	t.Parallel()
	svc, expert := newTokensService(newTokensStub(t), time.Minute)
	tokens, err := svc.GetTokenCatalog(context.Background(), types.PUBLIC)
	require.NoError(t, err)
	got := catalogByID(tokens)

	t.Run("lists every registered token except LP, self-ticker and malformed", func(t *testing.T) {
		assert.Len(t, tokens, 8, "11 listed minus the LP token, the self-ticker entry and the malformed id")
		assert.Len(t, got, 8)
		assert.NotContains(t, got, testContract(5), "LP token")
		assert.NotContains(t, got, testContract(9), "ticker equals its identifier")
		assert.NotContains(t, got, "CNOTACONTRACTID")
		assert.Zero(t, expert.ContractCallCount(tokUSDC), "the catalog never asks Stellar Expert")
		assert.Equal(t, types.CatalogToken{
			ID: tokUSDC, Code: "USDC", Name: "USD Coin", Decimals: 7, IconURL: "https://media/usdc.png", PriceUSD: 1.00002, Swappable: true,
		}, got[tokUSDC])
	})

	t.Run("swappable is listed, routable with matching decimals", func(t *testing.T) {
		assert.True(t, got[tokSolv].Swappable)
		assert.False(t, got[testContract(4)].Swappable, "not routable")
		assert.False(t, got[testContract(6)].Swappable, "not swap-listed")
		assert.False(t, got[testContract(7)].Swappable, "decimals disagree with the aggregator")
		assert.True(t, got[testContract(8)].Swappable, "an unpriced token can still be swappable")
	})

	t.Run("price falls back to the aggregator only at the depth floor", func(t *testing.T) {
		assert.InDelta(t, 1.00002, got[tokUSDC].PriceUSD, 0, "the list price wins over a deeper aggregator price")
		assert.InDelta(t, 0.5, got[testContract(8)].PriceUSD, 0, "at the floor the aggregator price is used")
		assert.Zero(t, got[testContract(10)].PriceUSD, "below the floor the price is omitted")
		assert.InDelta(t, 1, got[testContract(7)].PriceUSD, 0, "a token with a list price keeps it")
	})
}

func TestTokenCatalog_HasItsOwnCacheEntry(t *testing.T) {
	t.Parallel()
	st := newTokensStub(t)
	svc, _ := newTokensService(st, time.Minute)

	_, err := svc.GetTokenCatalog(context.Background(), types.PUBLIC)
	require.NoError(t, err)
	calls := st.listCalls.Load()
	_, err = svc.GetTokenCatalog(context.Background(), types.PUBLIC)
	require.NoError(t, err)
	assert.Equal(t, int32(1), st.priceCalls.Load())

	swapTokens, err := svc.GetSwapTokens(context.Background(), types.PUBLIC)
	require.NoError(t, err)
	assert.Len(t, swapTokens, 3, "the default list is unaffected and has its own cache entry")
	assert.Greater(t, st.listCalls.Load(), calls)
}

func TestTokenCatalog_AFailedPriceFetchServesTheListUncachedAndKeepsAnOlderCompleteCatalog(t *testing.T) {
	t.Parallel()
	st := newTokensStub(t)
	st.pricesFail.Store(true)
	svc, _ := newTokensService(st, time.Minute)
	now := time.Unix(1_700_000_000, 0)
	svc.now = func() time.Time { return now }

	degraded, err := svc.GetTokenCatalog(context.Background(), types.PUBLIC)
	require.NoError(t, err)
	assert.Len(t, degraded, 8, "prices only back up the list, so their loss does not fail the catalog")
	assert.Zero(t, catalogByID(degraded)[testContract(8)].PriceUSD)

	st.pricesFail.Store(false)
	full, err := svc.GetTokenCatalog(context.Background(), types.PUBLIC)
	require.NoError(t, err)
	assert.InDelta(t, 0.5, catalogByID(full)[testContract(8)].PriceUSD, 0, "the degraded catalog was not kept")

	now = now.Add(2 * time.Minute)
	st.pricesFail.Store(true)
	stale, err := svc.GetTokenCatalog(context.Background(), types.PUBLIC)
	require.NoError(t, err)
	assert.Equal(t, full, stale, "a complete catalog beats a degraded refresh")
}

func TestTokenCatalog_IsBoundedAndListsEachTokenOnce(t *testing.T) {
	t.Parallel()
	listed := make([]listedToken, 0, maxCatalogTokens+12)
	listed = append(listed, listedToken{Identifier: tokUSDC, Ticker: "USDC"}, listedToken{Identifier: tokUSDC, Ticker: "USDC"})
	for i := range maxCatalogTokens + 10 {
		listed = append(listed, listedToken{Identifier: testContractN(i), Ticker: "T"})
	}
	got := catalogRows(listed, nil, nil)
	assert.Len(t, got, maxCatalogTokens)
	ids := map[string]struct{}{}
	for _, r := range got {
		ids[r.id] = struct{}{}
	}
	assert.Len(t, ids, len(got), "no id appears twice")
}
