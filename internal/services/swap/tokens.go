package swap

import (
	"context"
	"net/http"
	"slices"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"

	"github.com/stellar/freighter-backend-v2/internal/logger"
	"github.com/stellar/freighter-backend-v2/internal/metrics"
	"github.com/stellar/freighter-backend-v2/internal/types"
	"github.com/stellar/freighter-backend-v2/internal/utils"
	"github.com/stellar/freighter-backend-v2/internal/utils/assetid"
)

const (
	tokensServiceName = "swap-tokens"
	// classifyConcurrency bounds the parallel Stellar Expert lookups.
	classifyConcurrency = 8
	// fetchTimeout bounds one refresh. It must stay under the swap
	// tokens handler timeout.
	fetchTimeout = 8 * time.Second
	// maxPriceAge is how old a cached price may be when it is served because a
	// refresh failed. Three default cache TTLs.
	maxPriceAge = 3 * time.Minute
)

// tokenRow is the part of a listed token that changes: its price and whether
// the aggregator can swap it. Its position in a list is part of it too.
type tokenRow struct {
	id        string
	priceUSD  float64
	swappable bool
}

// tokensCacheEntry is one fetched list of rows, valid for the cache TTL.
type tokensCacheEntry struct {
	rows    []tokenRow
	fetched time.Time
}

// tokensService lists the swap-listed tokens the aggregator can route. Stellar
// Expert says which are classic assets (a Stellar Asset Contract names the asset
// it wraps). A token's metadata and wrapped asset are kept for good; only its
// price, swappability and place in the list expire with the cache TTL.
type tokensService struct {
	networks   map[string]Network
	contracts  types.StellarExpertService
	httpClient *http.Client
	ttl        time.Duration
	now        func() time.Time
	svcMetrics *metrics.Service

	group singleflight.Group
	mu    sync.Mutex
	cache map[string]tokensCacheEntry
	// catalogCache holds the scope=all catalog, kept apart from the swap list.
	catalogCache map[string]tokensCacheEntry
	// meta holds each network's token metadata by contract id, as first seen. It
	// is bounded by the longest list seen, and never below maxCatalogTokens.
	meta map[string]map[string]types.CatalogToken
	// assets maps network+contract to the classic asset it wraps, "" for none.
	assets map[string]string
}

// NewTokensService returns the service behind the swap tokens route. Tokens are
// listed per network from networks and cached for ttl; contracts classifies
// each token as native, classic or Soroban.
func NewTokensService(networks map[string]Network, contracts types.StellarExpertService, ttl time.Duration, svcMetrics *metrics.Service) types.SwapTokensService {
	return &tokensService{
		networks:     networks,
		contracts:    contracts,
		httpClient:   &http.Client{Timeout: xoxnoHTTPTimeout},
		ttl:          ttl,
		now:          time.Now,
		svcMetrics:   svcMetrics,
		cache:        map[string]tokensCacheEntry{},
		catalogCache: map[string]tokensCacheEntry{},
		meta:         map[string]map[string]types.CatalogToken{},
		assets:       map[string]string{},
	}
}

func (s *tokensService) Name() string { return tokensServiceName }

// listedToken is the part of an entry of XOXNO's Stellar token list used here.
type listedToken struct {
	Identifier string  `json:"identifier"`
	Ticker     string  `json:"ticker"`
	Name       string  `json:"name"`
	Decimals   int     `json:"decimals"`
	PNGURL     string  `json:"pngUrl"`
	USDPrice   float64 `json:"usdPrice"`
	SwapListed bool    `json:"swapListed"`
	LPToken    bool    `json:"lpToken"`
}

type aggregatorToken struct {
	ID       string `json:"id"`
	Decimals int    `json:"decimals"`
}

