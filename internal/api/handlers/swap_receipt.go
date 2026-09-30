package handlers

import (
	"context"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/stellar/freighter-backend-v2/internal/api/httperror"
	response "github.com/stellar/freighter-backend-v2/internal/api/httpresponse"
	"github.com/stellar/freighter-backend-v2/internal/types"
	"github.com/stellar/freighter-backend-v2/internal/utils"
)

const SwapReceiptContextTimeout = 5 * time.Second

type SwapReceiptHandler struct{ SwapReceiptService types.SwapReceiptService }

func NewSwapReceiptHandler(svc types.SwapReceiptService) *SwapReceiptHandler {
	return &SwapReceiptHandler{SwapReceiptService: svc}
}

func (h *SwapReceiptHandler) GetSwapReceipt(w http.ResponseWriter, r *http.Request) error {
	query := r.URL.Query()
	network, viewer := query.Get("network"), query.Get("viewer")
	if len(query["network"]) != 1 || !isValidWalletBackendNetwork(network) {
		return httperror.BadRequestf("network must be PUBLIC or TESTNET")
	}
	if len(query["viewer"]) != 1 || !utils.IsValidStellarPublicKey(viewer) {
		return httperror.BadRequestf("viewer must be a Stellar account address")
	}
	hash := r.PathValue("transactionHash")
	if len(hash) != 64 {
		return httperror.BadRequestf("transactionHash must be 64 hexadecimal characters")
	}
	if _, err := hex.DecodeString(hash); err != nil {
		return httperror.BadRequestf("transactionHash must be 64 hexadecimal characters")
	}
	hash = strings.ToLower(hash)
	index, err := strconv.ParseUint(query.Get("operationIndex"), 10, 31)
	if len(query["operationIndex"]) != 1 || err != nil || index >= 100 {
		return httperror.BadRequestf("operationIndex must be a zero-based index between 0 and 99")
	}
	ctx, cancel := context.WithTimeout(r.Context(), SwapReceiptContextTimeout)
	defer cancel()
	receipt, err := h.SwapReceiptService.GetSwapReceipt(ctx, network, hash, viewer, int(index))
	if err != nil {
		return translateServiceError(r.Context(), err, "swap receipt", viewer, network)
	}
	w.Header().Set("Content-Type", "application/json")
	return response.OK(w, HttpResponse{Data: receipt})
}
