package swap

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/stellar/go-stellar-sdk/xdr"
)

const (
	// routePayloadVersion is the wire version of the router's packed program
	// (contracts/swap-aggregator/src/program.rs). The router rejects any other.
	routePayloadVersion = 1
	routeHeaderLen      = 10

	routeHdrVersion  = 0
	routeHdrTokenIn  = 1
	routeHdrTokenOut = 2
	routeHdrMinOut   = 3
	routeHdrReferral = 4
)

// checkRoutePayload verifies the fields of a router strategy payload that decide
// what the sender receives: the input and output token, and a minimum output no
// lower than minOutFloor. The router pays the output to the sender and reverts below
// the minimum. The swap instructions themselves are left to the router.
//
// The payload is the ScVal-encoded struct {amounts: Vec<i128>, assets:
// Vec<Address>, ops: Bytes}; ops starts with the packed header described in the
// router's program.rs.
func checkRoutePayload(payloadXDR []byte, tokenIn, tokenOut string, minOutFloor *big.Int) error {
	fields, err := decodeRoutePayload(payloadXDR)
	if err != nil {
		return err
	}
	header, err := routeHeader(fields["ops"])
	if err != nil {
		return err
	}

	got, err := vecAt(fields["assets"], int(header[routeHdrTokenIn]), scValAddress)
	if err != nil {
		return fmt.Errorf("route payload input token: %w", err)
	}
	if got != tokenIn {
		return errors.New("route payload input token is not the source token")
	}
	got, err = vecAt(fields["assets"], int(header[routeHdrTokenOut]), scValAddress)
	if err != nil {
		return fmt.Errorf("route payload output token: %w", err)
	}
	if got != tokenOut {
		return errors.New("route payload output token is not the destination token")
	}
	minOut, err := vecAt(fields["amounts"], int(header[routeHdrMinOut]), scValI128)
	if err != nil {
		return fmt.Errorf("route payload minimum output: %w", err)
	}
	if minOut.Cmp(minOutFloor) < 0 {
		return errors.New("route payload promises less than the quoted minimum output")
	}
	return nil
}

// decodeRoutePayload decodes the payload struct and requires exactly its three
// named fields.
func decodeRoutePayload(payloadXDR []byte) (map[string]xdr.ScVal, error) {
	var v xdr.ScVal
	if err := xdr.SafeUnmarshal(payloadXDR, &v); err != nil {
		return nil, fmt.Errorf("decoding route payload: %w", err)
	}
	if v.Type != xdr.ScValTypeScvMap || v.Map == nil || *v.Map == nil {
		return nil, errors.New("route payload is not a struct")
	}

	fields := make(map[string]xdr.ScVal, 3)
	for _, entry := range **v.Map {
		if entry.Key.Type != xdr.ScValTypeScvSymbol || entry.Key.Sym == nil {
			return nil, errors.New("route payload has a non-symbol field name")
		}
		name := string(*entry.Key.Sym)
		if name != "amounts" && name != "assets" && name != "ops" {
			return nil, fmt.Errorf("route payload has unexpected field %q", name)
		}
		fields[name] = entry.Val
	}
	if len(fields) != 3 {
		return nil, errors.New("route payload is missing a field")
	}
	return fields, nil
}

// routeHeader returns the packed header at the start of ops after checking its
// length, version and that it carries no referral.
func routeHeader(ops xdr.ScVal) ([]byte, error) {
	if ops.Type != xdr.ScValTypeScvBytes || ops.Bytes == nil {
		return nil, errors.New("route payload ops is not bytes")
	}
	header := []byte(*ops.Bytes)
	if len(header) < routeHeaderLen {
		return nil, errors.New("route payload header is truncated")
	}
	if header[routeHdrVersion] != routePayloadVersion {
		return nil, fmt.Errorf("route payload version %d is not supported", header[routeHdrVersion])
	}
	if referral := header[routeHdrReferral : routeHdrReferral+4]; referral[0]|referral[1]|referral[2]|referral[3] != 0 {
		return nil, errors.New("route payload carries a referral")
	}
	return header, nil
}

func vecAt[T any](v xdr.ScVal, idx int, conv func(xdr.ScVal) (T, error)) (zero T, _ error) {
	if v.Type != xdr.ScValTypeScvVec || v.Vec == nil || *v.Vec == nil {
		return zero, errors.New("registry is not a vector")
	}
	vec := **v.Vec
	if idx < 0 || idx >= len(vec) {
		return zero, fmt.Errorf("index %d is outside the registry", idx)
	}
	return conv(vec[idx])
}
