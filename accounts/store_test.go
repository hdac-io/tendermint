package accounts

import (
	"testing"

	"github.com/stretchr/testify/assert"
	dbm "github.com/tendermint/tendermint/libs/db"
)

//-------------------------------------------

func newMockAccount(stringName string) UnitAccount {
	keyPair, _ := GenKeyCandidateByObject()
	return UnitAccount{
		ID:     NewName(stringName),
		PubKey: keyPair.PubKey,
	}
}

func TestStoreAddDuplicate(t *testing.T) {
	assert := assert.New(t)

	db := dbm.NewMemDB()
	store := NewAccountStore(db)

	// registerAccount from accounts test module
	acc := newMockAccount("bryanrhee")

	added := store.AddNewAccount(acc)
	assert.True(added)

	// cant add twice
	added = store.AddNewAccount(acc)
	assert.False(added)
}

func TestStoreCommitDuplicate(t *testing.T) {
	assert := assert.New(t)

	db := dbm.NewMemDB()
	store := NewAccountStore(db)

	acc := newMockAccount("bryanrhee")

	store.MarkAccountAsCommitted(acc)

	added := store.AddNewAccount(acc)
	assert.False(added)
}

func TestStoreMark(t *testing.T) {
	assert := assert.New(t)

	db := dbm.NewMemDB()
	store := NewAccountStore(db)

	// before we do anything, pending are empty
	pendingAcc := store.PendingAccount(-1)
	assert.Equal(0, len(pendingAcc))

	acc := newMockAccount("bryanrhee")

	added := store.AddNewAccount(acc)
	assert.True(added)

	// get the evidence. verify. should be uncommitted
	accInfo := store.GetAccountInfo(acc)
	assert.Equal(acc, accInfo.UnitAccount)
	assert.False(accInfo.Committed)

	// new evidence should be returns in priority/pending
	pendingAcc = store.PendingAccount(-1)
	assert.Equal(1, len(pendingAcc))

	// priority is now empty
	store.MarkAccountAsBroadcasted(acc)
	pendingAcc = store.PendingAccount(-1)
	assert.Equal(1, len(pendingAcc))

	// priority and pending are now empty
	store.MarkAccountAsCommitted(acc)
	pendingAcc = store.PendingAccount(-1)
	assert.Equal(0, len(pendingAcc))

	// account object should show committed
	accObj := store.GetAccountInfo(accInfo.UnitAccount)
	assert.Equal(acc, accObj.UnitAccount)
	assert.True(accObj.Committed)
}
