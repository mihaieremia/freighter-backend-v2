// ABOUTME: Display names for XOXNO's hubs and spokes, mirroring the
// ABOUTME: protocol's own per-network config files.
package services

import (
	"fmt"

	"github.com/stellar/freighter-backend-v2/internal/types"
)

// Display names for XOXNO's hubs and spokes, mirroring
// rs-lending-xlm/configs/<network>/{hubs,spokes}.json.
//
// The config's own spoke names sometimes append the asset list they were set
// up with ("Main (USDC/EURC/XLM/BTC)"); that reads as setup notes rather than
// a name, and the assets are already listed underneath it wherever the name
// is shown, so only the name itself is carried here.
//
// Take the spoke IDs from the deployment, never from the config file's keys.
// Those keys are authoring order, and a spoke marked disabled is never
// created on chain, so every spoke authored after it is deployed one ID
// lower. Pubnet's disabled "Spiko RWA" sits at key 3, which is why the chain
// answers "Centrifuge RWA" there — and why reading the keys straight across
// mislabelled every RWA spoke. Verified against
// https://api.xoxno.com/stellar-lending/spokes.
//
// The chain carries only the numeric ids, and governance can add a hub or a
// spoke without this table changing, so an unknown id must never be given a
// name that looks authoritative. Hubs fall back to their number; spokes report
// no name at all, since a spoke name is supplementary to its id.

var xoxnoHubNames = map[string]map[int32]string{
	types.PUBLIC: {
		1: "Core",
		2: "RWA",
		3: "AMM",
	},
	types.TESTNET: {
		1: "Main",
		2: "Secondary",
		3: "Aquarius",
	},
}

var xoxnoSpokeNames = map[string]map[int32]string{
	types.PUBLIC: {
		1: "Blue Chip",
		2: "Etherfuse RWA",
		3: "Centrifuge RWA",
		4: "Stables & FX",
		5: "AMM Collateral",
		6: "Ondo RWA",
		7: "Commodities",
		8: "Aquarius Ecosystem",
	},
	types.TESTNET: {
		1: "Main",
		2: "XLM + USDC",
		3: "Full",
		4: "LP Tokens",
	},
}

// xoxnoHubName names a liquidity hub, falling back to its id so a hub added
// after this table was written still renders something a user can match to
// the protocol's own UI.
func xoxnoHubName(network string, hubID int32) string {
	if name, ok := xoxnoHubNames[network][hubID]; ok {
		return name
	}
	return fmt.Sprintf("Hub %d", hubID)
}

// xoxnoSpokeName names a risk spoke, or returns nil when the id is not one
// this build knows.
func xoxnoSpokeName(network string, spokeID int32) *string {
	if name, ok := xoxnoSpokeNames[network][spokeID]; ok {
		return &name
	}
	return nil
}
