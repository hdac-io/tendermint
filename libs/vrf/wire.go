package vrf

import (
	"crypto/elliptic"

	amino "github.com/tendermint/go-amino"
)

// RegisterAmino regist interface to marshaling library amino
func RegisterAmino(cdc *amino.Codec) {
	cdc.RegisterInterface((*PrivateKey)(nil), nil)
	cdc.RegisterInterface((*PublicKey)(nil), nil)
	cdc.RegisterInterface((*elliptic.Curve)(nil), nil)
}