// GetSwapTokens returns the listed, routable tokens. The list is cached for the
// TTL. If a refresh fails or outlasts the caller, the last list keeps being
// served, with any price older than maxPriceAge zeroed; with no list yet, that
// is an error. A token whose kind could not be looked up is left out, and only
// that lookup is tried again on the next request.
func (s *tokensService) GetSwapTokens(ctx context.Context, network string) (_ []types.SwapToken, err error) {
	defer recordCall(s.svcMetrics, tokensServiceName, "GetSwapTokens", network, time.Now(), &err)

	cfg, ok := s.networks[network]
	if !ok || cfg.QuoteURL == "" || cfg.TokenListURL == "" {
		return []types.SwapToken{}, nil
	}

	s.mu.Lock()
	entry, cached := s.cache[network]
	s.mu.Unlock()
	if cached && s.now().Sub(entry.fetched) < s.ttl && len(s.unclassified(network, entry.rows)) == 0 {
		return s.swapTokens(network, entry.rows), nil
	}

	// The refresh is shared by concurrent callers and outlives any one request,
	// so it must not inherit a caller's cancellation; ctx only bounds how long
	// this caller waits for it.
	ch := s.group.DoChan(network, func() (any, error) {
		return s.refreshSwapTokens(context.WithoutCancel(ctx), network, cfg)
	})
	select {
	case <-ctx.Done():
		if cached {
			return s.swapTokens(network, s.agedRows(entry)), nil
		}
		return nil, ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			if cached {
				return s.swapTokens(network, s.agedRows(entry)), nil
			}
			return nil, res.Err
		}
		out, _ := res.Val.([]types.SwapToken)
		return out, nil
	}
}

// refreshSwapTokens reloads the rows unless another refresh just did, then
// classifies the tokens it has no kind for yet.
func (s *tokensService) refreshSwapTokens(ctx context.Context, network string, cfg Network) ([]types.SwapToken, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	s.mu.Lock()
	entry, cached := s.cache[network]
	s.mu.Unlock()
	if !cached || s.now().Sub(entry.fetched) >= s.ttl {
		listed, routable, err := s.fetchLists(ctx, network, cfg)
		if err != nil {
			return nil, err
		}
		s.remember(network, listed)
		entry = tokensCacheEntry{rows: swapRows(listed, routable), fetched: s.now()}
		s.mu.Lock()
		s.cache[network] = entry
		s.mu.Unlock()
	}
	s.classifyAll(ctx, network, s.unclassified(network, entry.rows))
	return s.swapTokens(network, entry.rows), nil
}

// agedRows returns e's rows with every price zeroed once it is older than
// maxPriceAge: a stale price is worse than none.
func (s *tokensService) agedRows(e tokensCacheEntry) []tokenRow {
	if s.now().Sub(e.fetched) <= maxPriceAge {
		return e.rows
	}
	rows := slices.Clone(e.rows)
	for i := range rows {
		rows[i].priceUSD = 0
	}
	return rows
}

// remember records the metadata of every listed token not seen before. Past
// maxCatalogTokens it also forgets the tokens the list no longer carries.
func (s *tokensService) remember(network string, listed []listedToken) {
	s.mu.Lock()
	defer s.mu.Unlock()
	known := s.meta[network]
	if known == nil {
		known = map[string]types.CatalogToken{}
		s.meta[network] = known
	}
	current := make(map[string]struct{}, len(listed))
	for _, t := range listed {
		if !hasMetadata(t) {
			continue
		}
		current[t.Identifier] = struct{}{}
		if _, ok := known[t.Identifier]; !ok {
			known[t.Identifier] = types.CatalogToken{ID: t.Identifier, Code: t.Ticker, Name: t.Name, Decimals: t.Decimals, IconURL: t.PNGURL}
		}
	}
	if len(known) > maxCatalogTokens {
		for id := range known {
			if _, ok := current[id]; !ok {
				delete(known, id)
			}
		}
	}
}

// swapTokens turns rows into swap tokens, in row order. A row whose metadata
// or kind is not known yet is left out.
func (s *tokensService) swapTokens(network string, rows []tokenRow) []types.SwapToken {
	s.mu.Lock()
	defer s.mu.Unlock()
	tokens := make([]types.SwapToken, 0, len(rows))
	for _, r := range rows {
		meta, ok := s.meta[network][r.id]
		if !ok {
			continue
		}
		wrapped, ok := s.assets[network+":"+r.id]
		if !ok {
			continue
		}
		if token, ok := newSwapToken(meta, r.priceUSD, wrapped); ok {
			tokens = append(tokens, token)
		}
	}
	return tokens
}

