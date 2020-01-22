package privval

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"sync"

	"github.com/hdac-io/tendermint/crypto"
	"github.com/hdac-io/tendermint/crypto/bls"
	cmn "github.com/hdac-io/tendermint/libs/common"
	"github.com/hdac-io/tendermint/types"
	amino "github.com/tendermint/go-amino"
)

func RegisterFridaySignState(cdc *amino.Codec) {
	cdc.RegisterConcrete(&FridayFilePVSignState{}, "tendermint/fridayFilePVState", nil)
}

//-------------------------------------------------------------------------------
// FridayFilePVSignState stores the mutable part of PrivValidator.
type FridayFilePVSignState struct {
	HeightSignStateMap sync.Map `json:"height_sign_states"`
	ImmutableHeight    int64    `json:"immutable_height"`

	filePath string
}

// SignState stores sign info state per height
type SignState struct {
	Round     int          `json:"round"`
	Step      int8         `json:"step"`
	Signature []byte       `json:"signature,omitempty"`
	SignBytes cmn.HexBytes `json:"signbytes,omitempty"`
}

// CheckHRS checks the given height, round, step (HRS) against that of the
// FilePVLastSignState. It returns an error if the arguments constitute a regression,
// or if they match but the SignBytes are empty.
// The returned boolean indicates whether the last Signature should be reused -
// it returns true if the HRS matches the arguments and the SignBytes are not empty (indicating
// we have already signed for this HRS, and can reuse the existing signature).
// It panics if the HRS matches the arguments, there's a SignBytes, but no Signature.
func (ss *FridayFilePVSignState) CheckHRS(height int64, round int, step int8) (bool, *SignState, error) {
	if ss.ImmutableHeight >= height {
		return false, nil, fmt.Errorf("height regression. Got %v, immutable height %v", height, ss.ImmutableHeight)
	}

	if signStateInterface, exist := ss.HeightSignStateMap.Load(height); exist {
		signState := signStateInterface.(SignState)
		if signState.Round > round {
			return false, nil, fmt.Errorf("round regression at height %v. Got %v, last round %v", height, round, signState.Round)
		}

		if signState.Round == round {
			if signState.Step > step {
				return false, nil, fmt.Errorf("step regression at height %v round %v. Got %v, last step %v", height, round, step, signState.Step)
			} else if signState.Step == step {
				if signState.SignBytes != nil {
					if signState.Signature == nil {
						panic("pv: Signature is nil but SignBytes is not!")
					}
					return true, &signState, nil
				}
				return false, nil, errors.New("no SignBytes found")
			}
		}
	}

	return false, nil, nil
}

// StoreSignState save singnature information to map per height
func (ss *FridayFilePVSignState) storeSignState(height int64, round int, step int8, signBytes cmn.HexBytes, signature []byte) {
	ss.HeightSignStateMap.Store(height,
		SignState{
			Round:     round,
			Step:      step,
			Signature: signature,
			SignBytes: signBytes,
		})
}

//marshalSpecializedState is for marshaling the sync.Map field.
type marshalSpecializedState struct {
	HeightSignStateMap map[int64]SignState `json:"height_sign_states"`
	ImmutableHeight    int64               `json:"immutable_height"`
}

//MarshalJSON override purpose is for using builtin json marshaler
func (ss *FridayFilePVSignState) MarshalJSON() ([]byte, error) {
	tmpMap := make(map[int64]SignState)
	ss.HeightSignStateMap.Range(func(k, v interface{}) bool {
		tmpMap[k.(int64)] = v.(SignState)
		return true
	})

	//using builtin json marshaler, because Amino does not support the map type.
	encoded, err := json.Marshal(marshalSpecializedState{
		HeightSignStateMap: tmpMap,
		ImmutableHeight:    ss.ImmutableHeight,
	})
	return encoded, err
}

//UnmarshalJSON override purpose is for using builtin json marshaler
func (ss *FridayFilePVSignState) UnmarshalJSON(marshaled []byte) error {
	var tmpState marshalSpecializedState
	if err := json.Unmarshal(marshaled, &tmpState); err != nil {
		return err
	}

	for height, state := range tmpState.HeightSignStateMap {
		ss.HeightSignStateMap.Store(height, state)
	}
	ss.ImmutableHeight = tmpState.ImmutableHeight
	return nil
}

// Save persists the FridayFilePVLastSignState to its filePath.
// NOTE: change amino to builtin json marshaler, amino cannot support to map struct
func (ss *FridayFilePVSignState) Save() {
	outFile := ss.filePath
	if outFile == "" {
		panic("cannot save FridayFilePVLastSignState: filePath not set")
	}
	jsonBytes, err := cdc.MarshalJSONIndent(ss, "", "  ")
	if err != nil {
		panic(err)
	}
	err = cmn.WriteFileAtomic(outFile, jsonBytes, 0600)
	if err != nil {
		panic(err)
	}
}

// Reset resets all Sign State
// NOTE: Unsafe!
func (ss *FridayFilePVSignState) reset() {
	ss.HeightSignStateMap.Range(func(key interface{}, value interface{}) bool {
		ss.HeightSignStateMap.Delete(key)
		return true
	})
}

