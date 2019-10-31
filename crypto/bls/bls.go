package bls

import (
	"bytes"
	"errors"

	"github.com/tendermint/tendermint/crypto"
	herumi "github.com/tendermint/tendermint/crypto/bls/bls"
	"github.com/tendermint/tendermint/crypto/tmhash"
)

// PrivKeyBls Wrap to herumi bls for tendermint crypto.PrivKey
type PrivKeyBls struct {
	SerializedPrivKey []byte            `json:"raw_priv_key"`
	herumiPriv        *herumi.SecretKey //cache
}

// PubKeyBls Wrap to herumi bls for tendermint crypto.PubKey
type PubKeyBls struct {
	SerializedPubKey []byte            `json:"raw_pub_key"`
	herumiPub        *herumi.PublicKey //cache
}

func init() {
	herumi.Init(herumi.BLS12_381)
}

func GenerateKey() (PrivKeyBls, PubKeyBls) {
	var rawPriv herumi.SecretKey
	rawPriv.SetByCSPRNG()
	rawPub := rawPriv.GetPublicKey()

	serializedPriv := rawPriv.Serialize()
	serializedPub := rawPub.Serialize()

	return PrivKeyBls{SerializedPrivKey: serializedPriv}, PubKeyBls{SerializedPubKey: serializedPub}
}

func (privKey *PrivKeyBls) loadCache() error {
	if len(privKey.SerializedPrivKey) == 0 {
		return errors.New("serialized length is zero")
	}

	if privKey.herumiPriv == nil {
		var herumiPriv herumi.SecretKey
		if err := herumiPriv.Deserialize(privKey.SerializedPrivKey); err != nil {
			return err
		}
		privKey.herumiPriv = &herumiPriv
	}

	return nil
}

func (privKey PrivKeyBls) Bytes() []byte {
	data, _ := cdc.MarshalBinaryBare(privKey)
	return data
}

func (privKey PrivKeyBls) Sign(msg []byte) ([]byte, error) {
	if loadErr := privKey.loadCache(); loadErr != nil {
		return nil, loadErr
	}

	herumiSign := privKey.herumiPriv.SignHash(msg)
	return herumiSign.Serialize(), nil
}

func (privKey PrivKeyBls) PubKey() crypto.PubKey {
	if loadErr := privKey.loadCache(); loadErr != nil {
		panic("Cannot loaded herumi pubkey instance")
	}
	rawPub := privKey.herumiPriv.GetPublicKey()

	return PubKeyBls{SerializedPubKey: rawPub.Serialize()}
}

func (privKey PrivKeyBls) Equals(rhs crypto.PrivKey) bool {
	return bytes.Equal(privKey.SerializedPrivKey, rhs.(*PrivKeyBls).SerializedPrivKey)
}

func (pubKey PubKeyBls) Address() crypto.Address {
	return crypto.Address(tmhash.SumTruncated(pubKey.SerializedPubKey))
}
func (pubKey PubKeyBls) Bytes() []byte {
	data, _ := cdc.MarshalBinaryBare(pubKey)
	return data
}
func (pubKey PubKeyBls) VerifyBytes(msg []byte, sig []byte) bool {
	if pubKey.herumiPub == nil {
		var rawPub herumi.PublicKey
		if err := rawPub.Deserialize(pubKey.SerializedPubKey); err != nil {
			return false
		}

		pubKey.herumiPub = &rawPub
	}

	var herumiSign herumi.Sign
	if err := herumiSign.Deserialize(sig); err != nil {
		return false
	}
	return herumiSign.VerifyHash(pubKey.herumiPub, msg)
}

func (pubKey PubKeyBls) Equals(rhs crypto.PubKey) bool {
	return bytes.Equal(pubKey.SerializedPubKey, rhs.(*PubKeyBls).SerializedPubKey)
}