// unclassified returns the contracts among rows that Stellar Expert has not
// answered for yet.
func (s *tokensService) unclassified(network string, rows []tokenRow) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, r := range rows {
		if _, ok := s.assets[network+":"+r.id]; !ok {
			out = append(out, r.id)
		}
	}
	return out
}

// fetchLists fetches XOXNO's token list and the aggregator's token list in parallel.
func (s *tokensService) fetchLists(ctx context.Context, network string, cfg Network) ([]listedToken, []aggregatorToken, error) {
	var listed []listedToken
	var routable []aggregatorToken
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return s.get(gctx, network, "GetTokenList", "swap token list", cfg.TokenListURL, &listed)
	})
	g.Go(func() error {
		return s.get(gctx, network, "GetAggregatorTokens", "aggregator tokens", cfg.QuoteURL+"/api/v1/tokens", &routable)
	})
	if err := g.Wait(); err != nil {
		return nil, nil, err
	}
	return listed, routable, nil
}

// hasMetadata reports whether a listed entry can be offered at all: a contract
// id, not an LP token, and not an entry whose ticker is its own contract id, which
// has no metadata to show.
func hasMetadata(t listedToken) bool {
	return utils.IsValidContractID(t.Identifier) && !t.LPToken && t.Ticker != t.Identifier
}

// swapRows keeps the listed tokens worth offering: swap-listed, priced, with
// real metadata, and routable by the aggregator.
func swapRows(listed []listedToken, routable []aggregatorToken) []tokenRow {
	routableDecimals := make(map[string]int, len(routable))
	for _, t := range routable {
		routableDecimals[t.ID] = t.Decimals
	}
	var out []tokenRow
	for _, t := range listed {
		decimals, ok := routableDecimals[t.Identifier]
		// The aggregator's decimals decide how an amount is read, so a list that
		// disagrees with it is not trusted.
		if ok && hasMetadata(t) && t.SwapListed && t.USDPrice > 0 && decimals == t.Decimals {
			out = append(out, tokenRow{id: t.Identifier, priceUSD: t.USDPrice, swappable: true})
		}
	}
	return out
}

// classifyAll classifies the given contracts with bounded parallelism. A
// contract that cannot be classified is logged and left for the next request.
func (s *tokensService) classifyAll(ctx context.Context, network string, ids []string) {
	var g errgroup.Group
	g.SetLimit(classifyConcurrency)
	for _, id := range ids {
		g.Go(func() error {
			if err := s.classify(ctx, network, id); err != nil {
				logger.Global().WarnContext(ctx, "swap tokens: token could not be classified", "contract", id, "error", err)
			}
			return nil
		})
	}
	_ = g.Wait()
}

// newSwapToken builds the token for a row and the classic asset its contract
// wraps ("XLM" for native, "" for a Soroban token). It is false when the wrapped
// asset is not a readable Stellar Expert id.
func newSwapToken(m types.CatalogToken, priceUSD float64, wrapped string) (types.SwapToken, bool) {
	token := types.SwapToken{ID: m.ID, Code: m.Code, Name: m.Name, Decimals: m.Decimals, IconURL: m.IconURL, PriceUSD: priceUSD, Kind: types.SwapTokenSoroban}
	switch wrapped {
	case "":
	case "XLM":
		token.Kind = types.SwapTokenNative
	default:
		code, issuer, ok := assetid.SplitStellarExpert(wrapped)
		if !ok {
			return types.SwapToken{}, false
		}
		token.Kind, token.Asset, token.Code = types.SwapTokenClassic, code+":"+issuer, code
	}
	return token, true
}

// classify records the classic asset a contract wraps, "" for a Soroban token,
// asking Stellar Expert only for contracts it has not answered before.
func (s *tokensService) classify(ctx context.Context, network, contractID string) error {
	wrapped, err := s.contracts.GetContractAsset(ctx, network, contractID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.assets[network+":"+contractID] = wrapped
	s.mu.Unlock()
	return nil
}

// get fetches one upstream list. method labels the call in metrics; what
// names it in errors.
func (s *tokensService) get(ctx context.Context, network, method, what, reqURL string, dest any) (err error) {
	defer recordCall(s.svcMetrics, tokensServiceName, method, network, time.Now(), &err)

	status, err := getJSON(ctx, s.httpClient, reqURL, dest)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return statusError(what, status)
	}
	return nil
}
