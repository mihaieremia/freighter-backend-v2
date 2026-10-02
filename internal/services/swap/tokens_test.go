package swap

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stellar/freighter-backend-v2/internal/metrics"
	"github.com/stellar/freighter-backend-v2/internal/types"
)

const usdcAsset = "USDC-GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN-1"

var (
	tokUSDC = testContract(1)
	tokXLM  = testContract(2)
	tokSolv = testContract(3)
)

// listedFixture is XOXNO's token list: three tokens worth offering and one of
// each kind the service must drop.
func listedFixture() []listedToken {
	return []listedToken{
		{Identifier: tokUSDC, Ticker: "USDC", Name: "USD Coin", Decimals: 7, PNGURL: "https://media/usdc.png", USDPrice: 1.00002, SwapListed: true},
		{Identifier: tokXLM, Ticker: "XLM", Name: "Stellar Lumens", Decimals: 7, PNGURL: "https://media/xlm.png", USDPrice: 0.23, SwapListed: true},
		{Identifier: tokSolv, Ticker: "SolvBTC", Name: "SolvBTC", Decimals: 8, PNGURL: "https://media/solv.png", USDPrice: 84255.6, SwapListed: true},
		{Identifier: testContract(4), Ticker: "NOPE", Name: "Not routable", Decimals: 7, USDPrice: 1, SwapListed: true},
		{Identifier: testContract(5), Ticker: "LP", Name: "LP", Decimals: 7, USDPrice: 5, SwapListed: true, LPToken: true},
		{Identifier: testContract(6), Ticker: "MEH", Name: "Not listed", Decimals: 7, USDPrice: 1},
		{Identifier: testContract(7), Ticker: "BAD", Name: "Bad decimals", Decimals: 6, USDPrice: 1, SwapListed: true},
		{Identifier: testContract(8), Ticker: "FREE", Name: "No price", Decimals: 7, SwapListed: true},
		{Identifier: testContract(9), Ticker: testContract(9), Name: testContract(9), Decimals: 7, USDPrice: 1, SwapListed: true},
		{Identifier: testContract(10), Ticker: "THIN", Name: "Thin pool", Decimals: 7, SwapListed: true},
		{Identifier: "CNOTACONTRACTID", Ticker: "JUNK", Name: "Not a contract id", Decimals: 7, USDPrice: 1, SwapListed: true},
	}
}

// routableFixture is what the aggregator can route: every listed token except
// the unroutable one, with its own decimals.
func routableFixture() []aggregatorToken {
	return []aggregatorToken{
		{ID: tokUSDC, Decimals: 7},
		{ID: tokXLM, Decimals: 7},
		{ID: tokSolv, Decimals: 8},
		{ID: testContract(5), Decimals: 7},
		{ID: testContract(6), Decimals: 7},
		{ID: testContract(7), Decimals: 7},
		{ID: testContract(8), Decimals: 7},
		{ID: testContract(9), Decimals: 7},
		{ID: testContract(10), Decimals: 7},
		{ID: "CNOTACONTRACTID", Decimals: 7},
	}
}

type tokensStub struct {
	list, aggregator *httptest.Server
	listCalls        atomic.Int32
	listed           atomic.Value // []listedToken
	fail             atomic.Bool
	// slow holds the token list request until the test ends.
	slow    atomic.Bool
	release chan struct{}
	// pricesFail fails only the aggregator prices request; priceCalls counts it.
	pricesFail atomic.Bool
	priceCalls atomic.Int32
}

func newTokensStub(t *testing.T) *tokensStub {
	t.Helper()
	st := &tokensStub{release: make(chan struct{})}
	st.listed.Store(listedFixture())
	st.list = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st.listCalls.Add(1)
		if st.slow.Load() {
			select {
			case <-st.release:
			case <-r.Context().Done():
			}
		}
		if st.fail.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(st.listed.Load())
	}))
	st.aggregator = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/prices" {
			st.priceCalls.Add(1)
			if st.pricesFail.Load() || st.fail.Load() {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			_ = json.NewEncoder(w).Encode(pricesFixture())
			return
		}
		if r.URL.Path != "/api/v1/tokens" || st.fail.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(routableFixture())
	}))
	t.Cleanup(st.list.Close)
	t.Cleanup(st.aggregator.Close)
	t.Cleanup(func() { close(st.release) })
	return st
}

