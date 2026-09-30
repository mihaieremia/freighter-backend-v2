package services

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stellar/freighter-backend-v2/internal/types"
)

func TestStellarExpertReceipt_UsesExistingTransportAndOneRequest(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNotFound, http.StatusBadRequest, http.StatusFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				assert.Equal(t, "/explorer/testnet/tx/"+strings.Repeat("ab", 32), r.URL.Path)
				assert.Equal(t, "Bearer secret", r.Header.Get("Authorization"))
				assert.Equal(t, "https://freighter.app", r.Header.Get("Origin"))
				if status == http.StatusFound {
					w.Header().Set("Location", "/redirected")
				}
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"meta":"confirmed-meta"}`))
			}))
			defer srv.Close()
			svc := NewStellarExpertService("invalid-public", srv.URL+"/explorer/testnet/", "secret", "https://freighter.app", nil)
			meta, err := svc.GetTransactionMeta(context.Background(), types.TESTNET, strings.Repeat("ab", 32))
			assert.Equal(t, 1, calls)
			switch status {
			case http.StatusOK:
				require.NoError(t, err)
				assert.Equal(t, "confirmed-meta", meta)
			case http.StatusNotFound:
				require.NoError(t, err)
				assert.Empty(t, meta)
			default:
				require.Error(t, err)
				assert.False(t, errors.Is(err, ErrAssetMalformed))
				assert.False(t, errors.Is(err, ErrAssetNotFound))
			}
		})
	}
}

func TestStellarExpertReceipt_BoundsBodyAndHonorsCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"meta":"` + strings.Repeat("a", 1<<20) + `"}`))
	}))
	defer srv.Close()
	svc := NewStellarExpertService(srv.URL, "", "", "", nil)
	_, err := svc.GetTransactionMeta(context.Background(), types.PUBLIC, "hash")
	require.Error(t, err)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err = svc.GetTransactionMeta(ctx, types.PUBLIC, "hash")
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}
