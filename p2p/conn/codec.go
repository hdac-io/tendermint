package conn

import (
	amino "github.com/tendermint/go-amino"

	cryptoamino "github.com/hdac-io/tendermint/crypto/encoding/amino"
)

var cdc *amino.Codec = amino.NewCodec()

func init() {
	cryptoamino.RegisterAmino(cdc)
	RegisterPacket(cdc)
}
