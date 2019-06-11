package accounts

import (
	"errors"
	"fmt"

	"github.com/tendermint/tendermint/crypto"
)

/*
Requirements:
	- Provide account service with readable ID

Impl:
	- [Readable ID : Public key] matching logic
	- Key checking logic for account login
	- Key change logic
	- Duplicate readable ID check

*/

// UnitAccount struct for supporting readable ID
type UnitAccount struct {
	ID     Name
	PubKey crypto.PubKey
	// To be more appendded..
}

// AccountMap type
type AccountMap map[Name]*UnitAccount

// CheckExistingAccount checks the given string name already exists or not
func (ac *AccountMap) CheckExistingAccount(stringName string) bool {
	_, ok := (*ac)[NewName(stringName)]
	return ok
}

// NewAccount shows the step of ID registration
//    stringName string: Readable ID
//	  privKey crypto.PrivkKey: Matching private key
// Need to test: ac copied as a pointer or whole map object
func (ac *AccountMap) NewAccount(stringName string, privKey crypto.PrivKey) (*UnitAccount, error) {
	isDup := ac.CheckExistingAccount(stringName)
	if isDup == true {
		return nil, errors.New("Given ID already exists")
	}

	name := NewName(stringName)
	pubKey := privKey.PubKey()
	accountObj := &UnitAccount{
		PubKey: pubKey,
	}
	(*ac)[name] = accountObj
	fmt.Printf("Account '%s' has been created successfully with the following public key:\n", stringName)
	fmt.Println(accountObj.PubKey)

	return accountObj, nil
}

// NewAccountForBlockSync is needed for synching by block
func (ac *AccountMap) NewAccountForBlockSync(unitAccount UnitAccount) (*UnitAccount, error) {
	stringName, _ := unitAccount.ID.ToString()
	isDup := ac.CheckExistingAccount(stringName)
	if isDup == true {
		return nil, errors.New("Given ID already exists")
	}

	(*ac)[unitAccount.ID] = &unitAccount
	fmt.Printf("Account '%s' has been created successfully with the following public key:\n", stringName)
	fmt.Println(unitAccount.PubKey)

	return &unitAccount, nil
}

func (ac *AccountMap) keyCheck(stringName string, givenPrivKey crypto.PrivKey) (bool, error) {
	isExists := ac.CheckExistingAccount(stringName)
	if isExists == false {
		return false, errors.New("Account doesn't exist")
	}

	if currPubKey := (*ac)[NewName(stringName)]; currPubKey.PubKey == givenPrivKey.PubKey() {
		return true, nil
	}
	return false, errors.New("Key doesn't match")
}

// KeyCheck can be used as LogIn function
func (ac *AccountMap) KeyCheck(stringName string, givenPrivKey crypto.PrivKey) (bool, error) {
	return ac.keyCheck(stringName, givenPrivKey)
}

// KeyChange supports key change of account
func (ac *AccountMap) KeyChange(stringName string, oldPrivKey, newPrivKey crypto.PrivKey) (bool, error) {
	isMatched, err := ac.keyCheck(stringName, oldPrivKey)
	if isMatched == false {
		return false, err
	}

	newPubKey := newPrivKey.PubKey()
	(*ac)[NewName(stringName)].PubKey = newPubKey
	fmt.Printf("Key of account '%s' has been changed successfully with the following public key:\n", stringName)
	fmt.Println(newPubKey)

	return true, nil
}

// KeyChangeForBlockSync supports key change of account
func (ac *AccountMap) KeyChangeForBlockSync(oldAccout, newAccount UnitAccount) (bool, error) {
	if oldAccout.ID != newAccount.ID {
		return false, errors.New("Old account name and new account name should be same")
	}
	oldAccountInfo, isExist := (*ac)[oldAccout.ID]
	if isExist == false {
		return false, errors.New("No matching information with given old account")
	}
	oldPubKey := oldAccountInfo.PubKey
	if !oldPubKey.Equals(oldAccout.PubKey) {
		return false, errors.New("Wrong old public key")
	}

	(*ac)[newAccount.ID] = &newAccount
	stringName, _ := newAccount.ID.ToString()
	fmt.Printf("Key of account '%s' has been changed successfully with the following public key:\n", stringName)
	fmt.Println(newAccount.PubKey)

	return true, nil
}

func (ac *AccountMap) getPublicKey(stringName string) (crypto.PubKey, error) {
	isExists := ac.CheckExistingAccount(stringName)
	if isExists == false {
		return nil, errors.New("Account doesn't exist")
	}

	return (*ac)[NewName(stringName)].PubKey, nil
}

// GetPublicKey returns public key using string Name
func (ac *AccountMap) GetPublicKey(stringName string) (crypto.PubKey, error) {
	return ac.getPublicKey(stringName)
}

// GetAddress returns address using string Name
func (ac *AccountMap) GetAddress(stringName string) (crypto.Address, error) {
	pubKey, err := ac.getPublicKey(stringName)
	if err != nil {
		return nil, err
	}
	return pubKey.Address(), nil
}
