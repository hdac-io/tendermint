package accounts

import (
	"fmt"

	"github.com/tendermint/go-amino"
	dbm "github.com/tendermint/tendermint/libs/db"
)

/*
Requirements:
	- Valid new account must be persisted immediately and never forgotten
	- Uncommitted account must be continuously broadcast

Impl:
	- First commit atomically in outqueue, pending, lookup.
	- Once broadcast, remove from outqueue. No need to sync
	- Once committed, atomically remove from pending and update lookup.

Schema for indexing account (note you need both height and hash to find a piece of account):

"account-lookup"/<string account> -> AccountInfo
"account-outqueue"/<string account> -> AccountInfo
"account-pending"/<string account> -> AccountInfo
*/

// AccountInfo contains commit & broadcast priority
type AccountInfo struct {
	Committed bool
	// More to be added if needed
	UnitAccount UnitAccount
}

// RegisterAccountInfo registers to amino codec to byte digest
func RegisterAccountInfo(cd *amino.Codec) {
	cd.RegisterConcrete(&AccountInfo{}, "tendermint/accounts/AccountInfo", nil)
}

// Account is a GLOBAL VARIABLE for handling readabale account service
//var Account = make(AccountMap)

const (
	baseKeyLookup   = "account-lookup"   // all account
	baseKeyOutqueue = "account-outqueue" // not-yet broadcasted account
	baseKeyPending  = "account-pending"  // broadcasted but not committed
)

func keyLookup(stringName string) []byte {
	return _key("%s/%s", baseKeyLookup, stringName)
}

func keyOutqueue(stringName string) []byte {
	return _key("%s/%s", baseKeyOutqueue, stringName)
}

func keyPending(stringName string) []byte {
	return _key("%s/%s", baseKeyPending, stringName)
}

func _key(fmtString string, o ...interface{}) []byte {
	return []byte(fmt.Sprintf(fmtString, o...))
}

// AccountStore is a store of all the account we've seen, including
// accounts that has been committed, evidence that has been verified but not broadcast,
// and accounts that has been broadcast but not yet committed.
type AccountStore struct {
	db dbm.DB
}

// NewAccountStore returns AccountStore DB object
func NewAccountStore(db dbm.DB) *AccountStore {
	return &AccountStore{
		db: db,
	}
}

// PendingAccount returns up to maxNum known, uncommitted accounts.
// If maxNum is -1, all accounts are returned.
func (store *AccountStore) PendingAccount(maxNum int64) (accounts []UnitAccount) {
	return store.listAccounts(baseKeyPending, maxNum)
}

// listAccounts lists up to maxNum pieces of account for the given prefix key.
// It is wrapped by PendingAccount for convenience.
// If maxNum is -1, there's no cap on the size of returned accounts.
func (store *AccountStore) listAccounts(prefixKey string, maxNum int64) (accounts []UnitAccount) {
	var count int64
	iter := dbm.IteratePrefix(store.db, []byte(prefixKey))
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		val := iter.Value()

		if count == maxNum {
			return accounts
		}
		count++

		var acc AccountInfo
		err := cdc.UnmarshalBinaryBare(val, &acc)
		if err != nil {
			panic(err)
		}
		accounts = append(accounts, acc.UnitAccount)
	}
	return accounts
}

// GetAccountInfo fetches the AccountInfo with the given unit account data
// If not found, acc.UnitAccount is nil.
func (store *AccountStore) GetAccountInfo(unitAccount UnitAccount) AccountInfo {
	stringName, _ := unitAccount.ID.ToString()
	key := keyLookup(stringName)
	val := store.db.Get(key)

	if len(val) == 0 {
		return AccountInfo{}
	}
	var acc AccountInfo
	err := cdc.UnmarshalBinaryBare(val, &acc)
	if err != nil {
		panic(err)
	}
	return acc
}

// AddNewAccount adds the given unit account to the database.
// It returns false if the account is already stored.
func (store *AccountStore) AddNewAccount(unitAccount UnitAccount) bool {
	// check if we already have seen it
	acc := store.getAccountInfo(unitAccount)
	if acc.UnitAccount != (UnitAccount{}) {
		return false
	}

	acc = AccountInfo{
		Committed:   false,
		UnitAccount: unitAccount,
	}
	accBytes := cdc.MustMarshalBinaryBare(acc)

	// add it to the store
	strName, _ := unitAccount.ID.ToString()
	key := keyOutqueue(strName)
	store.db.Set(key, accBytes)

	key = keyPending(strName)
	store.db.Set(key, accBytes)

	key = keyLookup(strName)
	store.db.SetSync(key, accBytes)

	return true
}

// MarkAccountAsBroadcasted removes account from Outqueue.
func (store *AccountStore) MarkAccountAsBroadcasted(unitAccount UnitAccount) {
	acc := store.getAccountInfo(unitAccount)
	if acc.UnitAccount == (UnitAccount{}) {
		// nothing to do; we did not store the account yet (AddNewAccount):
		return
	}
	// remove from the outqueue
	strName, _ := unitAccount.ID.ToString()
	key := keyOutqueue(strName)
	store.db.Delete(key)
}

// MarkAccountAsCommitted removes account from pending and outqueue and sets the state to committed.
func (store *AccountStore) MarkAccountAsCommitted(unitAccount UnitAccount) {
	// if its committed, its been broadcast
	store.MarkAccountAsBroadcasted(unitAccount)

	strName, _ := unitAccount.ID.ToString()
	pendingKey := keyPending(strName)
	store.db.Delete(pendingKey)

	acc := AccountInfo{
		Committed:   true,
		UnitAccount: unitAccount,
	}

	lookupKey := keyLookup(strName)
	store.db.SetSync(lookupKey, cdc.MustMarshalBinaryBare(acc))
}

//---------------------------------------------------
// utils

// getAccountInfo is convenience for calling GetAccountInfo if we have the full unit account data.
func (store *AccountStore) getAccountInfo(unitAccount UnitAccount) AccountInfo {
	return store.GetAccountInfo(unitAccount)
}
