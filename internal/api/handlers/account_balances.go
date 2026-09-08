package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/stellar/go-stellar-sdk/strkey"

	"github.com/stellar/freighter-backend-v2/internal/api/httperror"
	response "github.com/stellar/freighter-backend-v2/internal/api/httpresponse"
	"github.com/stellar/freighter-backend-v2/internal/api/middleware"
	"github.com/stellar/freighter-backend-v2/internal/types"
)

type AccountBalancesHandler struct {
	WalletBackendService types.WalletBackendService
	MaxAddresses         int
}

func NewAccountBalancesHandler(walletBackendService types.WalletBackendService, maxAddresses int) *AccountBalancesHandler {
	return &AccountBalancesHandler{
		WalletBackendService: walletBackendService,
		MaxAddresses:         maxAddresses,
	}
}

// AddressesRequest is the body shared by the address-list endpoints:
// /accounts/balances and /accounts/positions.
type AddressesRequest struct {
	Addresses []string `json:"addresses"`
}

func validateAddressesRequest(r *http.Request, maxAddresses int) (*AddressesRequest, *httperror.HttpError) {
	var req AddressesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if middleware.IsMaxBytesError(err) {
			return nil, httperror.RequestEntityTooLarge("Request body too large", err)
		}
		return nil, httperror.BadRequest(fmt.Sprintf("invalid JSON: %s", err.Error()), err)
	}

	if len(req.Addresses) == 0 {
		errStr := "addresses array cannot be empty"
		return nil, httperror.BadRequest(errStr, errors.New(errStr))
	}

	if maxAddresses > 0 && len(req.Addresses) > maxAddresses {
		errStr := fmt.Sprintf("too many addresses: maximum is %d, got %d", maxAddresses, len(req.Addresses))
		return nil, httperror.BadRequest(errStr, errors.New(errStr))
	}

	// Validate each address is a valid Stellar address
	for _, addr := range req.Addresses {
		if _, err := strkey.Decode(strkey.VersionByteAccountID, addr); err != nil {
			return nil, httperror.BadRequest(fmt.Sprintf("invalid Stellar address %s: %s", addr, err.Error()), err)
		}
	}

	return &req, nil
}

// GetAccountBalances handles fetching account balances from wallet backend
func (h *AccountBalancesHandler) GetAccountBalances(w http.ResponseWriter, r *http.Request) error {
	contextWithTimeout, cancel := context.WithTimeout(r.Context(), WalletBackendContextTimeout)
	defer cancel()

	network, networkErr := walletBackendNetworkFromQuery(r)
	if networkErr != nil {
		return networkErr
	}

	req, validationErr := validateAddressesRequest(r, h.MaxAddresses)
	if validationErr != nil {
		return validationErr
	}

	balances, err := h.WalletBackendService.GetBalancesByAccountAddresses(contextWithTimeout, req.Addresses, network)
	if err != nil {
		// address is intentionally empty: this is a multi-address fan-out
		// endpoint, and individual ErrAccountNotFound outcomes are already
		// surfaced as is_funded:false inside a 200 body. The top-level
		// err here is only ever a systemic failure (graphql_error, http_error,
		// connection, timeout, internal).
		return translateServiceError(r.Context(), err, "account balances", "", network)
	}

	responseData := HttpResponse{
		Data: balances,
	}

	w.Header().Set("Content-Type", "application/json")
	return response.OK(w, responseData)
}
