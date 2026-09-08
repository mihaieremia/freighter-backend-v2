// ABOUTME: XOXNO lending market-catalog service: the cached market list and the
// ABOUTME: earn view derived from it. No allowlist: markets are governance-created.
package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	wbtypes "github.com/stellar/wallet-backend/pkg/wbclient/types"

	"github.com/stellar/freighter-backend-v2/internal/metrics"
	"github.com/stellar/freighter-backend-v2/internal/store"
	"github.com/stellar/freighter-backend-v2/internal/types"
)

const (
	xoxnoCatalogServiceName = "xoxno-catalog"

	xoxnoMarketsCacheKeyPrefix = "xoxno:markets:v1"
)

type xoxnoCatalogService struct {
	walletBackend types.WalletBackendService
	redis         *store.RedisStore
	cacheTTL      time.Duration
	svcMetrics    *metrics.Service
}

// NewXoxnoCatalogService wires the XOXNO market views. redis may be nil (no
// caching).
func NewXoxnoCatalogService(walletBackend types.WalletBackendService, redis *store.RedisStore, cacheTTL time.Duration, m *metrics.Service) types.XoxnoCatalogService {
	if cacheTTL <= 0 {
		cacheTTL = defaultCatalogCacheTTL
	}
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

	cacheKey := fmt.Sprintf("%s:%s", xoxnoMarketsCacheKeyPrefix, strings.ToLower(network))
	if cached, ok := cacheGet[[]wbtypes.XoxnoLendingMarket](ctx, x.redis, cacheKey); ok {
		return *cached, nil
	}
	markets, err := x.walletBackend.GetXoxnoLendingMarkets(ctx, network)
	if err != nil {
		return nil, err
	}
	cacheSet(ctx, x.redis, cacheKey, markets, x.cacheTTL)
	return markets, nil
}

// GetEarnOptions derives the earn catalog from the markets: one option per
// asset, one "pool" per hub the asset is listed in with at least one spoke
// accepting deposits (not paused or frozen). Pool IDs are "<hubId>:<asset>".
func (x *xoxnoCatalogService) GetEarnOptions(ctx context.Context, network string) (_ *types.EarnOptionsCatalog, err error) {
	start := time.Now()
	defer func() {
		metrics.Record(x.svcMetrics, xoxnoCatalogServiceName, "GetEarnOptions", network, time.Since(start).Seconds(), err)
	}()

	markets, err := x.GetMarkets(ctx, network)
	if err != nil {
		return nil, err
	}
	return &types.EarnOptionsCatalog{Options: deriveXoxnoEarnOptions(markets)}, nil
}

func deriveXoxnoEarnOptions(markets []wbtypes.XoxnoLendingMarket) []types.EarnAssetOption {
	byAsset := map[string]*types.EarnAssetOption{}
	for _, m := range markets {
		if !xoxnoAcceptsDeposits(m) {
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
		name := fmt.Sprintf("XOXNO Hub %d", m.HubID)
		supplyAPY := m.SupplyApy
		// EmissionsSupplyAPR stays nil: XOXNO has no emissions program.
		option.Pools = append(option.Pools, types.EarnPool{
			ID:          fmt.Sprintf("%d:%s", m.HubID, m.Asset),
			Name:        &name,
			SupplyAPY:   &supplyAPY,
			SuppliedUSD: m.SuppliedUsd,
		})
	}
	return sortedEarnOptions(byAsset)
}

// xoxnoAcceptsDeposits reports whether any spoke lists the market open.
func xoxnoAcceptsDeposits(m wbtypes.XoxnoLendingMarket) bool {
	for _, r := range m.Reserves {
		if !r.Paused && !r.Frozen {
			return true
		}
	}
	return false
}