// SetImmutableHeight remove signature lower than target height(usage: last commited height)
func (ss *FridayFilePVSignState) setImmutableHeight(height int64) error {
	if ss.ImmutableHeight > height {
		return fmt.Errorf("immutable height regression. Got %v, current immutable height %v", height, ss.ImmutableHeight)
	}

	ss.ImmutableHeight = height
	ss.HeightSignStateMap.Range(func(key interface{}, value interface{}) bool {
		if signedHeight := key.(int64); ss.ImmutableHeight > signedHeight {
			ss.HeightSignStateMap.Delete(signedHeight)
			return true
		}
		return false
	})

	return nil
}

// String returns a string representation of the FridayFilePVLastSignState.
func (ss *FridayFilePVSignState) String() string {
	var result string
	ss.HeightSignStateMap.Range(func(key interface{}, value interface{}) bool {
		SignState := value.(SignState)
		if len(result) != 0 {
			result += ", "
		}

		result += fmt.Sprintf("{LH:%v, LR:%v, LS:%v}", key.(int64), SignState.Round, SignState.Step)
		return true
	})

	return fmt.Sprintf("ImmutableHeight:%v, HeightSignStateMap:%v", ss.ImmutableHeight, result)
}

//-------------------------------------------------------------------------------

// FridayFilePV implements PrivValidator using data persisted to disk
// to prevent double signing.
// NOTE: the directories containing pv.Key.filePath and pv.SignState.filePath must already exist.
// It includes the LastSignature and LastSignBytes so we don't lose the signature
// if the process crashes after signing but before the resulting consensus message is processed.\
// NOTE: change to specialized SignState, because friday's require to check progressing multiple heights
type FridayFilePV struct {
	Key       FilePVKey
	SignState FridayFilePVSignState
}

// GenFilePV generates a new validator with randomly generated private key
// and sets the filePaths, but does not call Save().
func GenFridayFilePV(keyFilePath, stateFilePath string) *FridayFilePV {
	privKey := bls.GenPrivKey()

	return &FridayFilePV{
		Key: FilePVKey{
			Address:  privKey.PubKey().Address(),
			PubKey:   privKey.PubKey(),
			PrivKey:  privKey,
			filePath: keyFilePath,
		},
		SignState: FridayFilePVSignState{
			filePath: stateFilePath,
		},
	}
}

// LoadFilePV loads a FilePV from the filePaths.  The FilePV handles double
// signing prevention by persisting data to the stateFilePath.  If either file path
// does not exist, the program will exit.
func LoadFridayFilePV(keyFilePath, stateFilePath string) *FridayFilePV {
	return loadFridayFilePV(keyFilePath, stateFilePath, true)
}

// LoadFilePVEmptyState loads a FilePV from the given keyFilePath, with an empty SignState.
// If the keyFilePath does not exist, the program will exit.
func LoadFridayFilePVEmptyState(keyFilePath, stateFilePath string) *FridayFilePV {
	return loadFridayFilePV(keyFilePath, stateFilePath, false)
}

// If loadState is true, we load from the stateFilePath. Otherwise, we use an empty SignState.
func loadFridayFilePV(keyFilePath, stateFilePath string, loadState bool) *FridayFilePV {
	keyJSONBytes, err := ioutil.ReadFile(keyFilePath)
	if err != nil {
		cmn.Exit(err.Error())
	}
	pvKey := FilePVKey{}
	err = cdc.UnmarshalJSON(keyJSONBytes, &pvKey)
	if err != nil {
		cmn.Exit(fmt.Sprintf("Error reading PrivValidator key from %v: %v\n", keyFilePath, err))
	}

	// overwrite pubkey and address for convenience
	pvKey.PubKey = pvKey.PrivKey.PubKey()
	pvKey.Address = pvKey.PubKey.Address()
	pvKey.filePath = keyFilePath

	pvState := FridayFilePVSignState{}
	if loadState {
		stateJSONBytes, err := ioutil.ReadFile(stateFilePath)
		if err != nil {
			cmn.Exit(err.Error())
		}
		err = cdc.UnmarshalJSON(stateJSONBytes, &pvState)
		if err != nil {
			cmn.Exit(fmt.Sprintf("Error reading PrivValidator state from %v: %v\n", stateFilePath, err))
		}
	}

	pvState.filePath = stateFilePath

	return &FridayFilePV{
		Key:       pvKey,
		SignState: pvState,
	}
}

// LoadOrGenFridayFilePV loads a FilePV from the given filePaths
// or else generates a new one and saves it to the filePaths.
func LoadOrGenFridayFilePV(keyFilePath, stateFilePath string) *FridayFilePV {
	var pv *FridayFilePV
	if cmn.FileExists(keyFilePath) {
		pv = LoadFridayFilePV(keyFilePath, stateFilePath)
	} else {
		pv = GenFridayFilePV(keyFilePath, stateFilePath)
		pv.Save()
	}
	return pv
}

// GetAddress returns the address of the validator.
// Implements PrivValidator.
func (pv *FridayFilePV) GetAddress() types.Address {
	return pv.Key.Address
}

