// ABOUTME: Tests for depositableSpokes: which spokes accept a deposit of a
// ABOUTME: market, and the order a client should prefer them in.
package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stellar/freighter-backend-v2/internal/types"
	wbtypes "github.com/stellar/wallet-backend/pkg/wbclient/types"
)

func TestDepositableSpokes(t *testing.T) {
	market := func(reserves ...wbtypes.XoxnoLendingReserve) wbtypes.XoxnoLendingMarket {
		return wbtypes.XoxnoLendingMarket{HubID: 1, Asset: "CUSDC", Reserves: reserves}
	}

	t.Run("orders by id", func(t *testing.T) {
		// Every spoke earns the same rate — it belongs to the market — so
		// there is nothing to rank them by; id keeps the answer stable.
		got := depositableSpokes(types.PUBLIC, market(
			wbtypes.XoxnoLendingReserve{SpokeID: 5, IsCollateralizable: true},
			wbtypes.XoxnoLendingReserve{SpokeID: 4, IsCollateralizable: true},
			wbtypes.XoxnoLendingReserve{SpokeID: 2, IsCollateralizable: true},
		))

		require.Len(t, got, 3)
		assert.Equal(t, []int32{2, 4, 5}, []int32{got[0].ID, got[1].ID, got[2].ID})
	})

	t.Run("leaves out a spoke the contract would refuse", func(t *testing.T) {
		// A spoke that lists the asset only to be borrowed refuses the supply
		// outright — require_can_supply asserts is_collateralizable — so it is
		// as undepositable as a paused or frozen one.
		got := depositableSpokes(types.PUBLIC, market(
			wbtypes.XoxnoLendingReserve{SpokeID: 1, IsCollateralizable: true, Paused: true},
			wbtypes.XoxnoLendingReserve{SpokeID: 2, IsCollateralizable: true, Frozen: true},
			wbtypes.XoxnoLendingReserve{SpokeID: 4, IsCollateralizable: false},
			wbtypes.XoxnoLendingReserve{SpokeID: 3, IsCollateralizable: true},
		))

		require.Len(t, got, 1)
		assert.Equal(t, int32(3), got[0].ID)
	})

	t.Run("names the spoke and carries what actually differs between them", func(t *testing.T) {
		got := depositableSpokes(types.PUBLIC, market(
			wbtypes.XoxnoLendingReserve{
				SpokeID: 1, IsCollateralizable: true,
				LoanToValueBps: 7500, LiquidationThresholdBps: 7800,
			},
		))

		require.Len(t, got, 1)
		require.NotNil(t, got[0].Name)
		assert.Equal(t, "Blue Chip", *got[0].Name)
		assert.Equal(t, int32(7500), got[0].LoanToValueBps)
		assert.Equal(t, int32(7800), got[0].LiquidationThresholdBps)
	})
}
