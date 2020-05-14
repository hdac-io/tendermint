package state

import (
	amino "github.com/tendermint/go-amino"

	cryptoamino "github.com/hdac-io/tendermint/crypto/encoding/amino"
)

var cdc = amino.NewCodec()

func init() {
	cryptoamino.RegisterAmino(cdc)
}
