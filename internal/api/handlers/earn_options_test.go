// ABOUTME: Handler tests for the earn-options endpoints (Blend and XOXNO):
// ABOUTME: validation, error translation, and the success envelope.
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

func serveEarnOptions(t *testing.T, protocol string, svc types.EarnCatalogService, target string) *httptest.ResponseRecorder {
	t.Helper()
	handler := NewEarnOptionsHandler(protocol, svc)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/protocols/"+protocol+"/earn-options", CustomHandler(handler.GetEarnOptions))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestEarnOptionsHandler(t *testing.T) {
	for _, tc := range []struct {
		protocol string
		poolID   string
	}{
		{"blend", "CPOOL"},
		{"xoxno", "1:CUSDC"},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			path := "/api/v1/protocols/" + tc.protocol + "/earn-options"

			t.Run("success", func(t *testing.T) {
				svc := &utils.MockBlendCatalogService{GetEarnOptionsResult: &types.EarnOptionsCatalog{
					Options: []types.EarnAssetOption{{AssetID: "CUSDC", Pools: []types.EarnPool{{ID: tc.poolID}}}},
				}}
				rec := serveEarnOptions(t, tc.protocol, svc, path+"?network=TESTNET")
				require.Equal(t, http.StatusOK, rec.Code)
				assert.Contains(t, rec.Body.String(), `"asset_id":"CUSDC"`)
				assert.Contains(t, rec.Body.String(), `"id":"`+tc.poolID+`"`)
			})

			t.Run("validation", func(t *testing.T) {
				for _, target := range []string{path, path + "?network=NOPE", path + "?network=FUTURENET"} {
					rec := serveEarnOptions(t, tc.protocol, &utils.MockBlendCatalogService{}, target)
					assert.Equal(t, http.StatusBadRequest, rec.Code, target)
				}
			})

			t.Run("error translation", func(t *testing.T) {
				rec := serveEarnOptions(t, tc.protocol, &utils.MockBlendCatalogService{
					GetEarnOptionsError: &metrics.UpstreamError{Kind: "http_error", Code: 502, Err: errors.New("boom")},
				}, path+"?network=PUBLIC")
				assert.Equal(t, http.StatusBadGateway, rec.Code)

				rec = serveEarnOptions(t, tc.protocol, &utils.MockBlendCatalogService{GetEarnOptionsError: errors.New("wat")}, path+"?network=TESTNET")
				assert.Equal(t, http.StatusInternalServerError, rec.Code)
			})

			t.Run("empty is 200", func(t *testing.T) {
				rec := serveEarnOptions(t, tc.protocol, &utils.MockBlendCatalogService{}, path+"?network=TESTNET")
				require.Equal(t, http.StatusOK, rec.Code)
				assert.Contains(t, rec.Body.String(), `"options":[]`)
			})
		})
	}
}
