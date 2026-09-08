// ABOUTME: Positions service: fans out per-address wallet-backend lending
// ABOUTME: fetches and assembles the frontend-shaped account positions response.
package services

import (
	"context"
	"math"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/stellar/freighter-backend-v2/internal/metrics"
	"github.com/stellar/freighter-backend-v2/internal/types"
	"github.com/stellar/freighter-backend-v2/internal/utils"
)

const positionsServiceName = "positions"

type positionsService struct {
	walletBackend  types.WalletBackendService
	xoxnoMarkets   types.XoxnoCatalogService
	maxConcurrency int
	svcMetrics     *metrics.Service
}

// NewPositionsService wires the positions view. maxConcurrency caps the
// per-request fan-out goroutines, like the balances fan-out, and must be
// positive: it is the same operator-supplied value NewWalletBackendService
// already rejects when non-positive. xoxnoMarkets prices XOXNO legs and must
// be non-nil.
func NewPositionsService(walletBackend types.WalletBackendService, xoxnoMarkets types.XoxnoCatalogService, maxConcurrency int, m *metrics.Service) types.PositionsService {
	return &positionsService{
		walletBackend:  walletBackend,
		xoxnoMarkets:   xoxnoMarkets,
		maxConcurrency: maxConcurrency,
		svcMetrics:     m,
	}
}

func (p *positionsService) Name() string { return positionsServiceName }

// GetAccountsPositions returns positions for each unique requested address,
// fetched from wallet-backend on every request (like balances): no caching,
// so a fresh deposit is visible as soon as the indexer ingests it. Unknown
// accounts are normal per-address outcomes (empty positions, already
// normalized by the wallet-backend service); any other failure is systemic
// and fails the whole request. That includes the one market-catalog read
// that prices every address's legs.
func (p *positionsService) GetAccountsPositions(ctx context.Context, addresses []string, network string) (_ []*types.AccountPositions, err error) {
	start := time.Now()
	defer func() {
		metrics.Record(p.svcMetrics, positionsServiceName, "GetAccountsPositions", network, time.Since(start).Seconds(), err)
	}()

	unique := utils.DedupePreserveOrder(addresses)
	results := make([]*types.AccountPositions, len(unique))

	// One catalog read prices every address's XOXNO legs; it is cached
	// per network, so this is cheap.
	list, err := p.xoxnoMarkets.GetMarkets(ctx, network)
	if err != nil {
		return nil, err
	}
	markets := indexXoxnoMarkets(list)

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(p.maxConcurrency)
	for i, addr := range unique {
		g.Go(func() error {
			xoxno, fetchErr := p.walletBackend.GetXoxnoLendingPositions(gctx, addr, network)
			if fetchErr != nil {
				return fetchErr
			}
			rows := mapXoxnoPositions(network, xoxno, markets)
			total, netAPY := accountAggregate(rows)
			results[i] = &types.AccountPositions{
				Address:       addr,
				TotalValueUSD: total,
				NetAPY:        netAPY,
				Positions:     rows,
			}
			return nil
		})
	}
	if err = g.Wait(); err != nil {
		return nil, err
	}
	return results, nil
}

// accountAggregate computes the header figures from the per-pool summaries.
//
// TotalValueUSD: Σ pool NetUSD, with strict null propagation (any
// unavailable value nulls the total — an undercounted "total" is worse than
// an honest null), mirroring upstream's convention for pool totals. 0 for
// an account with no positions.
//
// NetAPY: mean of pool NetAPY weighted by pool SuppliedUSD — the base each
// per-pool rate is defined over (net dollars / total supplied), so
// rate × base reproduces the per-pool dollar earnings. Null when any pool's
// NetAPY or SuppliedUSD is unavailable or the supplied base is zero.
func accountAggregate(positions []types.PoolPosition) (total *float64, netAPY *float64) {
	sum := 0.0
	suppliedSum := 0.0
	apyNumerator := 0.0
	apyKnown := true
	for _, pool := range positions {
		if pool.NetUSD == nil {
			return nil, nil
		}
		sum += *pool.NetUSD
		if pool.NetAPY == nil || pool.SuppliedUSD == nil {
			apyKnown = false
			continue
		}
		apyNumerator += *pool.NetAPY * *pool.SuppliedUSD
		suppliedSum += *pool.SuppliedUSD
	}

	total = &sum
	if apyKnown && suppliedSum != 0 {
		apy := apyNumerator / suppliedSum
		if !math.IsInf(apy, 0) && !math.IsNaN(apy) {
			netAPY = &apy
		}
	}
	return total, netAPY
}
