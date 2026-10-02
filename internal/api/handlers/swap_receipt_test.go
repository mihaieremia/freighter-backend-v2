package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stellar/freighter-backend-v2/internal/api/httperror"
	"github.com/stellar/freighter-backend-v2/internal/metrics"
	"github.com/stellar/freighter-backend-v2/internal/types"
)

type fakeSwapReceiptService struct {
	types.SwapReceiptService
	called bool
	err    error
}

func (s *fakeSwapReceiptService) GetSwapReceipt(ctx context.Context, network, hash, viewer string, index int) (*types.SwapReceipt, error) {
	s.called = true
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > SwapReceiptContextTimeout {
		panic("receipt call must have a five-second deadline")
	}
	return &types.SwapReceipt{Network: network, TransactionHash: hash, Viewer: viewer, OperationIndex: index, Status: "confirmed", TokenOut: testContractAddress, ReceivedAtoms: "900719925474099300000"}, s.err
}

func receiptRequest(hash, query string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/swap/receipt/"+hash+"?"+query, nil)
	r.SetPathValue("transactionHash", hash)
	return r
}

func TestSwapReceipt_IdentityAmountsAndDeadline(t *testing.T) {
	svc := &fakeSwapReceiptService{}
	rr := httptest.NewRecorder()
	hash := strings.Repeat("AB", 32)
	require.NoError(t, NewSwapReceiptHandler(svc).GetSwapReceipt(rr, receiptRequest(hash, "network=TESTNET&viewer="+testAddress+"&operationIndex=2")))
	var result struct {
		Data types.SwapReceipt `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &result))
	assert.Equal(t, types.SwapReceipt{Network: types.TESTNET, TransactionHash: strings.ToLower(hash), Viewer: testAddress, OperationIndex: 2, Status: "confirmed", TokenOut: testContractAddress, ReceivedAtoms: "900719925474099300000"}, result.Data)
	assert.True(t, svc.called)
}

func TestSwapReceipt_RejectsAmbiguousOrInvalidInputsBeforeUpstream(t *testing.T) {
	hash := strings.Repeat("ab", 32)
	query := "network=PUBLIC&viewer=" + testAddress + "&operationIndex=0"
	for _, tc := range []struct{ name, hash, query string }{
		{"missing network", hash, strings.Replace(query, "network=PUBLIC", "", 1)},
		{"unknown network", hash, strings.Replace(query, "PUBLIC", "FUTURENET", 1)},
		{"duplicate network", hash, query + "&network=TESTNET"},
		{"invalid viewer", hash, strings.Replace(query, testAddress, "Gbad", 1)},
		{"duplicate viewer", hash, query + "&viewer=" + testAddress},
		{"missing index", hash, strings.Replace(query, "&operationIndex=0", "", 1)},
		{"negative index", hash, strings.Replace(query, "operationIndex=0", "operationIndex=-1", 1)},
		{"too large index", hash, strings.Replace(query, "operationIndex=0", "operationIndex=100", 1)},
		{"fractional index", hash, strings.Replace(query, "operationIndex=0", "operationIndex=1.0", 1)},
		{"duplicate index", hash, query + "&operationIndex=1"},
		{"short hash", "ab", query},
		{"nonhex hash", strings.Repeat("gg", 32), query},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeSwapReceiptService{}
			err := NewSwapReceiptHandler(svc).GetSwapReceipt(httptest.NewRecorder(), receiptRequest(tc.hash, tc.query))
			var herr *httperror.HttpError
			require.ErrorAs(t, err, &herr)
			assert.Equal(t, http.StatusBadRequest, herr.StatusCode)
			assert.False(t, svc.called)
		})
	}
}

func TestSwapReceipt_ServiceErrorsAreMapped(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{
		{&metrics.UpstreamError{Kind: "http_error", Err: context.DeadlineExceeded}, http.StatusGatewayTimeout},
		{&metrics.UpstreamError{Kind: "http_error", Code: 503}, http.StatusBadGateway},
		{context.Canceled, http.StatusServiceUnavailable},
	} {
		svc := &fakeSwapReceiptService{err: tc.err}
		err := NewSwapReceiptHandler(svc).GetSwapReceipt(httptest.NewRecorder(), receiptRequest(strings.Repeat("ab", 32), "network=PUBLIC&viewer="+testAddress+"&operationIndex=0"))
		var herr *httperror.HttpError
		require.ErrorAs(t, err, &herr)
		assert.Equal(t, tc.status, herr.StatusCode)
	}
}
