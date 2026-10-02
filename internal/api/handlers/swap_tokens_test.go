package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stellar/freighter-backend-v2/internal/types"
)

type fakeSwapTokensService struct {
	tokens  []types.SwapToken
	catalog []types.CatalogToken
	err     error
	network string
	// catalogCalls counts GetTokenCatalog calls.
	catalogCalls int
}

func (f *fakeSwapTokensService) GetTokenCatalog(_ context.Context, network string) ([]types.CatalogToken, error) {
	f.network = network
	f.catalogCalls++
	return f.catalog, f.err
}

func (f *fakeSwapTokensService) Name() string { return "fake" }
func (f *fakeSwapTokensService) GetSwapTokens(_ context.Context, network string) ([]types.SwapToken, error) {
	f.network = network
	return f.tokens, f.err
}

func callSwapTokens(svc types.SwapTokensService, network string) (*httptest.ResponseRecorder, error) {
	return callSwapTokensURL(svc, "/api/v1/swap/tokens?network="+network)
}

func callSwapTokensURL(svc types.SwapTokensService, target string) (*httptest.ResponseRecorder, error) {
	req, _ := http.NewRequest(http.MethodGet, target, nil)
	rr := httptest.NewRecorder()
	return rr, NewSwapTokensHandler(svc).GetSwapTokens(rr, req)
}

func TestSwapTokens_RejectsOtherNetworks(t *testing.T) {
	t.Parallel()
	for _, network := range []string{"", "FUTURENET", "MAINNET"} {
		t.Run(network, func(t *testing.T) {
			t.Parallel()
			svc := &fakeSwapTokensService{}
			_, err := callSwapTokens(svc, network)
			require.Error(t, err)
			assert.Equal(t, http.StatusBadRequest, unwrapHttpStatus(t, err))
			assert.Empty(t, svc.network)
		})
	}
}

func TestSwapTokens_DefaultResponseShapeIsUnchanged(t *testing.T) {
	t.Parallel()
	svc := &fakeSwapTokensService{
		tokens: []types.SwapToken{
			{ID: "CUSDC", Kind: types.SwapTokenClassic, Asset: "USDC:GISSUER", Code: "USDC", Name: "USD Coin", Decimals: 7, IconURL: "https://media/usdc.png", PriceUSD: 1},
			{ID: "CDEEP", Kind: types.SwapTokenSoroban, Code: "DEEP", Name: "Deep", Decimals: 6, IconURL: "https://media/deep.png", PriceUSD: 1.02},
		},
		catalog: []types.CatalogToken{{ID: "CX"}},
	}

	rr, err := callSwapTokens(svc, "PUBLIC")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, types.PUBLIC, svc.network)
	assert.JSONEq(t, `{"data":[
		{"id":"CUSDC","kind":"classic","asset":"USDC:GISSUER","code":"USDC","name":"USD Coin","decimals":7,"iconUrl":"https://media/usdc.png","priceUsd":1},
		{"id":"CDEEP","kind":"soroban","code":"DEEP","name":"Deep","decimals":6,"iconUrl":"https://media/deep.png","priceUsd":1.02}
	]}`, rr.Body.String())
	assert.Zero(t, svc.catalogCalls, "no scope keeps the swap list")
}

func TestSwapTokens_ScopeAllReturnsTheCatalog(t *testing.T) {
	t.Parallel()
	svc := &fakeSwapTokensService{
		tokens:  []types.SwapToken{{ID: "CNOT"}},
		catalog: []types.CatalogToken{{ID: "CDEEP", Code: "DEEP", Name: "Deep", Decimals: 6, IconURL: "https://media/deep.png", PriceUSD: 1.02, Swappable: true}, {ID: "CFREE", Code: "FREE", Name: "Free", Decimals: 7}},
	}

	rr, err := callSwapTokensURL(svc, "/api/v1/swap/tokens?network=PUBLIC&scope=all")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, types.PUBLIC, svc.network)
	assert.JSONEq(t, `{"data":[
		{"id":"CDEEP","code":"DEEP","name":"Deep","decimals":6,"iconUrl":"https://media/deep.png","priceUsd":1.02,"swappable":true},
		{"id":"CFREE","code":"FREE","name":"Free","decimals":7,"priceUsd":0,"swappable":false}
	]}`, rr.Body.String())
}

func TestSwapTokens_RejectsAnUnknownScope(t *testing.T) {
	t.Parallel()
	for _, scope := range []string{"ALL", "everything", "all,swap", "%20"} {
		t.Run(scope, func(t *testing.T) {
			t.Parallel()
			svc := &fakeSwapTokensService{}
			_, err := callSwapTokensURL(svc, "/api/v1/swap/tokens?network=PUBLIC&scope="+scope)
			require.Error(t, err)
			assert.Equal(t, http.StatusBadRequest, unwrapHttpStatus(t, err))
			assert.Contains(t, err.Error(), "invalid scope")
			assert.Empty(t, svc.network, "the service is not called")
		})
	}
}

func TestSwapTokens_ServiceErrorsAreMapped(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"/api/v1/swap/tokens?network=PUBLIC", "/api/v1/swap/tokens?network=PUBLIC&scope=all"} {
		_, err := callSwapTokensURL(&fakeSwapTokensService{err: context.DeadlineExceeded}, target)
		require.Error(t, err)
		assert.Equal(t, http.StatusGatewayTimeout, unwrapHttpStatus(t, err), target)
		assert.Contains(t, err.Error(), "swap tokens", "the message names the resource, not a quote")
	}
}

func TestSwapTokens_ScopeAllStillValidatesTheNetwork(t *testing.T) {
	t.Parallel()
	_, err := callSwapTokensURL(&fakeSwapTokensService{}, "/api/v1/swap/tokens?network=FUTURENET&scope=all")
	require.Error(t, err)
	assert.Equal(t, http.StatusBadRequest, unwrapHttpStatus(t, err))
}
