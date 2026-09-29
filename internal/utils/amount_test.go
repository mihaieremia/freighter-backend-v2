package utils

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDecimalAmount(t *testing.T) {
	t.Parallel()
	ok := map[string]int64{"1": 1_0000000, "0.0000001": 1, "12.5": 125_000000, "0012": 12_0000000}
	for in, want := range ok {
		got, err := ParseDecimalAmount(in, 7)
		require.NoError(t, err, in)
		assert.Equal(t, big.NewInt(want), got, in)
	}
	for _, in := range []string{"", "0", "0.0", "-1", "+1", "1e3", "1.", ".5", "1.23456789", "abc", "1 ", "0x10", "1,5"} {
		_, err := ParseDecimalAmount(in, 7)
		assert.Error(t, err, in)
	}
	got, err := ParseDecimalAmount("1.5", 18)
	require.NoError(t, err)
	assert.Equal(t, "1500000000000000000", got.String())
	_, err = ParseDecimalAmount("1", 39)
	assert.Error(t, err)
}
