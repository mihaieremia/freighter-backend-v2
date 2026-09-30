package types

import "context"

// SwapReceipt reports confirmed execution output, never a quoted minimum.
type SwapReceipt struct {
	Network         string `json:"network"`
	TransactionHash string `json:"transactionHash"`
	Viewer          string `json:"viewer"`
	OperationIndex  int    `json:"operationIndex"`
	Status          string `json:"status"`
	TokenOut        string `json:"tokenOut,omitempty"`
	ReceivedAtoms   string `json:"receivedAtoms,omitempty"`
}

type SwapReceiptService interface {
	Service
	GetSwapReceipt(ctx context.Context, network, transactionHash, viewer string, operationIndex int) (*SwapReceipt, error)
}
