package accounts

import (
	"fmt"
	"sync"

	clist "github.com/tendermint/tendermint/libs/clist"
	dbm "github.com/tendermint/tendermint/libs/db"
	"github.com/tendermint/tendermint/libs/log"

	sm "github.com/tendermint/tendermint/state"
	"github.com/tendermint/tendermint/types"
)

// AccountPool maintains a pool of valid Readable account
//   in an AccountStore.
// Remaining evidence dummies are legacy of evidence package,
//   and will be replaced in approproate form.
type AccountPool struct {
	logger log.Logger

	accountStore *AccountStore
	accountList  *clist.CList // concurrent linked-list of evidence

	// needed to load validators to verify evidence
	stateDB dbm.DB

	// latest state
	mtx   sync.Mutex
	state sm.State
}

func NewAccountPool(stateDB, evidenceDB dbm.DB) *AccountPool {
	// Implement here
	return evpool
}

func (acpool *AccountPool) AccountFront() *clist.CElement {
	return acpool.accountList.Front()
}

func (acpool *AccountPool) AccountWaitChan() <-chan struct{} {
	return acpool.accountList.WaitChan()
}

// SetLogger sets the Logger.
func (acpool *AccountPool) SetLogger(l log.Logger) {
	evpool.logger = l
}

// PriorityEvidence returns the priority evidence.
func (acpool *AccountPool) PriorityAccount() []AccountInfo {
	return acpool.accountStore.PriorityAccount()
}

// PendingEvidence returns up to maxNum uncommitted evidence.
// If maxNum is -1, all evidence is returned.
func (acpool *AccountPool) PendingAccount(maxNum int64) []AccountInfo {
	return evpool.accountStore.PendingAccount(maxNum)
}

// State returns the current state of the evpool.
func (acpool *AccountPool) State() sm.State {
	acpool.mtx.Lock()
	defer acpool.mtx.Unlock()
	return acpool.state
}

// Update loads the latest
func (acpool *AccountPool) Update(block *types.Block, state sm.State) {

	// sanity check
	if state.LastBlockHeight != block.Height {
		panic(fmt.Sprintf("Failed EvidencePool.Update sanity check: got state.Height=%d with block.Height=%d", state.LastBlockHeight, block.Height))
	}

	// update the state
	acpool.mtx.Lock()
	acpool.state = state
	acpool.mtx.Unlock()

	// remove evidence from pending and mark committed
	// If block data has account section, apply it
	//acpool.MarkEvidenceAsCommitted(block.Height, block.Evidence.Evidence)
}

// AddEvidence checks the evidence is valid and adds it to the pool.
func (acpool *AccountPool) AddAccount(unitAccount AccountInfo) (err error) {

	// TODO: check if we already have evidence for this
	// validator at this height so we dont get spammed

	if err := sm.VerifyAccount(acpool.stateDB, acpool.State(), unitAccount); err != nil {
		return err
	}

	// fetch the validator and return its voting power as its priority
	// TODO: something better ?
	valset, _ := sm.LoadValidators(acpool.stateDB, evidence.Height())
	_, val := valset.GetByAddress(evidence.Address())
	priority := val.VotingPower

	added := acpool.accountStore.AddNewAccount(unitAccount, priority)
	if !added {
		// evidence already known, just ignore
		return
	}

	acpool.logger.Info("Verified new account of byzantine behaviour", "account", unitAccount)

	// add evidence to clist
	acpool.accountList.PushBack(unitAccount)

	return nil
}

// MarkEvidenceAsCommitted marks all the evidence as committed and removes it from the queue.
func (acpool *AccountPool) MarkAccountAsCommitted(height int64, accountList []AccountInfo) {
	// make a map of committed evidence to remove from the clist
	blockEvidenceMap := make(map[string]struct{})
	for _, ev := range accountList {
		acpool.evidenceStore.MarkEvidenceAsCommitted(ev)
		blockEvidenceMap[evMapKey(ev)] = struct{}{}
	}

	// remove committed evidence from the clist
	maxAge := evpool.State().ConsensusParams.Evidence.MaxAge
	evpool.removeEvidence(height, maxAge, blockEvidenceMap)

}

// IsCommitted returns true if we have already seen this exact evidence and it is already marked as committed.
func (evpool *EvidencePool) IsCommitted(evidence types.Evidence) bool {
	ei := evpool.evidenceStore.getEvidenceInfo(evidence)
	return ei.Evidence != nil && ei.Committed
}

func (evpool *EvidencePool) removeEvidence(height, maxAge int64, blockEvidenceMap map[string]struct{}) {
	for e := evpool.evidenceList.Front(); e != nil; e = e.Next() {
		ev := e.Value.(types.Evidence)

		// Remove the evidence if it's already in a block
		// or if it's now too old.
		if _, ok := blockEvidenceMap[evMapKey(ev)]; ok ||
			ev.Height() < height-maxAge {

			// remove from clist
			evpool.evidenceList.Remove(e)
			e.DetachPrev()
		}
	}
}

func evMapKey(ev types.Evidence) string {
	return string(ev.Hash())
}
