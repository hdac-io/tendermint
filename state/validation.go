package state

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/hdac-io/tendermint/crypto"
	"github.com/hdac-io/tendermint/types"
	dbm "github.com/tendermint/tm-db"
)

//-----------------------------------------------------
// Validate block

func validateBlock(store BlockStore, evidencePool EvidencePool, stateDB dbm.DB, state State, block *types.Block) error {
	if state.Version.Consensus.Module == "friday" {
		return fridayValidateBlock(store, evidencePool, stateDB, state, block)
	} else {
		return tmValidateBlock(evidencePool, stateDB, state, block)
	}
}

func fridayValidateBlock(store BlockStore, evidencePool EvidencePool, stateDB dbm.DB, state State, block *types.Block) error {
	// Validate internal consistency.
	if err := block.ValidateFridayBasic(); err != nil {
		return err
	}

	lenULB := state.ConsensusParams.Block.LenULB

	// Validate basic info.
	if block.Version != state.Version.Consensus {
		return fmt.Errorf("Wrong Block.Header.Version. Expected %v, got %v",
			state.Version.Consensus,
			block.Version,
		)
	}
	if block.ChainID != state.ChainID {
		return fmt.Errorf("Wrong Block.Header.ChainID. Expected %v, got %v",
			state.ChainID,
			block.ChainID,
		)
	}

	if block.Height <= state.LastBlockHeight {
		return fmt.Errorf("Wrong Block.Header.Height. Expected over than %v, got %v",
			state.LastBlockHeight,
			block.Height,
		)
	}

	// Validate prev block id
	prevBlockHeight := block.Height - 1
	// just check commited case in state validation,
	// progressing prev block check is require to query the ConsensusState.
	if prevBlockHeight > 0 && prevBlockHeight <= state.LastBlockHeight {
		prevBlockMeta := store.LoadBlockMeta(prevBlockHeight)
		if !block.LastBlockID.Equals(prevBlockMeta.BlockID) {
			return &ErrLastBlockIDMismatch{block.LastBlockID, prevBlockMeta.BlockID}
		}
	}

	// Validate app info and last results
	if block.Height > lenULB {
		ulbHeight := block.Height - lenULB
		ulbAppHash, _ := LoadAppHash(stateDB, ulbHeight)
		if !bytes.Equal(block.AppHash, ulbAppHash) {
			return fmt.Errorf("Wrong Block.Header.AppHash.  Expected %X, got %v",
				ulbAppHash,
				block.AppHash,
			)
		}

		ulbABCIResponses, err := LoadABCIResponses(stateDB, ulbHeight)
		if err != nil {
			panic(fmt.Sprintf("Cannot load ulb ABCI responses. ulbHeight=%v, error=%v", ulbHeight, err.Error()))
		}
		ulbResultsHash := ulbABCIResponses.ResultsHash()
		if !bytes.Equal(block.LastResultsHash, ulbResultsHash) {
			return fmt.Errorf("Wrong Block.Header.LastResultsHash.  Expected %X, got %v",
				ulbResultsHash,
				block.LastResultsHash,
			)
		}
	}

	if !bytes.Equal(block.ConsensusHash, state.ConsensusParams.Hash()) {
		return fmt.Errorf("Wrong Block.Header.ConsensusHash.  Expected %X, got %v",
			state.ConsensusParams.Hash(),
			block.ConsensusHash,
		)
	}

	valHeight := block.Height
	if block.Height <= lenULB+1 {
		valHeight = 1
	}
	validators, err := LoadValidators(stateDB, valHeight)
	if err != nil {
		return fmt.Errorf("Cannot load validators. err=%v", err)
	}

	if !bytes.Equal(block.ValidatorsHash, validators.Hash()) {
		return fmt.Errorf("Wrong Block.Header.ValidatorsHash.  Expected %X, got %v",
			validators.Hash(),
			block.ValidatorsHash,
		)
	}

	if block.Height >= lenULB+1 {
		ulbNextValHeight := block.Height + 1
		ulbNextValidators, ulbNextValErr := LoadValidators(stateDB, ulbNextValHeight)
		if ulbNextValErr != nil {
			return fmt.Errorf("Cannot load validators. err=%v", ulbNextValErr)
		}

		if !bytes.Equal(block.NextValidatorsHash, ulbNextValidators.Hash()) {
			return fmt.Errorf("Wrong Block.Header.NextValidatorsHash.  Expected %X, got %v",
				ulbNextValidators.Hash(),
				block.NextValidatorsHash,
			)
		}
	}

	if block.Height <= lenULB {
		if len(block.LastCommit.Precommits) != 0 {
			return errors.New("Block at height less then LenULB can't have LastCommit precommits")
		}
	} else {
		ulbValidators, err := LoadValidators(stateDB, block.Height-lenULB)
		if err != nil {
			return fmt.Errorf("Cannot load ulb validators. err=%v", err)
		}

		if len(block.LastCommit.Precommits) != ulbValidators.Size() {
			return types.NewErrInvalidCommitPrecommits(ulbValidators.Size(), len(block.LastCommit.Precommits))
		}
		ulbHeight := block.Height - lenULB
		ulbBlockMeta := store.LoadBlockMeta(ulbHeight)
		err = ulbValidators.VerifyCommit(
			state.ChainID, ulbBlockMeta.BlockID, ulbHeight, block.LastCommit)
		if err != nil {
			return err
		}
	}

	// Validate block Time
	if block.Height > lenULB {
		ulbHeight := block.Height - lenULB
		ulbBlockMeta := store.LoadBlockMeta(ulbHeight)

		if !block.Time.After(ulbBlockMeta.Header.Time) {
			return fmt.Errorf("Block time %v not greater than ulb block time %v",
				block.Time,
				ulbBlockMeta.Header.Time,
			)
		}
		ulbValidators, err := LoadValidators(stateDB, ulbHeight)
		if err != nil {
			return fmt.Errorf("Cannot load ulb validators. err=%v", err)
		}
		medianTime := MedianTime(block.LastCommit, ulbValidators)
		if !block.Time.Equal(medianTime) {
			return fmt.Errorf("Invalid block time. Expected %v, got %v",
				medianTime,
				block.Time,
			)
		}
	} else if block.Height == 1 {
		genesisTime := state.LastBlockTime
		if !block.Time.Equal(genesisTime) {
			return fmt.Errorf("Block time %v is not equal to genesis time %v",
				block.Time,
				genesisTime,
			)
		}
	}

	// Limit the amount of evidence
	maxNumEvidence, _ := types.MaxEvidencePerBlock(state.ConsensusParams.Block.MaxBytes)
	numEvidence := int64(len(block.Evidence.Evidence))
	if numEvidence > maxNumEvidence {
		return types.NewErrEvidenceOverflow(maxNumEvidence, numEvidence)

	}

	// Validate all evidence.
	for _, ev := range block.Evidence.Evidence {
		if err := VerifyEvidence(stateDB, state, ev); err != nil {
			return types.NewErrEvidenceInvalid(ev, err)
		}
		if evidencePool != nil && evidencePool.IsCommitted(ev) {
			return types.NewErrEvidenceInvalid(ev, errors.New("evidence was already committed"))
		}
	}

	// NOTE: We can't actually verify it's the right proposer because we dont
	// know what round the block was first proposed. So just check that it's
	// a legit address and a known validator.
	if len(block.ProposerAddress) != crypto.AddressSize ||
		!state.Validators.HasAddress(block.ProposerAddress) {
		return fmt.Errorf("Block.Header.ProposerAddress, %X, is not a validator",
			block.ProposerAddress,
		)
	}

	return nil
}