func newTokensService(st *tokensStub, ttl time.Duration) (*tokensService, *fakeStellarExpert) {
	expert := newFakeStellarExpert()
	expert.SetContractAsset(tokUSDC, usdcAsset)
	expert.SetContractAsset(tokXLM, "XLM")
	expert.SetContractAsset(tokSolv, "")
	svc := NewTokensService(map[string]Network{
		types.PUBLIC: {QuoteURL: st.aggregator.URL, TokenListURL: st.list.URL + "/stellar/tokens?network=mainnet"},
	}, expert, ttl, nil).(*tokensService)
	return svc, expert
}

func byID(tokens []types.SwapToken) map[string]types.SwapToken {
	out := map[string]types.SwapToken{}
	for _, t := range tokens {
		out[t.ID] = t
	}
	return out
}

func TestSwapTokens_OffersTheListedRoutableTokensWithTheirKind(t *testing.T) {
	t.Parallel()
	svc, _ := newTokensService(newTokensStub(t), time.Minute)

	tokens, err := svc.GetSwapTokens(context.Background(), types.PUBLIC)
	require.NoError(t, err)
	got := byID(tokens)
	require.Len(t, got, 3, "unroutable, LP, unlisted, mismatched-decimals, unpriced, unnamed and non-contract tokens are dropped")

	assert.Equal(t, types.SwapToken{
		ID: tokUSDC, Kind: types.SwapTokenClassic, Asset: "USDC:GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN",
		Code: "USDC", Name: "USD Coin", Decimals: 7, IconURL: "https://media/usdc.png", PriceUSD: 1.00002,
	}, got[tokUSDC])
	assert.Equal(t, types.SwapTokenNative, got[tokXLM].Kind)
	assert.Empty(t, got[tokXLM].Asset)
	assert.Equal(t, types.SwapToken{
		ID: tokSolv, Kind: types.SwapTokenSoroban, Code: "SolvBTC", Name: "SolvBTC", Decimals: 8,
		IconURL: "https://media/solv.png", PriceUSD: 84255.6,
	}, got[tokSolv], "a contract that wraps no asset is a Soroban token")
}

func TestSwapTokens_AskStellarExpertOncePerContractEver(t *testing.T) {
	t.Parallel()
	svc, expert := newTokensService(newTokensStub(t), time.Minute)
	now := time.Unix(1_700_000_000, 0)
	svc.now = func() time.Time { return now }

	for range 3 {
		tokens, err := svc.GetSwapTokens(context.Background(), types.PUBLIC)
		require.NoError(t, err)
		require.Len(t, tokens, 3)
		now = now.Add(2 * time.Minute)
	}

	for _, id := range []string{tokUSDC, tokXLM, tokSolv} {
		assert.Equal(t, 1, expert.ContractCallCount(id), id)
	}
}

func TestSwapTokens_ATokenItCannotClassifyIsLeftOutAndOnlyItIsRetriedNextTime(t *testing.T) {
	t.Parallel()
	st := newTokensStub(t)
	svc, expert := newTokensService(st, time.Minute)
	expert.SetContractErr(tokSolv, errors.New("stellar expert down"))

	tokens, err := svc.GetSwapTokens(context.Background(), types.PUBLIC)
	require.NoError(t, err)
	assert.NotContains(t, byID(tokens), tokSolv, "guessing a kind could send a classic asset without its trustline")
	assert.Contains(t, byID(tokens), tokUSDC)
	lists := st.listCalls.Load()

	expert.SetContractErr(tokSolv, nil)
	tokens, err = svc.GetSwapTokens(context.Background(), types.PUBLIC)
	require.NoError(t, err)
	assert.Contains(t, byID(tokens), tokSolv, "the gap did not wait out the TTL")
	assert.Equal(t, lists, st.listCalls.Load(), "the lists were not fetched again")
	assert.Equal(t, 1, expert.ContractCallCount(tokUSDC))
	assert.Equal(t, 1, expert.ContractCallCount(tokXLM))
	assert.Equal(t, 2, expert.ContractCallCount(tokSolv))
}

