// ABOUTME: Handler for GET /protocols/xoxno/earn-options: the asset-first
// ABOUTME: "where can I earn this" catalog for XOXNO lending.
package handlers

import (
	"context"
	"net/http"

	response "github.com/stellar/freighter-backend-v2/internal/api/httpresponse"
	"github.com/stellar/freighter-backend-v2/internal/types"
)

// EarnOptionsHandler serves the XOXNO earn catalog.
type EarnOptionsHandler struct {
	CatalogService types.XoxnoCatalogService
}

func NewEarnOptionsHandler(svc types.XoxnoCatalogService) *EarnOptionsHandler {
	return &EarnOptionsHandler{CatalogService: svc}
}

// GetEarnOptions handles GET /api/v1/protocols/xoxno/earn-options.
func (h *EarnOptionsHandler) GetEarnOptions(w http.ResponseWriter, r *http.Request) error {
	ctx, cancel := context.WithTimeout(r.Context(), WalletBackendContextTimeout)
	defer cancel()

	network, networkErr := walletBackendNetworkFromQuery(r)
	if networkErr != nil {
		return networkErr
	}

	options, err := h.CatalogService.GetEarnOptions(ctx, network)
	if err != nil {
		return translateServiceError(r.Context(), err, "xoxno earn options", "", network)
	}
	return response.OK(w, HttpResponse{Data: options})
}
