// ABOUTME: Tests for the positions service: the account-level aggregate,
// ABOUTME: address dedupe, empty accounts, and upstream error propagation.
package services

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stellar/freighter-backend-v2/internal/types"
	"github.com/stellar/freighter-backend-v2/internal/utils"
)

func TestAccountAggregate(t *testing.T) {
	pool := func(usd, supplied, apy *float64) types.PoolPosition {
		return types.PoolPosition{NetUSD: usd, SuppliedUSD: supplied, NetAPY: apy}
	}

	t.Run("supplied-weighted mean across pools", func(t *testing.T) {
		total, apy := accountAggregate([]types.PoolPosition{
			pool(f64(8000), f64(9000), f64(0.05)),
			pool(f64(900), f64(1000), f64(0.01)),
		})
		require.NotNil(t, total)
		assert.InDelta(t, 8900, *total, 1e-9) // the total sums net values...
		require.NotNil(t, apy)
		assert.InDelta(t, 0.046, *apy, 1e-9) // ...but the rate weights by supplied
	})

	t.Run("strict null: one unpriced pool nulls the header", func(t *testing.T) {
		total, apy := accountAggregate([]types.PoolPosition{
			pool(f64(9000), f64(9000), f64(0.05)),
			pool(nil, nil, nil),
		})
		assert.Nil(t, total)
		assert.Nil(t, apy)
	})

	t.Run("null netApy nulls the rate but keeps the total", func(t *testing.T) {
		total, apy := accountAggregate([]types.PoolPosition{
			pool(f64(9000), f64(9000), f64(0.05)),
			pool(f64(1000), f64(1000), nil),
		})
		require.NotNil(t, total)
		assert.InDelta(t, 10000, *total, 1e-9)
		assert.Nil(t, apy)
	})

	t.Run("null suppliedUsd nulls the rate but keeps the total", func(t *testing.T) {
		total, apy := accountAggregate([]types.PoolPosition{
			pool(f64(9000), f64(9000), f64(0.05)),
			pool(f64(1000), nil, f64(0.01)),
		})
		require.NotNil(t, total)
		assert.InDelta(t, 10000, *total, 1e-9)
		assert.Nil(t, apy)
	})

	t.Run("no positions is a genuine zero, apy null", func(t *testing.T) {
		total, apy := accountAggregate(nil)
		require.NotNil(t, total)
		assert.Equal(t, 0.0, *total)
		assert.Nil(t, apy)
	})

	t.Run("zero supplied base yields null apy", func(t *testing.T) {
		total, apy := accountAggregate([]types.PoolPosition{
			pool(f64(0), f64(0), f64(0.05)),
		})
		require.NotNil(t, total)
		assert.Equal(t, 0.0, *total)
		assert.Nil(t, apy)
	})
}

func TestGetAccountsPositionsEmptyAccountAndDedupe(t *testing.T) {
	svc := NewPositionsService(&utils.MockWalletBackendService{}, fixedMarkets(nil), 10, nil)

	// Duplicates collapse, first-seen order preserved — like balances.
	results, err := svc.GetAccountsPositions(context.Background(), []string{xoxnoTestAddress, xoxnoTestAddress}, types.TESTNET)
	require.NoError(t, err)
	require.Len(t, results, 1)
	got := results[0]
	assert.Equal(t, xoxnoTestAddress, got.Address)
	assert.NotNil(t, got.Positions)
	assert.Empty(t, got.Positions)
	require.NotNil(t, got.TotalValueUSD)
	assert.Equal(t, 0.0, *got.TotalValueUSD)
	assert.Nil(t, got.NetAPY)
}

func TestGetAccountsPositionsUpstreamError(t *testing.T) {
	upErr := errors.New("wallet-backend on fire")

	t.Run("positions fetch failure fails the request", func(t *testing.T) {
		svc := NewPositionsService(&utils.MockWalletBackendService{GetXoxnoLendingPositionsError: upErr}, fixedMarkets(nil), 10, nil)
		_, err := svc.GetAccountsPositions(context.Background(), []string{xoxnoTestAddress}, types.TESTNET)
		assert.ErrorIs(t, err, upErr)
	})

	t.Run("market catalog failure fails the request", func(t *testing.T) {
		svc := NewPositionsService(&utils.MockWalletBackendService{}, failingMarkets{upErr}, 10, nil)
		_, err := svc.GetAccountsPositions(context.Background(), []string{xoxnoTestAddress}, types.TESTNET)
		assert.ErrorIs(t, err, upErr)
	})
}
