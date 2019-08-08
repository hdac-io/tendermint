package p256

import (
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"

	amino "github.com/tendermint/go-amino"
)

const (
	PrivKeyAminoName   = "tendermint/libs/vrf/p256/PrivateKey"
	PubKeyAminoName    = "tendermint/libs/vrf/p256/PublicKey"
	P256CurveAminoName = "tendermint/libs/vrf/elliptic/p256"
)

// MarshalAmino Support armino codec deep copy interface
func (k PrivateKey) MarshalAmino() (string, error) {
	pubKey := PublicKey{PublicKey: &k.PublicKey}
	marshaledPubKey, pubKeyErr := pubKey.MarshalAmino()
	if pubKeyErr != nil {
		return "", pubKeyErr
	}

	mappedPriv := map[string]string{"D": hex.EncodeToString(k.D.Bytes()), "PublicKey": marshaledPubKey}
	marsharedJSON, jsonErr := json.Marshal(mappedPriv)
	if jsonErr != nil {
		return "", jsonErr
	}

	return hex.EncodeToString(marsharedJSON), nil
}

// UnmarshalAmino Support armino codec deep copy interface
func (k *PrivateKey) UnmarshalAmino(s string) error {
	var mappedPriv map[string]string

	marshaledJSON, jsonDecodeErr := hex.DecodeString(s)
	if jsonDecodeErr != nil {
		return jsonDecodeErr
	}

	jsonErr := json.Unmarshal(marshaledJSON, &mappedPriv)
	if jsonErr != nil {
		return jsonErr
	}

	decoded, dErr := hex.DecodeString(mappedPriv["D"])
	if dErr != nil {
		return dErr
	}
	privateKey, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		return err
	}
	k.PrivateKey = privateKey
	k.D.SetBytes(decoded)

	var pubKey PublicKey
	pubKeyErr := pubKey.UnmarshalAmino(mappedPriv["PublicKey"])
	if pubKeyErr != nil {
		return pubKeyErr
	}
	k.PublicKey = *pubKey.PublicKey

	return nil
}

// MarshalAmino Support armino codec deep copy interface
func (pk PublicKey) MarshalAmino() (string, error) {
	return hex.EncodeToString(append(pk.X.Bytes(), pk.Y.Bytes()...)), nil
}

// UnmarshalAmino Support armino codec deep copy interface
func (pk *PublicKey) UnmarshalAmino(s string) error {
	decoded, err := hex.DecodeString(s)
	if err == nil {
		_, pkey := GenerateKey()
		pk.PublicKey = pkey.(*PublicKey).PublicKey
		pk.X.SetBytes(decoded[:len(decoded)/2])
		pk.Y.SetBytes(decoded[len(decoded)/2:])
	}
	return err
}

// RegisterAmino regist concrete struct to marshaling library amino
func RegisterAmino(cdc *amino.Codec) {
	cdc.RegisterConcrete(&PublicKey{}, PubKeyAminoName, nil)
	cdc.RegisterConcrete(&PrivateKey{}, PrivKeyAminoName, nil)
	cdc.RegisterConcrete(curve, P256CurveAminoName, nil)
}
