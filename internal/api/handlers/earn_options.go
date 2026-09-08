// ABOUTME: Handler for GET /protocols/{protocol}/earn-options: the asset-first
// ABOUTME: "where can I earn this" catalog, one instance per protocol.
package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/stellar/freighter-backend-v2/internal/api/httperror"
	response "github.com/stellar/freighter-backend-v2/internal/api/httpresponse"
	"github.com/stellar/freighter-backend-v2/internal/types"
)

// EarnOptionsHandler serves one protocol's earn catalog. Protocol names the
// protocol in error context ("blend", "xoxno").
type EarnOptionsHandler struct {
	Protocol string
	Service  types.EarnCatalogService
}

func NewEarnOptionsHandler(protocol string, svc types.EarnCatalogService) *EarnOptionsHandler {
	return &EarnOptionsHandler{Protocol: protocol, Service: svc}
}

// GetEarnOptions handles GET /api/v1/protocols/{protocol}/earn-options.
func (h *EarnOptionsHandler) GetEarnOptions(w http.ResponseWriter, r *http.Request) error {
	ctx, cancel := context.WithTimeout(r.Context(), catalogContextTimeout)
	defer cancel()

	network := r.URL.Query().Get("network")
	if !isValidWalletBackendNetwork(network) {
		return httperror.BadRequest(fmt.Sprintf("invalid network: must be %s or %s", types.PUBLIC, types.TESTNET), errors.New("invalid network"))
	}

	options, err := h.Service.GetEarnOptions(ctx, network)
	if err != nil {
		return translateServiceError(r.Context(), err, h.Protocol+" earn options", "", network)
	}
	return response.OK(w, HttpResponse{Data: options})
}
