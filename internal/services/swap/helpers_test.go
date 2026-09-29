package swap

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/stellar/freighter-backend-v2/internal/types"
)

const testIssuer = "GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN"

func testContract(b byte) string {
	raw := make([]byte, 32)
	raw[0] = b
	return strkey.MustEncode(strkey.VersionByteContract, raw)
}

// testContractN is a distinct contract id for every i, none equal to testContract.
func testContractN(i int) string {
	raw := make([]byte, 32)
	raw[0], raw[1], raw[2] = 0xFF, byte(i>>8), byte(i)
	return strkey.MustEncode(strkey.VersionByteContract, raw)
}

// mustAddr unwraps a utils address decoder for a fixture value known to be valid.
func mustAddr(a *xdr.ScAddress, err error) xdr.ScAddress {
	if err != nil {
		panic(err)
	}
	return *a
}

// fakeStellarExpert answers GetContractAsset from a programmable table. Any
// contract without an entry is an error, never an empty success.
type fakeStellarExpert struct {
	types.StellarExpertService

	mu     sync.Mutex
	assets map[string]string
	errs   map[string]error
	calls  map[string]int
}

func newFakeStellarExpert() *fakeStellarExpert {
	return &fakeStellarExpert{
		assets: map[string]string{},
		errs:   map[string]error{},
		calls:  map[string]int{},
	}
}

func (f *fakeStellarExpert) GetContractAsset(_ context.Context, _, contractID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[contractID]++
	if err := f.errs[contractID]; err != nil {
		return "", err
	}
	asset, ok := f.assets[contractID]
	if !ok {
		return "", fmt.Errorf("fake stellar expert: %w: %s", errUnknownContract, contractID)
	}
	return asset, nil
}

var errUnknownContract = errors.New("contract not programmed")

// SetContractAsset sets the classic asset a contract wraps; "" is a contract
// that wraps none.
func (f *fakeStellarExpert) SetContractAsset(contractID, asset string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.assets[contractID] = asset
}

func (f *fakeStellarExpert) SetContractErr(contractID string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs[contractID] = err
}

func (f *fakeStellarExpert) ContractCallCount(contractID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[contractID]
}
