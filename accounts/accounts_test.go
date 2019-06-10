package accounts

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

var Account = make(AccountMap)

func registerAccount(stringName string) (*UnitAccount, error) {
	keyPair, err1 := GenKeyCandidateByObject()
	if err1 != nil {
		return nil, err1
	}
	accountObj, err2 := Account.NewAccount(stringName, keyPair.PrivKey)
	if err2 != nil {
		return nil, err2
	}
	return accountObj, nil
}

func TestExistingAccount(t *testing.T) {
	fmt.Println("Test1: Existing account filtering")
	_, err := registerAccount("bryanRhee")

	isExist := Account.CheckExistingAccount("bryanrhee")
	if err != nil {
		isExist = false
	}
	assert.EqualValues(t, isExist, true)
}

func TestTryCreatingDuplicatedAccount(t *testing.T) {
	fmt.Println("Test2: Duplicate account creation test")
	registerAccount("bryanRhee")

	// Try to create duplicate account
	isSuccess := false
	_, err := registerAccount("bryanrhee")

	if err != nil {
		isSuccess = true
	}
	assert.EqualValues(t, isSuccess, true)
}

func TestKeyChecking(t *testing.T) {
	fmt.Println("Test3: Key checking feature test")
	isSucceeded := false

	keyPair, err1 := GenKeyCandidateByObject()
	_, err2 := Account.NewAccount("psy2848048", keyPair.PrivKey)
	if err1 != nil || err2 != nil {
		fmt.Println(err1)
		fmt.Println(err2)
		assert.EqualValues(t, isSucceeded, true)
	}
	identified, err2 := Account.KeyCheck("psy2848048", keyPair.PrivKey)
	if identified == true {
		isSucceeded = true
	}
	assert.EqualValues(t, isSucceeded, true)

	// Wrong key pair test
	isSucceeded = false
	wrongKeyPair, _ := GenKeyCandidateByObject()
	cannotIdentified, err2 := Account.KeyCheck("psy2848048", wrongKeyPair.PrivKey)
	if cannotIdentified == false {
		isSucceeded = true
	}
	assert.EqualValues(t, isSucceeded, true)
}

func TestKeyChanging(t *testing.T) {
	fmt.Println("Test4: Key changing")
	isSucceeded := false

	firstKeyPair, _ := GenKeyCandidateByObject()
	Account.NewAccount("psy2848048.test", firstKeyPair.PrivKey)

	newKeyPair, _ := GenKeyCandidateByObject()
	isSucceeded, err := Account.KeyChange("psy2848048.test", firstKeyPair.PrivKey, newKeyPair.PrivKey)
	identified, err2 := Account.KeyCheck("psy2848048.test", newKeyPair.PrivKey)
	if err == nil && err2 == nil && identified == true {
		isSucceeded = true
	} else {
		fmt.Println(err)
	}

	assert.EqualValues(t, isSucceeded, true)
}

func TestGetAddressAndPublicKey(t *testing.T) {
	fmt.Println("Test5: Get address & public key")
	isSucceeded := false

	firstKeyPair, _ := GenKeyCandidateByObject()
	Account.NewAccount("leejjang.1440a", firstKeyPair.PrivKey)
	pubKey, err1 := Account.GetPublicKey("leejjang.1440a")
	address, err2 := Account.GetAddress("leejjang.1440a")
	if err1 == nil && err2 == nil && firstKeyPair.PubKey == pubKey {
		fmt.Println(pubKey)
		fmt.Println(address)
		isSucceeded = true
	} else {
		fmt.Println(err1)
		fmt.Println(err2)
	}
	assert.EqualValues(t, isSucceeded, true)
}
