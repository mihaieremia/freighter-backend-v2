// ABOUTME: XOXNO lending market-catalog service: the cached market list and the
// ABOUTME: earn view derived from it. No allowlist: markets are governance-created.
package services

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"

	wbtypes "github.com/stellar/wallet-backend/pkg/wbclient/types"

	"github.com/stellar/freighter-backend-v2/internal/logger"
	"github.com/stellar/freighter-backend-v2/internal/metrics"
	"github.com/stellar/freighter-backend-v2/internal/store"
	"github.com/stellar/freighter-backend-v2/internal/types"
)

const (
	xoxnoCatalogServiceName = "xoxno-catalog"

	xoxnoMarketsCacheKeyPrefix = "xoxno:markets:v1"

	// catalogFetchTimeout bounds the shared upstream read. It matches the
	// budget the handlers give a wallet-backend-fronted request, but is its
	// own because the fetch runs detached from any single caller.
	catalogFetchTimeout = 10 * time.Second
)

type xoxnoCatalogService struct {
	walletBackend types.WalletBackendService
	redis         *store.RedisStore
	cacheTTL      time.Duration
	svcMetrics    *metrics.Service
	// fetchGroup coalesces concurrent upstream fetches for the same cache key
	// so a herd at TTL expiry issues one wallet-backend call instead of one
	// per in-flight request. There is a single key per network and every
	// /accounts/positions call reads it, so the herd is the normal case.
	fetchGroup singleflight.Group
}

// NewXoxnoCatalogService wires the XOXNO market views. redis may be nil (no
// caching); cacheTTL is only read when it is not, and the serve flag rejects a
// non-positive one because Redis reads a zero TTL as "never expires".
func NewXoxnoCatalogService(walletBackend types.WalletBackendService, redis *store.RedisStore, cacheTTL time.Duration, m *metrics.Service) types.XoxnoCatalogService {
	return &xoxnoCatalogService{walletBackend: walletBackend, redis: redis, cacheTTL: cacheTTL, svcMetrics: m}
}

func (x *xoxnoCatalogService) Name() string { return xoxnoCatalogServiceName }

