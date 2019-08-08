package bls

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"

	"github.com/hdac-io/tendermint/crypto"
	"github.com/hdac-io/tendermint/crypto/tmhash"
	herumi "github.com/hdac-io/bls-go-binary/bls"
	"github.com/hdac-io/tendermint/crypto/vrf"
)

var (
	errInvalidVRF = errors.New("invalid VRF proof")
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

func (privKey PrivKeyBls) Evaluate(m []byte) (index [32]byte, proof []byte) {
	//get the BLS signature of the message
	//pi = VRF_prove(SK, alpha)
	msgHash := sha256.Sum256(m)
	pi, err := privKey.Sign(msgHash[:])
	if err != nil {
		panic(err)
	}

	//hash the signature and output as VRF beta
	beta := sha256.Sum256(pi)
	return beta, pi
}

func (privKey PrivKeyBls) GetVrfSigner() vrf.PrivateKey {
	return privKey
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

func (pubKey PubKeyBls) ProofToHash(m, proof []byte) (index [32]byte, err error) {
	nilIndex := [32]byte{}

	if len(proof) == 0 {
		return nilIndex, errInvalidVRF
	}

	msgSig := herumi.Sign{}
	if err := msgSig.Deserialize(proof); err != nil {
		return nilIndex, err
	}

	msgHash := sha256.Sum256(m)
	if !msgSig.VerifyHash(&pubKey.PublicKey, msgHash[:]) {
		return nilIndex, errInvalidVRF
	}

	return sha256.Sum256(proof), nil
}

func (pubKey PubKeyBls) GetVrfVerifier() vrf.PublicKey {
	return &pubKey
}
