// ABOUTME: Handler tests for the XOXNO earn-options endpoint: validation,
// ABOUTME: error translation, and the success envelope.
package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stellar/freighter-backend-v2/internal/metrics"
	"github.com/stellar/freighter-backend-v2/internal/types"
	"github.com/stellar/freighter-backend-v2/internal/utils"
)

const xoxnoEarnOptionsPath = "/api/v1/protocols/xoxno/earn-options"

func serveEarnOptions(t *testing.T, svc types.XoxnoCatalogService, target string) *httptest.ResponseRecorder {
	t.Helper()
	handler := NewEarnOptionsHandler(svc)
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+xoxnoEarnOptionsPath, CustomHandler(handler.GetEarnOptions))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestEarnOptionsHandler(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		svc := &utils.MockXoxnoCatalogService{GetEarnOptionsResult: &types.EarnOptionsCatalog{
			Options: []types.EarnAssetOption{{AssetID: "CUSDC", Pools: []types.EarnPool{{ID: "1:CUSDC"}}}},
		}}
		rec := serveEarnOptions(t, svc, xoxnoEarnOptionsPath+"?network=TESTNET")
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `"asset_id":"CUSDC"`)
		assert.Contains(t, rec.Body.String(), `"id":"1:CUSDC"`)
	})

	t.Run("validation", func(t *testing.T) {
		for _, target := range []string{xoxnoEarnOptionsPath, xoxnoEarnOptionsPath + "?network=NOPE", xoxnoEarnOptionsPath + "?network=FUTURENET"} {
			rec := serveEarnOptions(t, &utils.MockXoxnoCatalogService{}, target)
			assert.Equal(t, http.StatusBadRequest, rec.Code, target)
		}
	})

	t.Run("error translation", func(t *testing.T) {
		rec := serveEarnOptions(t, &utils.MockXoxnoCatalogService{
			GetEarnOptionsError: &metrics.UpstreamError{Kind: "http_error", Code: 502, Err: errors.New("boom")},
		}, xoxnoEarnOptionsPath+"?network=PUBLIC")
		assert.Equal(t, http.StatusBadGateway, rec.Code)

		rec = serveEarnOptions(t, &utils.MockXoxnoCatalogService{GetEarnOptionsError: errors.New("wat")}, xoxnoEarnOptionsPath+"?network=TESTNET")
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("empty is 200", func(t *testing.T) {
		rec := serveEarnOptions(t, &utils.MockXoxnoCatalogService{}, xoxnoEarnOptionsPath+"?network=TESTNET")
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `"options":[]`)
	})
}