// GetMarkets returns every XOXNO market, cached per network. It caches the
// raw SDK type because both the earn view and the positions pricer need the
// upstream shape.
func (x *xoxnoCatalogService) GetMarkets(ctx context.Context, network string) (_ []wbtypes.XoxnoLendingMarket, err error) {
	start := time.Now()
	defer func() {
		metrics.Record(x.svcMetrics, xoxnoCatalogServiceName, "GetMarkets", network, time.Since(start).Seconds(), err)
	}()

	// The cache is best-effort throughout: a nil store or a Redis failure
	// only costs an upstream read, it never fails the request.
	cacheKey := fmt.Sprintf("%s:%s", xoxnoMarketsCacheKeyPrefix, strings.ToLower(network))
	if x.redis != nil {
		hits, cacheErr := x.redis.MGetJSON(ctx, []string{cacheKey}, func() any { return new([]wbtypes.XoxnoLendingMarket) })
		if cacheErr != nil {
			logger.Warn("xoxno-catalog: redis MGet failed; bypassing cache", "key", cacheKey, "error", cacheErr)
		} else if cached, ok := hits[cacheKey].(*[]wbtypes.XoxnoLendingMarket); ok {
			return *cached, nil
		}
	}
	// The caller's ctx only bounds how long this request waits — the shared
	// fetch runs under its own budget so one caller's cancellation can't
	// poison the other waiters.
	ch := x.fetchGroup.DoChan(cacheKey, func() (any, error) {
		fctx, cancel := context.WithTimeout(context.Background(), catalogFetchTimeout)
		defer cancel()
		markets, fetchErr := x.walletBackend.GetXoxnoLendingMarkets(fctx, network)
		if fetchErr != nil {
			return nil, fetchErr
		}
		if x.redis != nil {
			if cacheErr := x.redis.SetJSON(fctx, cacheKey, markets, x.cacheTTL); cacheErr != nil {
				logger.Warn("xoxno-catalog: redis SET failed", "key", cacheKey, "error", cacheErr)
			}
		}
		return markets, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
		markets, _ := res.Val.([]wbtypes.XoxnoLendingMarket)
		return markets, nil
	}
}

// GetEarnOptions derives the earn catalog from the markets: one option per
// asset, one "pool" per hub the asset is listed in with at least one spoke
// accepting deposits. Pool IDs are "<hubId>:<asset>".
func (x *xoxnoCatalogService) GetEarnOptions(ctx context.Context, network string) (_ *types.EarnOptionsCatalog, err error) {
	start := time.Now()
	defer func() {
		metrics.Record(x.svcMetrics, xoxnoCatalogServiceName, "GetEarnOptions", network, time.Since(start).Seconds(), err)
	}()

	markets, err := x.GetMarkets(ctx, network)
	if err != nil {
		return nil, err
	}
	return &types.EarnOptionsCatalog{Options: deriveXoxnoEarnOptions(network, markets)}, nil
}

func deriveXoxnoEarnOptions(network string, markets []wbtypes.XoxnoLendingMarket) []types.EarnAssetOption {
	byAsset := map[string]*types.EarnAssetOption{}
	for _, m := range markets {
		spokes := depositableSpokes(network, m)
		if len(spokes) == 0 {
			continue
		}
		option, ok := byAsset[m.Asset]
		if !ok {
			option = &types.EarnAssetOption{
				AssetID:  m.Asset,
				Symbol:   m.TokenSymbol,
				Name:     m.TokenName,
				Decimals: m.TokenDecimals,
				Pools:    []types.EarnPool{},
			}
			byAsset[m.Asset] = option
		}
		option.Pools = append(option.Pools, types.EarnPool{
			ID:          fmt.Sprintf("%d:%s", m.HubID, m.Asset),
			Name:        xoxnoHubName(network, m.HubID),
			SupplyAPY:   m.SupplyApy,
			SuppliedUSD: m.SuppliedUsd,
			Spokes:      spokes,
		})
	}
	return sortedEarnOptions(byAsset)
}

// depositableSpokes lists the spokes a deposit of this market can go into, by
// id, so the answer is stable between calls. Every spoke earns the same rate —
// it belongs to the market — so there is nothing to rank them by; they differ
// only in how the deposit may later be borrowed against.
//
// A reserve that is paused, frozen, or not collateralizable is left out
// entirely: the contract would refuse the supply. Collateralizability is a
// supply gate, not just a valuation one — `require_can_supply`
// (`controller/src/positions/mod.rs` in rs-lending-xlm) asserts
// `is_collateralizable` and panics `NotCollateral` before any deposit is
// credited.
func depositableSpokes(network string, m wbtypes.XoxnoLendingMarket) []types.EarnSpoke {
	spokes := make([]types.EarnSpoke, 0, len(m.Reserves))
	for _, r := range m.Reserves {
		if r.Paused || r.Frozen || !r.IsCollateralizable {
			continue
		}
		spokes = append(spokes, types.EarnSpoke{
			ID:                      r.SpokeID,
			Name:                    xoxnoSpokeName(network, r.SpokeID),
			LoanToValueBps:          r.LoanToValueBps,
			LiquidationThresholdBps: r.LiquidationThresholdBps,
		})
	}

	slices.SortFunc(spokes, func(a, b types.EarnSpoke) int { return cmp.Compare(a.ID, b.ID) })
	return spokes
}

// suppliedUSDOrLeast orders an unpriced pool last under a descending sort: no
// real supplied figure can be smaller.
func suppliedUSDOrLeast(p types.EarnPool) float64 {
	if p.SuppliedUSD == nil {
		return -math.MaxFloat64
	}
	return *p.SuppliedUSD
}

// sortedEarnOptions flattens the per-asset map into the response order of the
// XOXNO earn catalog: assets by asset id; each asset's pools by supplied USD
// descending (unpriced last, id tie-break).
func sortedEarnOptions(byAsset map[string]*types.EarnAssetOption) []types.EarnAssetOption {
	options := make([]types.EarnAssetOption, 0, len(byAsset))
	for _, option := range byAsset {
		slices.SortFunc(option.Pools, func(a, b types.EarnPool) int {
			if c := cmp.Compare(suppliedUSDOrLeast(b), suppliedUSDOrLeast(a)); c != 0 {
				return c
			}
			return cmp.Compare(a.ID, b.ID)
		})
		options = append(options, *option)
	}
	slices.SortFunc(options, func(a, b types.EarnAssetOption) int { return cmp.Compare(a.AssetID, b.AssetID) })
	return options
}
