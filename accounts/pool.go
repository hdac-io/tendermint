package accounts

import (
	"sync"

	dbm "github.com/tendermint/tendermint/libs/db"
	"github.com/tendermint/tendermint/libs/log"

	sm "github.com/tendermint/tendermint/state"
)

// AccountPool maintains a pool of valid readable account
// in an AccountStore.
type AccountPool struct {
	logger log.Logger

	accountStore *AccountStore
	accountList  *AccountMap

	// needed to load validators to verify evidence
	stateDB dbm.DB

	// latest state
	mtx   sync.Mutex
	state sm.State
}

// NewAccountPool acts as an AccountPool constructor
func NewAccountPool(stateDB, accDB dbm.DB) *AccountPool {
	accStore := NewAccountStore(accDB)
	accMap := make(AccountMap)
	accpool := &AccountPool{
		stateDB:      stateDB,
		state:        sm.LoadState(stateDB),
		logger:       log.NewNopLogger(),
		accountStore: accStore,
		accountList:  &accMap,
	}
	return accpool
}

// SetLogger sets the Logger.
func (accpool *AccountPool) SetLogger(l log.Logger) {
	accpool.logger = l
}

// PendingAccount returns up to maxNum uncommitted accounts.
// If maxNum is -1, all evidence is returned.
func (accpool *AccountPool) PendingAccount(maxNum int64) []UnitAccount {
	return accpool.accountStore.PendingAccount(maxNum)
}

// State returns the current state of the evpool.
func (accpool *AccountPool) State() sm.State {
	accpool.mtx.Lock()
	defer accpool.mtx.Unlock()
	return accpool.state
}

// Update loads the latest
// Currently blocked because block data style and the data representation of account manipulation are not set yet.

/*
func (accpool *AccountPool) Update(block *types.Block, state sm.State) {

	// sanity check
	if state.LastBlockHeight != block.Height {
		panic(fmt.Sprintf("Failed EvidencePool.Update sanity check: got state.Height=%d with block.Height=%d", state.LastBlockHeight, block.Height))
	}

	// update the state
	evpool.mtx.Lock()
	evpool.state = state
	evpool.mtx.Unlock()

	// remove evidence from pending and mark committed
	evpool.MarkEvidenceAsCommitted(block.Height, block.Evidence.Evidence)
}
*/

// VerifyAccount verifies the account fully by checking:
// - it is sufficiently recent (MaxAge)
// - it is from a key who was a validator at the given height
// - it is internally consistent
// - it was properly signed by the alleged equivocator
// TODO: This method should be relocated into state/validation.go

// VerifyAccount(stateDB dbm.DB, state State, unitAccount UnitAccount) error
//    should be implemented

// AddAccount checks the account is valid and adds it to the pool.
func (accpool *AccountPool) AddAccount(unitAccount UnitAccount) (err error) {

	// Not yet verifiable
	// Once block data structure is fixed and then can develop it.
	/*
		if err := sm.VerifyEvidence(evpool.stateDB, evpool.State(), evidence); err != nil {
			return err
		}

		valset, _ := sm.LoadValidators(accpool.stateDB, accpool.Height())
		_, val := valset.GetByAddress(evidence.Address())
		priority := val.VotingPower
	*/

	added := accpool.accountStore.AddNewAccount(unitAccount)
	if !added {
		// evidence already known, just ignore
		return
	}

	accpool.logger.Info("Verified new account of byzantine behaviour", "account", unitAccount)

	// Add account to account list
	accpool.accountList.NewAccountForBlockSync(unitAccount)

	return nil
}

func (accpool *AccountPool) KeyChange(oldAccount, newAccount UnitAccount) (err error) {
	// Not yet verifiable
	// Once block data structure is fixed and then can develop it.
	/*
		if err := sm.VerifyEvidence(evpool.stateDB, evpool.State(), evidence); err != nil {
			return err
		}

		valset, _ := sm.LoadValidators(accpool.stateDB, accpool.Height())
		_, val := valset.GetByAddress(evidence.Address())
		priority := val.VotingPower
	*/

	changed := accpool.accountStore.ChangeKey(oldAccount, newAccount)
	if !changed {
		return
	}

	accpool.logger.Info("Verified account key change of byzantine behaviour", "account", newAccount)

	// Add account to account list
	accpool.accountList.KeyChangeForBlockSync(oldAccount, newAccount)

	return nil

}

// MarkAccountAsCommitted marks all the evidence as committed and removes it from the queue.
func (accpool *AccountPool) MarkAccountAsCommitted(accounts []UnitAccount) {
	for _, acc := range accounts {
		accpool.accountStore.MarkAccountAsCommitted(acc)
	}
}

// IsCommitted returns true if we have already seen this exact account and it is already marked as committed.
func (accpool *AccountPool) IsCommitted(unitAccount UnitAccount) bool {
	accInfo := accpool.accountStore.getAccountInfo(unitAccount)
	return accInfo.UnitAccount != UnitAccount{} && accInfo.Committed
}