func TestSwapTokens_ARefreshSurvivesTheCallerGivingUp(t *testing.T) {
	t.Parallel()
	st := newTokensStub(t)
	svc, _ := newTokensService(st, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _ = svc.GetSwapTokens(ctx, types.PUBLIC)

	tokens, err := svc.GetSwapTokens(context.Background(), types.PUBLIC)
	require.NoError(t, err, "the shared refresh must not inherit one caller's cancellation")
	assert.NotEmpty(t, tokens)
	assert.Equal(t, int32(1), st.listCalls.Load(), "the second caller shares the first refresh")
}

func TestSwapTokens_RemembersMetadataForGood(t *testing.T) {
	t.Parallel()
	svc, _ := newTokensService(newTokensStub(t), time.Minute)

	svc.remember(types.PUBLIC, listedFixture())
	assert.Len(t, svc.meta[types.PUBLIC], 8, "LP, self-ticker and malformed entries carry no metadata")

	renamed := []listedToken{{Identifier: tokUSDC, Ticker: "OTHER", Name: "Other", Decimals: 6, PNGURL: "x"}}
	svc.remember(types.PUBLIC, renamed)
	assert.Equal(t, types.CatalogToken{ID: tokUSDC, Code: "USDC", Name: "USD Coin", Decimals: 7, IconURL: "https://media/usdc.png"}, svc.meta[types.PUBLIC][tokUSDC],
		"metadata is kept after the token leaves the list, and never overwritten")
	assert.Len(t, svc.meta[types.PUBLIC], 8)

	many := make([]listedToken, 0, maxCatalogTokens+5)
	for i := range maxCatalogTokens + 5 {
		many = append(many, listedToken{Identifier: testContractN(i), Ticker: "T"})
	}
	svc.remember(types.PUBLIC, many)
	assert.Len(t, svc.meta[types.PUBLIC], len(many)+8, "metadata survives a large replacement list")
	assert.Equal(t, "USDC", svc.meta[types.PUBLIC][tokUSDC].Code)
}

func TestSwapTokens_UsesCorrectedRoutableDecimals(t *testing.T) {
	t.Parallel()
	st := newTokensStub(t)
	listed := listedFixture()
	listed[2].Decimals = 7 // The aggregator uses 8 for this Soroban token.
	st.listed.Store(listed)
	svc, _ := newTokensService(st, time.Minute)
	now := time.Unix(1_700_000_000, 0)
	svc.now = func() time.Time { return now }

	first, err := svc.GetSwapTokens(context.Background(), types.PUBLIC)
	require.NoError(t, err)
	assert.NotContains(t, byID(first), tokSolv)

	st.listed.Store(listedFixture())
	now = now.Add(2 * time.Minute)
	corrected, err := svc.GetSwapTokens(context.Background(), types.PUBLIC)
	require.NoError(t, err)
	require.Contains(t, byID(corrected), tokSolv)
	assert.Equal(t, 8, byID(corrected)[tokSolv].Decimals)
}

// tokenList is one of the two cached lists a tokensService serves. Both follow
// the same freshness rules.
type tokenList struct {
	name string
	get  func(*tokensService, context.Context, string) (any, error)
	// unpriced returns the list with every price zeroed.
	unpriced func(any) any
	// methods are the metric methods one cold call records.
	methods []string
}

func unpriced[T any](list []T, zero func(*T)) []T {
	out := slices.Clone(list)
	for i := range out {
		zero(&out[i])
	}
	return out
}

var tokenLists = []tokenList{
	{
		name: "swap tokens",
		get: func(s *tokensService, ctx context.Context, network string) (any, error) {
			return s.GetSwapTokens(ctx, network)
		},
		unpriced: func(l any) any {
			return unpriced(l.([]types.SwapToken), func(t *types.SwapToken) { t.PriceUSD = 0 })
		},
		methods: []string{"GetSwapTokens", "GetTokenList", "GetAggregatorTokens"},
	},
	{
		name: "catalog",
		get: func(s *tokensService, ctx context.Context, network string) (any, error) {
			return s.GetTokenCatalog(ctx, network)
		},
		unpriced: func(l any) any {
			return unpriced(l.([]types.CatalogToken), func(t *types.CatalogToken) { t.PriceUSD = 0 })
		},
		methods: []string{"GetTokenCatalog", "GetTokenList", "GetAggregatorTokens", "GetAggregatorPrices"},
	},
}

// storedFixture is a service that has fetched l once at the returned time, for
// the tests that then let a refresh fail.
func storedFixture(t *testing.T, st *tokensStub, l tokenList) (*tokensService, *time.Time, any) {
	t.Helper()
	svc, _ := newTokensService(st, time.Minute)
	now := time.Unix(1_700_000_000, 0)
	svc.now = func() time.Time { return now }
	first, err := l.get(svc, context.Background(), types.PUBLIC)
	require.NoError(t, err)
	require.NotEmpty(t, first)
	return svc, &now, first
}

// soon is a context that expires before any slow upstream answers.
func soon(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	t.Cleanup(cancel)
	return ctx
}

func TestTokenLists(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, l := range tokenLists {
		t.Run(l.name, func(t *testing.T) {
			t.Parallel()

			t.Run("caches within the TTL", func(t *testing.T) {
				t.Parallel()
				st := newTokensStub(t)
				svc, _ := newTokensService(st, time.Minute)
				_, err := l.get(svc, ctx, types.PUBLIC)
				require.NoError(t, err)
				calls := st.listCalls.Load()
				_, err = l.get(svc, ctx, types.PUBLIC)
				require.NoError(t, err)
				assert.Equal(t, calls, st.listCalls.Load())
			})

			t.Run("serves the last list when a refresh fails", func(t *testing.T) {
				t.Parallel()
				st := newTokensStub(t)
				svc, now, first := storedFixture(t, st, l)
				*now = now.Add(2 * time.Minute)
				st.fail.Store(true)
				stale, err := l.get(svc, ctx, types.PUBLIC)
				require.NoError(t, err)
				assert.Equal(t, first, stale)
			})

			t.Run("serves the last list when a refresh outlasts the caller", func(t *testing.T) {
				t.Parallel()
				st := newTokensStub(t)
				svc, now, first := storedFixture(t, st, l)
				*now = now.Add(2 * time.Minute)
				st.slow.Store(true)
				stale, err := l.get(svc, soon(t), types.PUBLIC)
				require.NoError(t, err)
				assert.Equal(t, first, stale)
			})

			t.Run("a cold request gives up with the caller", func(t *testing.T) {
				t.Parallel()
				st := newTokensStub(t)
				st.slow.Store(true)
				svc, _ := newTokensService(st, time.Minute)
				_, err := l.get(svc, soon(t), types.PUBLIC)
				assert.Equal(t, context.DeadlineExceeded, err, "the caller's own deadline, not an upstream failure")
			})

			t.Run("fails when nothing is cached and an upstream is down", func(t *testing.T) {
				t.Parallel()
				st := newTokensStub(t)
				st.fail.Store(true)
				svc, _ := newTokensService(st, time.Minute)
				got, err := l.get(svc, ctx, types.PUBLIC)
				assert.Error(t, err)
				assert.Nil(t, got, "nothing is served stale when nothing was ever fetched")
			})

			t.Run("an unconfigured network has an empty list", func(t *testing.T) {
				t.Parallel()
				svc, _ := newTokensService(newTokensStub(t), time.Minute)
				got, err := l.get(svc, ctx, types.TESTNET)
				require.NoError(t, err)
				assert.NotNil(t, got, "an empty list encodes as [] not null")
				assert.Empty(t, got)
			})

			t.Run("records the call and its upstream fetches", func(t *testing.T) {
				t.Parallel()
				svc, _ := newTokensService(newTokensStub(t), time.Minute)
				m := metrics.NewMetrics(prometheus.NewRegistry())
				svc.svcMetrics = m.Service
				_, err := l.get(svc, ctx, types.PUBLIC)
				require.NoError(t, err)
				for _, method := range l.methods {
					assert.InDelta(t, 1, testutil.ToFloat64(m.Service.CallsTotal.WithLabelValues("swap-tokens", method, "PUBLIC")), 0, method)
				}
			})

			t.Run("a price exactly maxPriceAge old is still served", func(t *testing.T) {
				t.Parallel()
				st := newTokensStub(t)
				svc, now, first := storedFixture(t, st, l)
				*now = now.Add(maxPriceAge)
				st.fail.Store(true)
				stale, err := l.get(svc, ctx, types.PUBLIC)
				require.NoError(t, err)
				assert.Equal(t, first, stale)
			})

			t.Run("an older price is zeroed and a refresh brings it back", func(t *testing.T) {
				t.Parallel()
				st := newTokensStub(t)
				svc, now, first := storedFixture(t, st, l)
				require.NotEqual(t, first, l.unpriced(first), "the fixture carries prices")
				*now = now.Add(maxPriceAge + time.Second)
				st.fail.Store(true)
				stale, err := l.get(svc, ctx, types.PUBLIC)
				require.NoError(t, err)
				assert.Equal(t, l.unpriced(first), stale, "metadata and kind survive; only the price goes")

				*now = now.Add(time.Second)
				st.fail.Store(false)
				fresh, err := l.get(svc, ctx, types.PUBLIC)
				require.NoError(t, err)
				assert.Equal(t, first, fresh)
			})

			t.Run("an older price is zeroed when a refresh outlasts the caller", func(t *testing.T) {
				t.Parallel()
				st := newTokensStub(t)
				svc, now, first := storedFixture(t, st, l)
				*now = now.Add(time.Hour)
				st.slow.Store(true)
				stale, err := l.get(svc, soon(t), types.PUBLIC)
				require.NoError(t, err)
				assert.Equal(t, l.unpriced(first), stale)
			})
		})
	}
}
