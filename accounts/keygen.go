package accounts

import (
	"io/ioutil"

	amino "github.com/tendermint/go-amino"
	"github.com/tendermint/tendermint/crypto"
	"github.com/tendermint/tendermint/crypto/ed25519"
)

var cdc = amino.NewCodec()

// KeyPair struct works for generating [Public key : Private key] pair for account
// It, especially private key only works for JSON marshalling on file flushing.
// Private key does not use in DB store
type KeyPair struct {
	PrivKey crypto.PrivKey
	PubKey  crypto.PubKey
}

func genKeyCandidate(isOnFile bool, filePath string) (*KeyPair, error) {
	privKey := ed25519.GenPrivKey()
	pubKey := privKey.PubKey()

	keyPair := &KeyPair{
		PrivKey: privKey,
		PubKey:  pubKey,
	}

	if isOnFile == true {
		jsonBytes, err := cdc.MarshalJSON(keyPair)
		if err != nil {
			return nil, err
		}
		err = ioutil.WriteFile(filePath, jsonBytes, 0600)
		if err != nil {
			return nil, err
		}
	}
	return keyPair, nil
}

// GenKeyCandidateByObject returns randomly-generated KeyPair object
func GenKeyCandidateByObject() (*KeyPair, error) {
	keyPair, err := genKeyCandidate(false, "")
	return keyPair, err
}

// GenKeyCandidateByFile flushes as a file
func GenKeyCandidateByFile(filePath string) error {
	_, err := genKeyCandidate(true, filePath)
	return err
}
