package bls

import (
	"github.com/hdac-io/tendermint/crypto"
	amino "github.com/tendermint/go-amino"
)

const (
	PrivKeyAminoName = "tendermint/PrivKeyBls12_381"
	PubKeyAminoName  = "tendermint/PubKeyBls12_381"
)

var cdc = amino.NewCodec()

func init() {
	cdc.RegisterInterface((*crypto.PrivKey)(nil), nil)
	cdc.RegisterConcrete(PrivKeyBls{}, PrivKeyAminoName, nil)

	cdc.RegisterInterface((*crypto.PubKey)(nil), nil)
	cdc.RegisterConcrete(PubKeyBls{}, PubKeyAminoName, nil)
}
