package bls

import (
	"encoding/base64"

	"github.com/hdac-io/tendermint/crypto"
	"github.com/hdac-io/tendermint/crypto/tmhash"
	herumi "github.com/herumi/bls-go-binary/bls"
)

// PrivKeyBls Wrap to herumi bls for tendermint crypto.PrivKey
type PrivKeyBls struct {
	herumi.SecretKey
}

// PubKeyBls Wrap to herumi bls for tendermint crypto.PubKey
type PubKeyBls struct {
	herumi.PublicKey
}

func init() {
	herumi.Init(herumi.BLS12_381)
}

func GenPrivKey() PrivKeyBls {
	var priv PrivKeyBls
	priv.SetByCSPRNG()
	return priv
}

// MarshalAmino implement raw deep copy without json tag based default encode
// it's useful shorter length more than default encode
func (privKey PrivKeyBls) MarshalAmino() (string, error) {
	return base64.StdEncoding.EncodeToString(privKey.Serialize()), nil
}

func (privKey *PrivKeyBls) UnmarshalAmino(text string) error {
	if serializedPrivKey, err := base64.StdEncoding.DecodeString(text); err == nil {
		return privKey.Deserialize(serializedPrivKey)
	} else {
		return err
	}
}

func (privKey PrivKeyBls) Bytes() []byte {
	data, _ := cdc.MarshalBinaryBare(privKey)
	return data
}

func (privKey PrivKeyBls) Sign(msg []byte) ([]byte, error) {
	herumiSign := privKey.SignHash(msg)
	return herumiSign.Serialize(), nil
}

func (privKey PrivKeyBls) PubKey() crypto.PubKey {
	return PubKeyBls{*privKey.GetPublicKey()}
}

func (privKey PrivKeyBls) Equals(rhs crypto.PrivKey) bool {
	return privKey.IsEqual(&(rhs.(*PrivKeyBls).SecretKey))
}

// MarshalAmino implement raw deep copy without json tag based default encode
// it's useful shorter length more than default encode
func (pubKey PubKeyBls) MarshalAmino() (string, error) {
	return base64.StdEncoding.EncodeToString(pubKey.Serialize()), nil
}

func (pubKey *PubKeyBls) UnmarshalAmino(text string) error {
	if serializedPubKey, err := base64.StdEncoding.DecodeString(text); err == nil {
		return pubKey.Deserialize(serializedPubKey)
	} else {
		return err
	}
}

func (pubKey PubKeyBls) Address() crypto.Address {
	return crypto.Address(tmhash.SumTruncated(pubKey.Serialize()))
}
func (pubKey PubKeyBls) Bytes() []byte {
	data, _ := cdc.MarshalBinaryBare(pubKey)
	return data
}
func (pubKey PubKeyBls) VerifyBytes(msg []byte, sig []byte) bool {
	var herumiSign herumi.Sign
	if err := herumiSign.Deserialize(sig); err != nil {
		return false
	}
	return herumiSign.VerifyHash(&pubKey.PublicKey, msg)
}

func (pubKey PubKeyBls) Equals(rhs crypto.PubKey) bool {
	return pubKey.IsEqual(&(rhs.(*PubKeyBls).PublicKey))
}
