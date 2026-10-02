package swap

import (
	"context"
	"errors"
	"math"
	"time"

	"golang.org/x/sync/errgroup"

	xoxno "github.com/xoxno/sdk-go"

	"github.com/stellar/freighter-backend-v2/internal/logger"
	"github.com/stellar/freighter-backend-v2/internal/types"
)

const (
	// minFallbackPriceDepthUSD is the pool depth the aggregator's price needs
	// before it stands in for a missing XOXNO list price. A thin pool is cheap to
	// move, so a price it sets is worse than no price.
	minFallbackPriceDepthUSD = 1000
	// maxCatalogTokens bounds the catalog however long the upstream list is.
	maxCatalogTokens = 2000
)

var errCatalogPricesUnavailable = errors.New("aggregator prices unavailable")

// aggregatorPrice is an entry of the aggregator's /api/v1/prices, keyed by
// contract id.
type aggregatorPrice = xoxno.Price

// GetTokenCatalog returns every registered token except LP tokens and entries
// without metadata. It is cached like GetSwapTokens: if a refresh fails or
// outlasts the caller, the last catalog keeps being served, with any price older
// than maxPriceAge zeroed.
func (s *tokensService) GetTokenCatalog(ctx context.Context, network string) (_ []types.CatalogToken, err error) {
	defer recordCall(s.svcMetrics, tokensServiceName, "GetTokenCatalog", network, time.Now(), &err)

	cfg, ok := s.networks[network]
	if !ok || cfg.QuoteURL == "" || cfg.TokenListURL == "" {
		return []types.CatalogToken{}, nil
	}

	s.mu.Lock()
	entry, cached := s.catalogCache[network]
	s.mu.Unlock()
	if cached && s.now().Sub(entry.fetched) < s.ttl {
		return s.catalogTokens(network, entry.rows), nil
	}

	// Shared by concurrent callers and outliving any one request, like the swap
	// list refresh: ctx only bounds how long this caller waits.
	ch := s.group.DoChan("catalog:"+network, func() (any, error) {
		rows, pricesOK, err := s.fetchCatalog(context.WithoutCancel(ctx), network, cfg)
		if err != nil {
			return nil, err
		}
		if !pricesOK {
			// A catalog without fallback prices is not kept, so the next request
			// tries again; a previous complete one is served in its place.
			if cached {
				return nil, errCatalogPricesUnavailable
			}
			return s.catalogTokens(network, rows), nil
		}
		s.mu.Lock()
		s.catalogCache[network] = tokensCacheEntry{rows: rows, fetched: s.now()}
		s.mu.Unlock()
		return s.catalogTokens(network, rows), nil
	})
	select {
	case <-ctx.Done():
		if cached {
			return s.catalogTokens(network, s.agedRows(entry)), nil
		}
		return nil, ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			if cached {
				return s.catalogTokens(network, s.agedRows(entry)), nil
			}
			return nil, res.Err
		}
		out, _ := res.Val.([]types.CatalogToken)
		return out, nil
	}
}

// catalogTokens turns rows into catalog tokens, in row order.
func (s *tokensService) catalogTokens(network string, rows []tokenRow) []types.CatalogToken {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]types.CatalogToken, 0, len(rows))
	for _, r := range rows {
		m, ok := s.meta[network][r.id]
		if !ok {
			continue
		}
		m.Decimals, m.PriceUSD, m.Swappable = r.decimals, r.priceUSD, r.swappable
		out = append(out, m)
	}
	return out
}

// fetchCatalog builds the catalog from XOXNO's list, the aggregator's tokens and
// the aggregator's prices. The prices only back up the list's own, so a failure
// to read them degrades the catalog instead of failing it; pricesOK reports it.
func (s *tokensService) fetchCatalog(ctx context.Context, network string, cfg Network) (_ []tokenRow, pricesOK bool, _ error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	var prices map[string]aggregatorPrice
	var pricesErr error
	var g errgroup.Group
	g.Go(func() error {
		pricesErr = s.fetchXoxno(ctx, network, "GetAggregatorPrices", func(c *xoxno.Client) (err error) { prices, err = c.Prices(ctx); return })
		return nil
	})
	listed, routable, err := s.fetchLists(ctx, network, cfg)
	_ = g.Wait()
	if err != nil {
		return nil, false, err
	}
	if pricesErr != nil {
		logger.Global().WarnContext(ctx, "swap tokens: aggregator prices unavailable", "error", pricesErr)
		prices = nil
	}
	s.remember(network, listed)
	return catalogRows(listed, routable, prices), pricesErr == nil, nil
}

// catalogRows keeps each listed token with metadata once, up to maxCatalogTokens,
// priced from the list, else the aggregator when deep enough, else 0.
func catalogRows(listed []listedToken, routable []aggregatorToken, prices map[string]aggregatorPrice) []tokenRow {
	routableDecimals := make(map[string]int, len(routable))
	for _, t := range routable {
		routableDecimals[t.ID] = t.Decimals
	}
	seen := make(map[string]struct{}, len(listed))
	out := make([]tokenRow, 0, len(listed))
	for _, t := range listed {
		if len(out) == maxCatalogTokens {
			break
		}
		if !hasMetadata(t) {
			continue
		}
		if _, dup := seen[t.Identifier]; dup {
			continue
		}
		seen[t.Identifier] = struct{}{}
		decimals, routed := routableDecimals[t.Identifier]
		out = append(out, tokenRow{
			id:        t.Identifier,
			decimals:  t.Decimals,
			priceUSD:  catalogPrice(t, prices[t.Identifier]),
			swappable: t.SwapListed && routed && decimals == t.Decimals,
		})
	}
	return out
}

func catalogPrice(t listedToken, fallback aggregatorPrice) float64 {
	if usableUSD(t.USDPrice) {
		return t.USDPrice
	}
	if usableUSD(fallback.USD) && fallback.DepthUSD >= minFallbackPriceDepthUSD {
		return fallback.USD
	}
	return 0
}

func usableUSD(p float64) bool { return p > 0 && !math.IsInf(p, 0) && !math.IsNaN(p) }
