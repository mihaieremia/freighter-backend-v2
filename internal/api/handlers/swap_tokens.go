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

// SwapTokensHandler serves the swap tokens route.
type SwapTokensHandler struct {
	SwapTokensService types.SwapTokensService
}

// NewSwapTokensHandler returns a handler that lists swappable tokens from svc.
func NewSwapTokensHandler(svc types.SwapTokensService) *SwapTokensHandler {
	return &SwapTokensHandler{SwapTokensService: svc}
}

// swapTokensScopeAll selects the whole XOXNO catalog instead of the swappable list.
const swapTokensScopeAll = "all"

// GetSwapTokens lists the Soroban tokens the swap aggregator can route, with a
// price backed by enough liquidity to show. With scope=all it lists every token
// XOXNO registers instead, with a logo and a price where one is known.
func (h *SwapTokensHandler) GetSwapTokens(w http.ResponseWriter, r *http.Request) error {
	ctx, cancel := context.WithTimeout(r.Context(), SwapContextTimeout)
	defer cancel()

	network := r.URL.Query().Get("network")
	if !isValidWalletBackendNetwork(network) {
		return httperror.BadRequest(fmt.Sprintf("invalid network: must be %s or %s", types.PUBLIC, types.TESTNET), errors.New("invalid network"))
	}

	scope := r.URL.Query().Get("scope")
	if scope != "" && scope != swapTokensScopeAll {
		return httperror.BadRequest(fmt.Sprintf("invalid scope: must be omitted or %q", swapTokensScopeAll), errors.New("invalid scope"))
	}

	var data any
	var err error
	if scope == swapTokensScopeAll {
		data, err = h.SwapTokensService.GetTokenCatalog(ctx, network)
	} else {
		data, err = h.SwapTokensService.GetSwapTokens(ctx, network)
	}
	if err != nil {
		return translateServiceError(r.Context(), err, "swap tokens", "", network)
	}

	w.Header().Set("Content-Type", "application/json")
	return response.OK(w, HttpResponse{Data: data})
}