func tmValidateBlock(evidencePool EvidencePool, stateDB dbm.DB, state State, block *types.Block) error {
	// Validate internal consistency.
	if err := block.ValidateBasic(); err != nil {
		return err
	}

	// Validate basic info.
	if block.Version != state.Version.Consensus {
		return fmt.Errorf("Wrong Block.Header.Version. Expected %v, got %v",
			state.Version.Consensus,
			block.Version,
		)
	}
	if block.ChainID != state.ChainID {
		return fmt.Errorf("Wrong Block.Header.ChainID. Expected %v, got %v",
			state.ChainID,
			block.ChainID,
		)
	}

	if block.Height != state.LastBlockHeight+1 {
		return fmt.Errorf("Wrong Block.Header.Height. Expected %v, got %v",
			state.LastBlockHeight+1,
			block.Height,
		)
	}

	// Validate prev block info.
	if !block.LastBlockID.Equals(state.LastBlockID) {
		return fmt.Errorf("Wrong Block.Header.LastBlockID.  Expected %v, got %v",
			state.LastBlockID,
			block.LastBlockID,
		)
	}

	newTxs := int64(len(block.Data.Txs))
	if block.TotalTxs != state.LastBlockTotalTx+newTxs {
		return fmt.Errorf("Wrong Block.Header.TotalTxs. Expected %v, got %v",
			state.LastBlockTotalTx+newTxs,
			block.TotalTxs,
		)
	}

	// Validate app info
	if !bytes.Equal(block.AppHash, state.AppHash) {
		return fmt.Errorf("Wrong Block.Header.AppHash.  Expected %X, got %v",
			state.AppHash,
			block.AppHash,
		)
	}
	if !bytes.Equal(block.ConsensusHash, state.ConsensusParams.Hash()) {
		return fmt.Errorf("Wrong Block.Header.ConsensusHash.  Expected %X, got %v",
			state.ConsensusParams.Hash(),
			block.ConsensusHash,
		)
	}
	if !bytes.Equal(block.LastResultsHash, state.LastResultsHash) {
		return fmt.Errorf("Wrong Block.Header.LastResultsHash.  Expected %X, got %v",
			state.LastResultsHash,
			block.LastResultsHash,
		)
	}
	if !bytes.Equal(block.ValidatorsHash, state.Validators.Hash()) {
		return fmt.Errorf("Wrong Block.Header.ValidatorsHash.  Expected %X, got %v",
			state.Validators.Hash(),
			block.ValidatorsHash,
		)
	}
	if !bytes.Equal(block.NextValidatorsHash, state.NextValidators.Hash()) {
		return fmt.Errorf("Wrong Block.Header.NextValidatorsHash.  Expected %X, got %v",
			state.NextValidators.Hash(),
			block.NextValidatorsHash,
		)
	}

	// Validate block LastCommit.
	if block.Height == 1 {
		if len(block.LastCommit.Precommits) != 0 {
			return errors.New("Block at height 1 can't have LastCommit precommits")
		}
	} else {
		if len(block.LastCommit.Precommits) != state.LastValidators.Size() {
			return types.NewErrInvalidCommitPrecommits(state.LastValidators.Size(), len(block.LastCommit.Precommits))
		}
		err := state.LastValidators.VerifyCommit(
			state.ChainID, state.LastBlockID, block.Height-1, block.LastCommit)
		if err != nil {
			return err
		}
	}

	// Validate block Time
	if block.Height > 1 {
		if !block.Time.After(state.LastBlockTime) {
			return fmt.Errorf("Block time %v not greater than last block time %v",
				block.Time,
				state.LastBlockTime,
			)
		}

		medianTime := MedianTime(block.LastCommit, state.LastValidators)
		if !block.Time.Equal(medianTime) {
			return fmt.Errorf("Invalid block time. Expected %v, got %v",
				medianTime,
				block.Time,
			)
		}
	} else if block.Height == 1 {
		genesisTime := state.LastBlockTime
		if !block.Time.Equal(genesisTime) {
			return fmt.Errorf("Block time %v is not equal to genesis time %v",
				block.Time,
				genesisTime,
			)
		}
	}

	// Limit the amount of evidence
	maxNumEvidence, _ := types.MaxEvidencePerBlock(state.ConsensusParams.Block.MaxBytes)
	numEvidence := int64(len(block.Evidence.Evidence))
	if numEvidence > maxNumEvidence {
		return types.NewErrEvidenceOverflow(maxNumEvidence, numEvidence)

	}

	// Validate all evidence.
	for _, ev := range block.Evidence.Evidence {
		if err := VerifyEvidence(stateDB, state, ev); err != nil {
			return types.NewErrEvidenceInvalid(ev, err)
		}
		if evidencePool != nil && evidencePool.IsCommitted(ev) {
			return types.NewErrEvidenceInvalid(ev, errors.New("evidence was already committed"))
		}
	}

	// NOTE: We can't actually verify it's the right proposer because we dont
	// know what round the block was first proposed. So just check that it's
	// a legit address and a known validator.
	if len(block.ProposerAddress) != crypto.AddressSize ||
		!state.Validators.HasAddress(block.ProposerAddress) {
		return fmt.Errorf("Block.Header.ProposerAddress, %X, is not a validator",
			block.ProposerAddress,
		)
	}

	return nil
}

