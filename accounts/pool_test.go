package accounts

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"

	dbm "github.com/tendermint/tendermint/libs/db"
	sm "github.com/tendermint/tendermint/state"
	"github.com/tendermint/tendermint/types"
	tmtime "github.com/tendermint/tendermint/types/time"
)

func TestMain(m *testing.M) {
	types.RegisterMockEvidences(cdc)

	code := m.Run()
	os.Exit(code)
}

func initializeValidatorState(valAddr []byte, height int64) dbm.DB {
	stateDB := dbm.NewMemDB()

	// create validator set and state
	valSet := &types.ValidatorSet{
		Validators: []*types.Validator{
			{Address: valAddr},
		},
	}
	state := sm.State{
		LastBlockHeight:             0,
		LastBlockTime:               tmtime.Now(),
		Validators:                  valSet,
		NextValidators:              valSet.CopyIncrementProposerPriority(1),
		LastHeightValidatorsChanged: 1,
		ConsensusParams: types.ConsensusParams{
			Evidence: types.EvidenceParams{
				MaxAge: 1000000,
			},
		},
	}

	// save all states up to height
	for i := int64(0); i < height; i++ {
		state.LastBlockHeight = i
		sm.SaveState(stateDB, state)
	}

	return stateDB
}

// Cannot test good or bad account yet.
// Block this test in now, and added it later
/*
func TestEvidencePool(t *testing.T) {

	valAddr := []byte("val1")
	height := int64(5)
	stateDB := initializeValidatorState(valAddr, height)
	evidenceDB := dbm.NewMemDB()
	pool := NewEvidencePool(stateDB, evidenceDB)

	goodEvidence := types.NewMockGoodEvidence(height, 0, valAddr)
	badEvidence := types.MockBadEvidence{goodEvidence}

	// bad evidence
	err := pool.AddEvidence(badEvidence)
	assert.NotNil(t, err)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		<-pool.EvidenceWaitChan()
		wg.Done()
	}()

	err = pool.AddEvidence(goodEvidence)
	assert.Nil(t, err)
	wg.Wait()

	assert.Equal(t, 1, pool.evidenceList.Len())

	// if we send it again, it shouldnt change the size
	err = pool.AddEvidence(goodEvidence)
	assert.Nil(t, err)
	assert.Equal(t, 1, pool.evidenceList.Len())
}
*/

func TestAccountPoolIsCommitted(t *testing.T) {
	// Initialization:
	valAddr := []byte("validator_address")
	height := int64(42)
	stateDB := initializeValidatorState(valAddr, height)
	accountDB := dbm.NewMemDB()
	pool := NewAccountPool(stateDB, accountDB)

	// Account which is not seen yet:
	keyPair, _ := GenKeyCandidateByObject()
	acc := UnitAccount{
		ID:     NewName("bryanrhee"),
		PubKey: keyPair.PubKey,
	}

	assert.False(t, pool.IsCommitted(acc))

	// evidence seen but not yet committed:
	assert.NoError(t, pool.AddAccount(acc))
	assert.False(t, pool.IsCommitted(acc))

	accArr := pool.PendingAccount(-1)
	assert.EqualValues(t, acc, accArr[0])

	// evidence seen and committed:
	pool.MarkAccountAsCommitted([]UnitAccount{acc})
	assert.True(t, pool.IsCommitted(acc))
	assert.True(t, pool.accountList.CheckExistingAccount("bryanrhee"))
}
func TestAccountPoolKeyChange(t *testing.T) {
	// Initialization:
	valAddr := []byte("validator_address")
	height := int64(42)
	stateDB := initializeValidatorState(valAddr, height)
	accountDB := dbm.NewMemDB()
	pool := NewAccountPool(stateDB, accountDB)

	// Account preparation
	keyPair, _ := GenKeyCandidateByObject()
	acc := UnitAccount{
		ID:     NewName("psy2848048"),
		PubKey: keyPair.PubKey,
	}
	pool.AddAccount(acc)
	pool.MarkAccountAsCommitted([]UnitAccount{acc})

	// Try to key change
	anotherKeyPair, _ := GenKeyCandidateByObject()
	newKeyAcc := UnitAccount{
		ID:     NewName("psy2848048"),
		PubKey: anotherKeyPair.PubKey,
	}
	pool.KeyChange(acc, newKeyAcc)
	pool.MarkAccountAsCommitted([]UnitAccount{newKeyAcc})

	pubkeyInAccPool, _ := pool.accountList.GetPublicKey("psy2848048")
	assert.EqualValues(t, newKeyAcc.PubKey, pubkeyInAccPool)
}
