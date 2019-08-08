package p2p

import (
	amino "github.com/tendermint/go-amino"
	cryptoAmino "github.com/tendermint/tendermint/crypto/encoding/amino"
	"github.com/tendermint/tendermint/libs/vrf"
	"github.com/tendermint/tendermint/libs/vrf/p256"
)

var cdc = amino.NewCodec()

func init() {
	cryptoAmino.RegisterAmino(cdc)
	vrf.RegisterAmino(cdc)
	p256.RegisterAmino(cdc)
}
