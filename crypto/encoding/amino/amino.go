package cryptoAmino

import (
	"reflect"

	"github.com/hdac-io/tendermint/crypto/bls"

	amino "github.com/tendermint/go-amino"
	"github.com/hdac-io/tendermint/crypto"
	"github.com/hdac-io/tendermint/crypto/ed25519"
	"github.com/hdac-io/tendermint/crypto/multisig"
	"github.com/hdac-io/tendermint/crypto/secp256k1"
)

var cdc = amino.NewCodec()

// nameTable is used to map public key concrete types back
// to their registered amino names. This should eventually be handled
// by amino. Example usage:
// nameTable[reflect.TypeOf(ed25519.PubKeyEd25519{})] = ed25519.PubKeyAminoName
var nameTable = make(map[reflect.Type]string, 4)

func init() {
	// NOTE: It's important that there be no conflicts here,
	// as that would change the canonical representations,
	// and therefore change the address.
	// TODO: Remove above note when
	// https://github.com/tendermint/go-amino/issues/9
	// is resolved
	RegisterAmino(cdc)

	// TODO: Have amino provide a way to go from concrete struct to route directly.
	// Its currently a private API
	nameTable[reflect.TypeOf(ed25519.PubKeyEd25519{})] = ed25519.PubKeyAminoName
	nameTable[reflect.TypeOf(secp256k1.PubKeySecp256k1{})] = secp256k1.PubKeyAminoName
	nameTable[reflect.TypeOf(multisig.PubKeyMultisigThreshold{})] = multisig.PubKeyMultisigThresholdAminoRoute
	nameTable[reflect.TypeOf(bls.PubKeyBls{})] = bls.PubKeyAminoName
}

// PubkeyAminoName returns the amino route of a pubkey
// cdc is currently passed in, as eventually this will not be using
// a package level codec.
func PubkeyAminoName(cdc *amino.Codec, key crypto.PubKey) (string, bool) {
	route, found := nameTable[reflect.TypeOf(key)]
	return route, found
}

// RegisterAmino registers all crypto related types in the given (amino) codec.
func RegisterAmino(cdc *amino.Codec) {
	// These are all written here instead of
	cdc.RegisterInterface((*crypto.PubKey)(nil), nil)
	cdc.RegisterConcrete(ed25519.PubKeyEd25519{},
		ed25519.PubKeyAminoName, nil)
	cdc.RegisterConcrete(secp256k1.PubKeySecp256k1{},
		secp256k1.PubKeyAminoName, nil)
	cdc.RegisterConcrete(multisig.PubKeyMultisigThreshold{},
		multisig.PubKeyMultisigThresholdAminoRoute, nil)
	cdc.RegisterConcrete(bls.PubKeyBls{},
		bls.PubKeyAminoName, nil)

	cdc.RegisterInterface((*crypto.PrivKey)(nil), nil)
	cdc.RegisterConcrete(ed25519.PrivKeyEd25519{},
		ed25519.PrivKeyAminoName, nil)
	cdc.RegisterConcrete(secp256k1.PrivKeySecp256k1{},
		secp256k1.PrivKeyAminoName, nil)
	cdc.RegisterConcrete(bls.PrivKeyBls{},
		bls.PrivKeyAminoName, nil)
}

func PrivKeyFromBytes(privKeyBytes []byte) (privKey crypto.PrivKey, err error) {
	err = cdc.UnmarshalBinaryBare(privKeyBytes, &privKey)
	return
}

func PubKeyFromBytes(pubKeyBytes []byte) (pubKey crypto.PubKey, err error) {
	err = cdc.UnmarshalBinaryBare(pubKeyBytes, &pubKey)
	return
}