// VerifyEvidence verifies the evidence fully by checking:
// - it is sufficiently recent (MaxAge)
// - it is from a key who was a validator at the given height
// - it is internally consistent
// - it was properly signed by the alleged equivocator
func VerifyEvidence(stateDB dbm.DB, state State, evidence types.Evidence) error {
	height := state.LastBlockHeight

	evidenceAge := height - evidence.Height()
	maxAge := state.ConsensusParams.Evidence.MaxAge
	if evidenceAge > maxAge {
		return fmt.Errorf("Evidence from height %d is too old. Min height is %d",
			evidence.Height(), height-maxAge)
	}

	valset, err := LoadValidators(stateDB, evidence.Height())
	if err != nil {
		// TODO: if err is just that we cant find it cuz we pruned, ignore.
		// TODO: if its actually bad evidence, punish peer
		return err
	}

	// The address must have been an active validator at the height.
	// NOTE: we will ignore evidence from H if the key was not a validator
	// at H, even if it is a validator at some nearby H'
	// XXX: this makes lite-client bisection as is unsafe
	// See https://github.com/tendermint/tendermint/issues/3244
	ev := evidence
	height, addr := ev.Height(), ev.Address()
	_, val := valset.GetByAddress(addr)
	if val == nil {
		return fmt.Errorf("Address %X was not a validator at height %d", addr, height)
	}

	if err := evidence.Verify(state.ChainID, val.PubKey); err != nil {
		return err
	}

	return nil
}
