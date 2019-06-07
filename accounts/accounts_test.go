package accounts

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestKeyGeneration(t *testing.T) {
	fmt.Println("Test1: Private key - Public key generation")
	isError := true
	testKeyPair, err := GenKeyCandidateByObject()
	if err == nil {
		isError = false
	}

	fmt.Printf("Private key: %s\n", testKeyPair.PrivKey.Bytes())
	fmt.Printf("Public key: %s\n", testKeyPair.PubKey)
	fmt.Printf("Address: %s\n", testKeyPair.PubKey.Address())

	assert.EqualValues(t, isError, false)
}
