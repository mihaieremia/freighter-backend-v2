// ABOUTME: Tests for the XOXNO hub and spoke display names: the per-network
// ABOUTME: lookups and the fallback for an id with no configured name.
package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stellar/freighter-backend-v2/internal/types"
)

func TestXoxnoHubName(t *testing.T) {
	// Names differ per network for the same id, so a table shared across
	// networks would mislabel one of them.
	assert.Equal(t, "Core", xoxnoHubName(types.PUBLIC, 1))
	assert.Equal(t, "Main", xoxnoHubName(types.TESTNET, 1))

	// Governance can add a hub before this table catches up; the fallback
	// names it by number rather than borrowing another hub's name.
	assert.Equal(t, "Hub 7", xoxnoHubName(types.PUBLIC, 7))
	assert.Equal(t, "Hub 1", xoxnoHubName("FUTURENET", 1))
}

func TestXoxnoSpokeName(t *testing.T) {
	// IDs come from the deployment, not the config file's keys: pubnet's
	// disabled "Spiko RWA" is authored at key 3 but never created, so the
	// chain's spoke 3 is the one authored at key 4.
	name := xoxnoSpokeName(types.PUBLIC, 3)
	require.NotNil(t, name)
	assert.Equal(t, "Centrifuge RWA", *name)

	name = xoxnoSpokeName(types.PUBLIC, 4)
	require.NotNil(t, name)
	assert.Equal(t, "Stables & FX", *name)

	// Nothing is deployed at the id the last config key would suggest.
	assert.Nil(t, xoxnoSpokeName(types.PUBLIC, 9))

	// The config appends the assets a spoke was set up with; only the name
	// itself is carried, so this reads "Main", not "Main (USDC/EURC/XLM/BTC)".
	name = xoxnoSpokeName(types.TESTNET, 1)
	require.NotNil(t, name)
	assert.Equal(t, "Main", *name)

	name = xoxnoSpokeName(types.TESTNET, 4)
	require.NotNil(t, name)
	assert.Equal(t, "LP Tokens", *name)

	// A spoke name is supplementary to its id, so an unknown one reports
	// nothing rather than a placeholder the client would render as real.
	assert.Nil(t, xoxnoSpokeName(types.PUBLIC, 99))
	assert.Nil(t, xoxnoSpokeName("FUTURENET", 1))
}

func TestXoxnoSpokeNameIsCopiedPerCall(t *testing.T) {
	// The map values are shared; returning &value from a range variable would
	// alias every caller onto one string.
	a := xoxnoSpokeName(types.PUBLIC, 1)
	b := xoxnoSpokeName(types.PUBLIC, 2)
	require.NotNil(t, a)
	require.NotNil(t, b)
	assert.NotEqual(t, *a, *b)
}