// GetPubKey returns the public key of the validator.
// Implements PrivValidator.
func (pv *FridayFilePV) GetPubKey() crypto.PubKey {
	return pv.Key.PubKey
}

// SignVote signs a canonical representation of the vote, along with the
// chainID. Implements PrivValidator.
func (pv *FridayFilePV) SignVote(chainID string, vote *types.Vote) error {
	if err := pv.signVote(chainID, vote); err != nil {
		return fmt.Errorf("error signing vote: %v", err)
	}
	return nil
}

// SignProposal signs a canonical representation of the proposal, along with
// the chainID. Implements PrivValidator.
func (pv *FridayFilePV) SignProposal(chainID string, proposal *types.Proposal) error {
	if err := pv.signProposal(chainID, proposal); err != nil {
		return fmt.Errorf("error signing proposal: %v", err)
	}
	return nil
}

// GetParallelProgressablePV implements PrivValidator.
func (pv *FridayFilePV) GetParallelProgressablePV() types.ParallelProgressablePV {
	return pv
}

// Save persists the FridayFilePV to disk.
func (pv *FridayFilePV) Save() {
	pv.Key.Save()
	pv.SignState.Save()
}

// Reset resets all fields in the FridayFilePV.
// NOTE: Unsafe!
func (pv *FridayFilePV) Reset() {
	pv.SignState.reset()
	pv.Save()
}

// SetImmutableHeight remove signature lower than target height(usage: last commited height)
// Implements ParallelProgressablePV
func (pv *FridayFilePV) SetImmutableHeight(height int64) error {
	return pv.SignState.setImmutableHeight(height)
}

// String returns a string representation of the FridayFilePV.
func (pv *FridayFilePV) String() string {
	return fmt.Sprintf("PrivValidator{%v SignState:%s}", pv.GetAddress(), pv.SignState.String())
}

// signVote checks if the vote is good to sign and sets the vote signature.
// It may need to set the timestamp as well if the vote is otherwise the same as
// a previously signed vote (ie. we crashed after signing but before the vote hit the WAL).
func (pv *FridayFilePV) signVote(chainID string, vote *types.Vote) error {
	height, round, step := vote.Height, vote.Round, voteToStep(vote)

	sameHRS, existSignState, err := pv.SignState.CheckHRS(height, round, step)
	if err != nil {
		return err
	}

	signBytes := vote.SignBytes(chainID)

	// We might crash before writing to the wal,
	// causing us to try to re-sign for the same HRS.
	// If signbytes are the same, use the last signature.
	// If they only differ by timestamp, use last timestamp and signature
	// Otherwise, return error
	if sameHRS {
		if bytes.Equal(signBytes, existSignState.SignBytes) {
			vote.Signature = existSignState.Signature
		} else if timestamp, ok := checkVotesOnlyDifferByTimestamp(existSignState.SignBytes, signBytes); ok {
			vote.Timestamp = timestamp
			vote.Signature = existSignState.Signature
		} else {
			err = fmt.Errorf("conflicting data")
		}
		return err
	}

	// It passed the checks. Sign the vote
	sig, err := pv.Key.PrivKey.Sign(signBytes)
	if err != nil {
		return err
	}
	pv.saveSigned(height, round, step, signBytes, sig)
	vote.Signature = sig
	return nil
}

// signProposal checks if the proposal is good to sign and sets the proposal signature.
// It may need to set the timestamp as well if the proposal is otherwise the same as
// a previously signed proposal ie. we crashed after signing but before the proposal hit the WAL).
func (pv *FridayFilePV) signProposal(chainID string, proposal *types.Proposal) error {
	height, round, step := proposal.Height, proposal.Round, stepPropose

	sameHRS, existSignState, err := pv.SignState.CheckHRS(height, round, step)
	if err != nil {
		return err
	}

	signBytes := proposal.SignBytes(chainID)

	// We might crash before writing to the wal,
	// causing us to try to re-sign for the same HRS.
	// If signbytes are the same, use the last signature.
	// If they only differ by timestamp, use last timestamp and signature
	// Otherwise, return error
	if sameHRS {
		if bytes.Equal(signBytes, existSignState.SignBytes) {
			proposal.Signature = existSignState.Signature
		} else if timestamp, ok := checkProposalsOnlyDifferByTimestamp(existSignState.SignBytes, signBytes); ok {
			proposal.Timestamp = timestamp
			proposal.Signature = existSignState.Signature
		} else {
			err = fmt.Errorf("conflicting data")
		}
		return err
	}

	// It passed the checks. Sign the proposal
	sig, err := pv.Key.PrivKey.Sign(signBytes)
	if err != nil {
		return err
	}
	pv.saveSigned(height, round, step, signBytes, sig)
	proposal.Signature = sig
	return nil
}

// Persist height/round/step and signature
func (pv *FridayFilePV) saveSigned(height int64, round int, step int8,
	signBytes []byte, sig []byte) {

	pv.SignState.storeSignState(height, round, step, signBytes, sig)
	pv.SignState.Save()
}
