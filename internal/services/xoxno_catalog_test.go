// ABOUTME: Tests for the XOXNO catalog service: earn-option derivation from
// ABOUTME: the market list and the cache-less read path.
package services

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wbtypes "github.com/stellar/wallet-backend/pkg/wbclient/types"

	"github.com/stellar/freighter-backend-v2/internal/types"
	"github.com/stellar/freighter-backend-v2/internal/utils"
)

func f64(v float64) *float64 { return &v }

func xoxnoMarketsFixture() []wbtypes.XoxnoLendingMarket {
	usdc, xlm := "USDC", "XLM"
	seven := int32(7)
	return []wbtypes.XoxnoLendingMarket{
		{HubID: 2, Asset: "CUSDC", TokenSymbol: &usdc, TokenDecimals: &seven, SupplyApy: 0.03, BorrowApy: 0.06, SuppliedUsd: f64(500), PriceUsd: f64(1),
			Reserves: []wbtypes.XoxnoLendingReserve{{SpokeID: 1, IsCollateralizable: true}}},
		{HubID: 1, Asset: "CUSDC", TokenSymbol: &usdc, TokenDecimals: &seven, SupplyApy: 0.05, BorrowApy: 0.08, SuppliedUsd: f64(1000), PriceUsd: f64(1),
			Reserves: []wbtypes.XoxnoLendingReserve{{SpokeID: 1, IsCollateralizable: true, Paused: true}, {SpokeID: 2, IsCollateralizable: true}}},
		{HubID: 1, Asset: "CXLM", TokenSymbol: &xlm, TokenDecimals: &seven, SupplyApy: 0.01, BorrowApy: 0.02, SuppliedUsd: nil, PriceUsd: nil,
			Reserves: []wbtypes.XoxnoLendingReserve{{SpokeID: 1, IsCollateralizable: true}}},
		{HubID: 3, Asset: "CFROZEN", SupplyApy: 0.9, SuppliedUsd: f64(9),
			Reserves: []wbtypes.XoxnoLendingReserve{{SpokeID: 1, IsCollateralizable: true, Frozen: true}}}, // no open spoke: excluded
		{HubID: 3, Asset: "CUNLISTED", SupplyApy: 0.9, SuppliedUsd: f64(9)}, // no spoke at all: excluded
	}
}

func TestXoxnoEarnOptionsDerivation(t *testing.T) {
	svc := NewXoxnoCatalogService(&utils.MockWalletBackendService{GetXoxnoLendingMarketsResult: xoxnoMarketsFixture()}, nil, 0, nil)

	got, err := svc.GetEarnOptions(context.Background(), types.TESTNET)
	require.NoError(t, err)
	require.Len(t, got.Options, 2, "frozen and unlisted markets are excluded")

	usdc := got.Options[0]
	assert.Equal(t, "CUSDC", usdc.AssetID)
	require.NotNil(t, usdc.Symbol)
	assert.Equal(t, "USDC", *usdc.Symbol)
	require.Len(t, usdc.Pools, 2)
	assert.Equal(t, "1:CUSDC", usdc.Pools[0].ID, "hubs ordered by supplied USD desc")
	// Testnet hub 1 is "Main" in rs-lending-xlm/configs/testnet/hubs.json.
	assert.Equal(t, "Main", usdc.Pools[0].Name)
	assert.InDelta(t, 0.05, usdc.Pools[0].SupplyAPY, 1e-12)
	assert.Equal(t, "2:CUSDC", usdc.Pools[1].ID)

	xlm := got.Options[1]
	assert.Equal(t, "CXLM", xlm.AssetID)
	assert.Nil(t, xlm.Pools[0].SuppliedUSD, "unpriced stays null")
}

func TestXoxnoEarnOptionsEmptyIsNonNil(t *testing.T) {
	svc := NewXoxnoCatalogService(&utils.MockWalletBackendService{}, nil, 0, nil)
	got, err := svc.GetEarnOptions(context.Background(), types.TESTNET)
	require.NoError(t, err)
	assert.NotNil(t, got.Options)
	assert.Empty(t, got.Options)
}

func TestSortedEarnOptionsOrder(t *testing.T) {
	got := sortedEarnOptions(map[string]*types.EarnAssetOption{
		"Z": {AssetID: "Z", Pools: []types.EarnPool{{ID: "z1"}}},
		"A": {AssetID: "A", Pools: []types.EarnPool{
			{ID: "unpriced-b", Name: "b"},
			{ID: "small", SuppliedUSD: f64(1)},
			{ID: "unpriced-a", Name: "a"},
			{ID: "big", SuppliedUSD: f64(10)},
			{ID: "also-small", SuppliedUSD: f64(1)},
		}},
	})
	require.Len(t, got, 2)
	assert.Equal(t, "A", got[0].AssetID, "assets by id")
	ids := make([]string, 0, len(got[0].Pools))
	for _, p := range got[0].Pools {
		ids = append(ids, p.ID)
	}
	assert.Equal(t, []string{"big", "also-small", "small", "unpriced-a", "unpriced-b"}, ids,
		"supplied desc, id tie-break, unpriced last by id")
}

func TestXoxnoCatalogPropagatesUpstreamError(t *testing.T) {
	svc := NewXoxnoCatalogService(&utils.MockWalletBackendService{GetXoxnoLendingMarketsError: assert.AnError}, nil, 0, nil)
	_, err := svc.GetEarnOptions(context.Background(), types.PUBLIC)
	require.ErrorIs(t, err, assert.AnError)
}

// countingWalletBackend delays and counts the market fetch so a herd of
// callers is in flight before the first one returns.
type countingWalletBackend struct {
	*utils.MockWalletBackendService
	delay time.Duration
	calls atomic.Int64
}

func (c *countingWalletBackend) GetXoxnoLendingMarkets(ctx context.Context, network string) ([]wbtypes.XoxnoLendingMarket, error) {
	c.calls.Add(1)
	time.Sleep(c.delay)
	return c.MockWalletBackendService.GetXoxnoLendingMarkets(ctx, network)
}

func TestXoxnoCatalogCoalescesConcurrentFetches(t *testing.T) {
	t.Parallel()

	// A delay wide enough that all goroutines are in-flight at DoChan before
	// the shared fetch completes, so singleflight coalesces them.
	walletBackend := &countingWalletBackend{
		MockWalletBackendService: &utils.MockWalletBackendService{GetXoxnoLendingMarketsResult: xoxnoMarketsFixture()},
		delay:                    50 * time.Millisecond,
	}
	svc := NewXoxnoCatalogService(walletBackend, nil, time.Minute, nil)

	const n = 10
	var wg sync.WaitGroup
	results := make([][]wbtypes.XoxnoLendingMarket, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = svc.GetMarkets(context.Background(), types.TESTNET)
		}(i)
	}
	wg.Wait()

	assert.Equal(t, int64(1), walletBackend.calls.Load(),
		"concurrent reads of one network's catalog should coalesce to a single upstream call")
	for i := range results {
		require.NoError(t, errs[i])
		assert.Len(t, results[i], len(xoxnoMarketsFixture()), "every caller receives the shared result")
	}
}
