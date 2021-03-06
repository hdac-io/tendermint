package friday

import (
	"bytes"
	"fmt"
	"reflect"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"

	cmn "github.com/hdac-io/tendermint/libs/common"
	"github.com/hdac-io/tendermint/libs/fail"
	"github.com/hdac-io/tendermint/libs/log"
	tmtime "github.com/hdac-io/tendermint/types/time"

	cfg "github.com/hdac-io/tendermint/config"
	tmcs "github.com/hdac-io/tendermint/consensus"
	cstypes "github.com/hdac-io/tendermint/consensus/types"
	tmevents "github.com/hdac-io/tendermint/libs/events"
	"github.com/hdac-io/tendermint/p2p"
	sm "github.com/hdac-io/tendermint/state"
	"github.com/hdac-io/tendermint/types"
)

//-----------------------------------------------------------------------------
// Errors

var (
	ErrInvalidProposalSignature = errors.New("Error invalid proposal signature")
	ErrInvalidProposalPOLRound  = errors.New("Error invalid proposal POL round")
	ErrAddingVote               = errors.New("Error adding vote")
	ErrVoteHeightMismatch       = errors.New("Error vote height mismatch")
)

//-----------------------------------------------------------------------------

var (
	msgQueueSize = 1000
)

// msgs from the reactor which may update the state
type msgInfo struct {
	Msg    ConsensusMessage `json:"msg"`
	PeerID p2p.ID           `json:"peer_key"`
}

// internally generated messages which may update the state
type timeoutInfo struct {
	Duration time.Duration         `json:"duration"`
	Height   int64                 `json:"height"`
	Round    int                   `json:"round"`
	Step     cstypes.RoundStepType `json:"step"`
}

func (ti *timeoutInfo) String() string {
	return fmt.Sprintf("%v ; %d/%d %v", ti.Duration, ti.Height, ti.Round, ti.Step)
}

// interface to the mempool
type txNotifier interface {
	TxsAvailable() <-chan struct{}
}

// interface to the evidence pool
type evidencePool interface {
	AddEvidence(types.Evidence) error
}

// ConsensusState handles execution of the consensus algorithm.
// It processes votes and proposals, and upon reaching agreement,
// commits blocks to the chain and executes them against the application.
// The internal state machine receives input from peers, the internal validator, and from a timer.
type ConsensusState struct {
	cmn.BaseService

	// config details
	config        *cfg.ConsensusConfig
	privValidator types.PrivValidator // for signing votes

	// store blocks and commits
	blockStore sm.BlockStore

	// create and execute blocks
	blockExec *sm.BlockExecutor

	// notify us if txs are available
	txNotifier txNotifier

	// add evidence to the pool
	// when it's detected
	evpool evidencePool

	// internal state
	mtx         sync.RWMutex
	roundStates sync.Map
	state       sm.State // State until height-1.

	finalizeMtx      sync.RWMutex
	waitFinalizeCond *sync.Cond
	waitFinalize     int32

	// state changes may be triggered by: msgs from peers,
	// msgs from ourself, or by timeouts
	peerMsgQueue       chan msgInfo
	internalMsgQueue   chan msgInfo
	timeoutTickers     sync.Map
	aggregatedTockChan chan timeoutInfo

	// information about about added votes and block parts are written on this channel
	// so statistics can be computed by reactor
	statsMsgQueue chan msgInfo

	// signal for scheduling new height, triggered by: scheduleNewHeightRound0
	newHeightQueue chan int64

	// we use eventBus to trigger msg broadcasts in the reactor,
	// and to notify external subscribers, eg. through a websocket
	eventBus *types.EventBus

	// a Write-Ahead Log ensures we can recover from any kind of crash
	// and helps us avoid signing conflicting votes
	wal          WAL
	replayMode   bool // so we don't log signing errors during replay
	doWALCatchup bool // determines if we even try to do the catchup

	// for tests where we want to limit the number of transitions the state makes
	nSteps int

	// some functions can be overwritten for testing
	decideProposal func(height int64, round int)
	doPrevote      func(height int64, round int)
	setProposal    func(proposal *types.Proposal) error

	// closed when we finish shutting down
	done chan struct{}

	// synchronous pubsub between consensus state and reactor.
	// state only emits EventNewRoundStep and EventVote
	evsw tmevents.EventSwitch

	// for reporting metrics
	metrics *tmcs.Metrics
}

// StateOption sets an optional parameter on the ConsensusState.
type StateOption func(*ConsensusState)

// NewConsensusState returns a new ConsensusState.
func NewConsensusState(
	config *cfg.ConsensusConfig,
	state sm.State,
	blockExec *sm.BlockExecutor,
	blockStore sm.BlockStore,
	txNotifier txNotifier,
	evpool evidencePool,
	options ...StateOption,
) *ConsensusState {
	cs := &ConsensusState{
		config:             config,
		blockExec:          blockExec,
		blockStore:         blockStore,
		txNotifier:         txNotifier,
		peerMsgQueue:       make(chan msgInfo, msgQueueSize),
		internalMsgQueue:   make(chan msgInfo, msgQueueSize),
		timeoutTickers:     sync.Map{},
		aggregatedTockChan: make(chan timeoutInfo, tickTockBufferSize),
		statsMsgQueue:      make(chan msgInfo, msgQueueSize),
		newHeightQueue:     make(chan int64),
		done:               make(chan struct{}),
		doWALCatchup:       true,
		wal:                nilWAL{},
		evpool:             evpool,
		evsw:               tmevents.NewEventSwitch(),
		metrics:            tmcs.NopMetrics(),
		roundStates:        sync.Map{},
	}
	// set function defaults (may be overwritten before calling Start)
	cs.decideProposal = cs.defaultDecideProposal
	cs.doPrevote = cs.defaultDoPrevote
	cs.setProposal = cs.defaultSetProposal

	cs.waitFinalizeCond = sync.NewCond(&cs.finalizeMtx)

	cs.updateToState(state)

	// Don't call scheduleRound0 yet.
	// We do that upon Start().
	cs.reconstructLastCommit(state)
	cs.BaseService = *cmn.NewBaseService(nil, "ConsensusState", cs)
	for _, option := range options {
		option(cs)
	}
	return cs
}

//----------------------------------------
// Public interface

// SetLogger implements Service.
func (cs *ConsensusState) SetLogger(l log.Logger) {
	cs.BaseService.Logger = l
}

// SetEventBus sets event bus.
func (cs *ConsensusState) SetEventBus(b *types.EventBus) {
	cs.eventBus = b
	cs.blockExec.SetEventBus(b)
}

// StateMetrics sets the metrics.
func StateMetrics(metrics *tmcs.Metrics) StateOption {
	return func(cs *ConsensusState) { cs.metrics = metrics }
}

// String returns a string.
func (cs *ConsensusState) String() string {
	// better not to access shared variables
	return fmt.Sprintf("ConsensusState") //(H:%v R:%v S:%v", cs.Height, cs.Round, cs.Step)
}

// GetState returns a copy of the chain state.
func (cs *ConsensusState) GetState() sm.State {
	cs.mtx.RLock()
	defer cs.mtx.RUnlock()
	return cs.state.Copy()
}

// GetLastHeight returns the last height committed.
// If there were no blocks, returns 0.
func (cs *ConsensusState) GetLastHeight() int64 {
	cs.mtx.RLock()
	defer cs.mtx.RUnlock()
	return cs.state.LastBlockHeight
}

// GetRoundState returns a shallow copy of the internal consensus state.
func (cs *ConsensusState) GetRoundState(height int64) *cstypes.RoundState {
	if rs := cs.getRoundState(height); rs != nil {
		copiedRs := rs.Copy()
		return &copiedRs
	} else {
		return nil
	}
}

func (cs *ConsensusState) getRoundState(height int64) *cstypes.RoundState {
	rs, exist := cs.roundStates.Load(height)
	if !exist {
		return nil
	}

	return rs.(*cstypes.RoundState)
}

// GetRoundStateJSON returns a json of multiple RoundState, marshalled using go-amino.
func (cs *ConsensusState) GetRoundStateJSON() ([]byte, error) {

	// transrate to slice, because of amino cannot support sync.Map
	var roundStates []cstypes.RoundState

	cs.roundStates.Range(func(key, value interface{}) bool {
		rs := value.(*cstypes.RoundState)
		roundStates = append(roundStates, rs.Copy())

		return true
	})

	return cdc.MarshalJSON(roundStates)
}

// GetRoundStateSimpleJSON returns a json of multiple RoundStateSimple, marshalled using go-amino.
func (cs *ConsensusState) GetRoundStateSimpleJSON() ([]byte, error) {

	var simples []cstypes.RoundStateSimple

	cs.roundStates.Range(func(key, value interface{}) bool {
		rs := value.(*cstypes.RoundState)
		simples = append(simples, rs.RoundStateSimple())

		return true
	})

	return cdc.MarshalJSON(simples)
}

// GetRoundStatesMap returns internal progressing multiple round states
func (cs *ConsensusState) GetRoundStatesMap() *sync.Map {
	return &cs.roundStates
}

// GetValidators returns a copy of the current validators.
func (cs *ConsensusState) GetValidators() (int64, []*types.Validator) {
	cs.mtx.RLock()
	defer cs.mtx.RUnlock()
	return cs.state.LastBlockHeight, cs.state.Validators.Copy().Validators
}

// SetPrivValidator sets the private validator account for signing votes.
func (cs *ConsensusState) SetPrivValidator(priv types.PrivValidator) {
	cs.mtx.Lock()
	if pppv := priv.GetParallelProgressablePV(); pppv == nil {
		panic(fmt.Sprintf("Unexpected PrivValidator type. cannot suppported parallel progressable. got %v", reflect.TypeOf(priv)))
	}

	cs.privValidator = priv
	cs.mtx.Unlock()
}

// LoadCommit loads the commit for a given height.
func (cs *ConsensusState) LoadCommit(height int64) *types.Commit {
	cs.mtx.RLock()
	defer cs.mtx.RUnlock()
	bsHeight := cs.blockStore.Height()
	distance := bsHeight - height
	if distance <= cs.state.ConsensusParams.Block.LenULB {
		return cs.blockStore.LoadSeenCommit(height)
	}
	return cs.blockStore.LoadBlockCommit(height)
}

// OnStart implements cmn.Service.
// It loads the latest state via the WAL, and starts the timeout and receive routines.
func (cs *ConsensusState) OnStart() error {

	if err := cs.evsw.Start(); err != nil {
		return err
	}

	// we may set the WAL in testing before calling Start,
	// so only OpenWAL if its still the nilWAL
	if _, ok := cs.wal.(nilWAL); ok {
		walFile := cs.config.WalFile()
		wal, err := cs.OpenWAL(walFile)
		if err != nil {
			cs.Logger.Error("Error loading ConsensusState wal", "err", err.Error())
			return err
		}
		cs.wal = wal
	}

	// we need the timeoutRoutine for replay so
	// we don't block on the tick chan.
	// NOTE: we will get a build up of garbage go routines
	// firing on the tockChan until the receiveRoutine is started
	// to deal with them (by that point, at most one will be valid)
	height := cs.state.LastBlockHeight + 1
	if _, hasTicker := cs.timeoutTickers.Load(height); !hasTicker {
		cs.timeoutTickers.Store(height, NewTimeoutTicker(cs.aggregatedTockChan))
		ticker, _ := cs.timeoutTickers.Load(height)
		ticker.(TimeoutTicker).Start()
	}

	// we may have lost some votes if the process crashed
	// reload from consensus log to catchup
	if cs.doWALCatchup {
		if err := cs.catchupReplay(height); err != nil {
			// don't try to recover from data corruption error
			if IsDataCorruptionError(err) {
				cs.Logger.Error("Encountered corrupt WAL file", "err", err.Error())
				cs.Logger.Error("Please repair the WAL file before restarting")
				fmt.Println(`You can attempt to repair the WAL as follows:

----
WALFILE=~/.tendermint/data/cs.wal/wal
cp $WALFILE ${WALFILE}.bak # backup the file
go run scripts/wal2json/main.go $WALFILE > wal.json # this will panic, but can be ignored
rm $WALFILE # remove the corrupt file
go run scripts/json2wal/main.go wal.json $WALFILE # rebuild the file without corruption
----`)

				return err
			}

			cs.Logger.Error("Error on catchup replay. Proceeding to start ConsensusState anyway", "err", err.Error())
			// NOTE: if we ever do return an error here,
			// make sure to stop the timeoutTicker
		}
	}

	// now start the receiveRoutine
	go cs.receiveRoutine(0)

	// schedule the first round!
	// use GetRoundState so we don't race the receiveRoutine for access
	cs.scheduleNewHeightRound0(height)
	return nil
}

// OnStop implements cmn.Service.
func (cs *ConsensusState) OnStop() {
	cs.evsw.Stop()
	cs.timeoutTickers.Range(func(key, value interface{}) bool {
		ticker := value.(TimeoutTicker)
		ticker.Stop()
		return true
	})
	// WAL is stopped in receiveRoutine.
}

// Wait waits for the the main routine to return.
// NOTE: be sure to Stop() the event switch and drain
// any event channels or this may deadlock
func (cs *ConsensusState) Wait() {
	<-cs.done
}

// OpenWAL opens a file to log all consensus messages and timeouts for deterministic accountability
func (cs *ConsensusState) OpenWAL(walFile string) (WAL, error) {
	wal, err := NewWAL(walFile)
	if err != nil {
		cs.Logger.Error("Failed to open WAL for consensus state", "wal", walFile, "err", err)
		return nil, err
	}
	wal.SetLogger(cs.Logger.With("wal", walFile))
	if err := wal.Start(); err != nil {
		return nil, err
	}
	return wal, nil
}

//------------------------------------------------------------
// Public interface for passing messages into the consensus state, possibly causing a state transition.
// If peerID == "", the msg is considered internal.
// Messages are added to the appropriate queue (peer or internal).
// If the queue is full, the function may block.
// TODO: should these return anything or let callers just use events?

// AddVote inputs a vote.
func (cs *ConsensusState) AddVote(vote *types.Vote, peerID p2p.ID) (added bool, err error) {
	if peerID == "" {
		cs.internalMsgQueue <- msgInfo{&VoteMessage{vote}, ""}
	} else {
		cs.peerMsgQueue <- msgInfo{&VoteMessage{vote}, peerID}
	}

	// TODO: wait for event?!
	return false, nil
}

// SetProposal inputs a proposal.
func (cs *ConsensusState) SetProposal(proposal *types.Proposal, peerID p2p.ID) error {

	if peerID == "" {
		cs.internalMsgQueue <- msgInfo{&ProposalMessage{proposal}, ""}
	} else {
		cs.peerMsgQueue <- msgInfo{&ProposalMessage{proposal}, peerID}
	}

	// TODO: wait for event?!
	return nil
}

// AddProposalBlockPart inputs a part of the proposal block.
func (cs *ConsensusState) AddProposalBlockPart(height int64, round int, part *types.Part, peerID p2p.ID) error {

	if peerID == "" {
		cs.internalMsgQueue <- msgInfo{&BlockPartMessage{height, round, part}, ""}
	} else {
		cs.peerMsgQueue <- msgInfo{&BlockPartMessage{height, round, part}, peerID}
	}

	// TODO: wait for event?!
	return nil
}

// SetProposalAndBlock inputs the proposal and all block parts.
func (cs *ConsensusState) SetProposalAndBlock(proposal *types.Proposal, block *types.Block, parts *types.PartSet, peerID p2p.ID) error {
	if err := cs.SetProposal(proposal, peerID); err != nil {
		return err
	}
	for i := 0; i < parts.Total(); i++ {
		part := parts.GetPart(i)
		if err := cs.AddProposalBlockPart(proposal.Height, proposal.Round, part, peerID); err != nil {
			return err
		}
	}
	return nil
}

//------------------------------------------------------------
// internal functions for managing the state

func (cs *ConsensusState) updateHeight(height int64) {
	cs.metrics.Height.Set(float64(height))
	cs.updateNewHeight(height)
}

func (cs *ConsensusState) updateNewHeight(height int64) bool {

	validators := (*types.ValidatorSet)(nil)
	ulbPrecommits := (*types.VoteSet)(nil)
	ulbValidators := (*types.ValidatorSet)(nil)

	if height > cs.state.ConsensusParams.Block.LenULB {
		ulbHeight := height - cs.state.ConsensusParams.Block.LenULB

		// cannot getting ulb commits and validators when restart shutdowned node
		// it's come recovery to reconstructLastCommit
		if interfaceULB, hasULBRound := cs.roundStates.Load(ulbHeight); hasULBRound {
			ulbRound := interfaceULB.(*cstypes.RoundState)
			if ulbRound.CommitRound > -1 && ulbRound.Votes != nil {
				if !ulbRound.Votes.Precommits(ulbRound.CommitRound).HasTwoThirdsMajority() {
					panic("updateToState(state) called but last Precommit round didn't have +2/3")
				}
				ulbPrecommits = ulbRound.Votes.Precommits(ulbRound.CommitRound)
				ulbValidators = ulbRound.Validators
			}
		}

		validators, _ = sm.LoadValidators(cs.blockExec.DB(), height)
	} else {
		validators, _ = sm.LoadValidators(cs.blockExec.DB(), height)
	}

	startTime := tmtime.Now()

	cs.roundStates.LoadOrStore(
		height,
		&cstypes.RoundState{
			Height:                    height,
			Round:                     0,
			Step:                      cstypes.RoundStepNewHeight,
			StartTime:                 startTime,
			Validators:                validators,
			Proposal:                  nil,
			ProposalBlock:             nil,
			ProposalBlockParts:        nil,
			LockedRound:               -1,
			LockedBlock:               nil,
			LockedBlockParts:          nil,
			ValidRound:                -1,
			ValidBlock:                nil,
			ValidBlockParts:           nil,
			Votes:                     cstypes.NewHeightVoteSet(cs.state.ChainID, height, validators),
			CommitRound:               -1,
			LastCommit:                ulbPrecommits,
			LastValidators:            ulbValidators,
			TriggeredTimeoutPrecommit: false,
		},
	)

	if _, hasTicker := cs.timeoutTickers.Load(height); !hasTicker {
		cs.timeoutTickers.Store(height, NewTimeoutTicker(cs.aggregatedTockChan))
		ticker, _ := cs.timeoutTickers.Load(height)
		ticker.(TimeoutTicker).Start()
	}

	// Finally, broadcast RoundState
	cs.newStep(height)

	return true
}

func (cs *ConsensusState) cleanupFinalizedRoundState(height int64) {
	if cs.blockStore.Height() < height {
		panic("Target height finalized not yet")
	}
	if ticker, hasTicker := cs.timeoutTickers.Load(height); hasTicker {
		ticker.(TimeoutTicker).Stop()
	}
	cs.roundStates.Delete(height)
	cs.timeoutTickers.Delete(height)
	if err := cs.privValidator.GetParallelProgressablePV().SetImmutableHeight(height); err != nil {
		panic(err)
	}
}

func (cs *ConsensusState) updateRoundStep(height int64, round int, step cstypes.RoundStepType) {
	heightRound := cs.getRoundState(height)
	if heightRound == nil {
		panic("Must be just initialized height round")
	}
	heightRound.Round = round
	heightRound.Step = step
}

// Enter : onStart
// Enter : received complete proposal block by previous height
func (cs *ConsensusState) scheduleNewHeightRound0(height int64) {
	// ignore commited height
	if cs.state.LastBlockHeight < height {
		go func() {
			if height > cs.state.ConsensusParams.Block.LenULB {
				//Waiting for ulb round commit
				for ulbHeight := height - cs.state.ConsensusParams.Block.LenULB; ulbHeight > cs.state.LastBlockHeight; {
					time.Sleep(time.Millisecond * 10)
				}
			}

			cs.newHeightQueue <- height
		}()
	}
}

// enterNewRound(height, 0) at cs.StartTime.
func (cs *ConsensusState) scheduleRound0(rs *cstypes.RoundState) {
	//cs.Logger.Info("scheduleRound0", "now", tmtime.Now(), "startTime", cs.StartTime)
	sleepDuration := rs.StartTime.Sub(tmtime.Now())
	cs.scheduleTimeout(sleepDuration, rs.Height, 0, cstypes.RoundStepNewHeight)
}

// Attempt to schedule a timeout (by sending timeoutInfo on the tickChan)
func (cs *ConsensusState) scheduleTimeout(duration time.Duration, height int64, round int, step cstypes.RoundStepType) {
	ticker, ok := cs.timeoutTickers.Load(height)
	if !ok {
		panic("Must be initialized ticker")
	}
	ticker.(TimeoutTicker).ScheduleTimeout(timeoutInfo{duration, height, round, step})
}

// send a msg into the receiveRoutine regarding our own proposal, block part, or vote
func (cs *ConsensusState) sendInternalMessage(mi msgInfo) {
	select {
	case cs.internalMsgQueue <- mi:
	default:
		// NOTE: using the go-routine means our votes can
		// be processed out of order.
		// TODO: use CList here for strict determinism and
		// attempt push to internalMsgQueue in receiveRoutine
		cs.Logger.Info("Internal msg queue is full. Using a go-routine")
		go func() { cs.internalMsgQueue <- mi }()
	}
}

// Reconstruct LastCommit from SeenCommit, which we saved along with the block,
// (which happens even before saving the state)
func (cs *ConsensusState) reconstructLastCommit(state sm.State) {
	if state.LastBlockHeight == 0 {
		return
	}

	startReconstructHeight := state.LastBlockHeight - cs.state.ConsensusParams.Block.LenULB + 1
	for height, i := startReconstructHeight, 0; height <= state.LastBlockHeight; height, i = height+1, i+1 {
		if height < 1 {
			continue
		}

		ulbValidators, err := sm.LoadValidators(cs.blockExec.DB(), height)
		if err != nil {
			panic("Cannot load ulb validators into reconstructLastCommit")
		}

		seenCommit := cs.blockStore.LoadSeenCommit(height)
		lastPrecommits := types.CommitToVoteSet(state.ChainID, seenCommit, ulbValidators)
		if !lastPrecommits.HasTwoThirdsMajority() {
			panic("Failed to reconstruct LastCommit: Does not have +2/3 maj")
		}

		restoreHeight := height + cs.state.ConsensusParams.Block.LenULB
		cs.updateHeight(restoreHeight)
		restoreRs := cs.getRoundState(restoreHeight)
		restoreRs.LastCommit = lastPrecommits
		restoreRs.LastValidators = ulbValidators
	}
}

// Updates ConsensusState and increments height to match that of state.
// The round becomes 0 and cs.Step becomes cstypes.RoundStepNewHeight.
func (cs *ConsensusState) updateToState(state sm.State) {
	if !cs.state.IsEmpty() {
		rs := cs.getRoundState(cs.state.LastBlockHeight + 1)

		if rs.CommitRound > -1 && 0 < rs.Height && rs.Height != state.LastBlockHeight {
			panic(fmt.Sprintf("updateToState() expected state height of %v but found %v",
				rs.Height, state.LastBlockHeight))
		}
		if !cs.state.IsEmpty() && cs.state.LastBlockHeight+1 != rs.Height {
			// This might happen when someone else is mutating cs.state.
			// Someone forgot to pass in state.Copy() somewhere?!
			panic(fmt.Sprintf("Inconsistent cs.state.LastBlockHeight+1 %v vs cs.Height %v",
				cs.state.LastBlockHeight+1, rs.Height))
		}

		// If state isn't further out than cs.state, just ignore.
		// This happens when SwitchToConsensus() is called in the reactor.
		// We don't want to reset e.g. the Votes, but we still want to
		// signal the new round step, because other services (eg. txNotifier)
		// depend on having an up-to-date peer state!
		if !cs.state.IsEmpty() && (state.LastBlockHeight <= cs.state.LastBlockHeight) {
			cs.Logger.Info("Ignoring updateToState()", "newHeight", state.LastBlockHeight+1, "oldHeight", cs.state.LastBlockHeight+1)
			cs.newStep(state.LastBlockHeight + 1)
			return
		}
	} else {

	}

	cs.state = state

	// Next desired block height
	height := state.LastBlockHeight + 1

	// RoundState fields
	cs.updateHeight(height)
}

func (cs *ConsensusState) newStep(height int64) {
	rs := cs.getRoundState(height)
	rsEvent := rs.RoundStateEvent()
	cs.wal.Write(rsEvent)
	cs.nSteps++
	// newStep is called by updateToState in NewConsensusState before the eventBus is set!
	if cs.eventBus != nil {
		cs.eventBus.PublishEventNewRoundStep(rsEvent)
		cs.evsw.FireEvent(types.EventNewRoundStep, rs)
	}
}

//-----------------------------------------
// the main go routines

// receiveRoutine handles messages which may cause state transitions.
// it's argument (n) is the number of messages to process before exiting - use 0 to run forever
// It keeps the RoundState and is the only thing that updates it.
// Updates (state transitions) happen on timeouts, complete proposals, and 2/3 majorities.
// ConsensusState must be locked before any internal state is updated.
func (cs *ConsensusState) receiveRoutine(maxSteps int) {
	onExit := func(cs *ConsensusState) {
		// NOTE: the internalMsgQueue may have signed messages from our
		// priv_val that haven't hit the WAL, but its ok because
		// priv_val tracks LastSig

		// close wal now that we're done writing to it
		cs.wal.Stop()
		cs.wal.Wait()

		close(cs.done)
	}

	defer func() {
		if r := recover(); r != nil {
			cs.Logger.Error("CONSENSUS FAILURE!!!", "err", r, "stack", string(debug.Stack()))
			// stop gracefully
			//
			// NOTE: We most probably shouldn't be running any further when there is
			// some unexpected panic. Some unknown error happened, and so we don't
			// know if that will result in the validator signing an invalid thing. It
			// might be worthwhile to explore a mechanism for manual resuming via
			// some console or secure RPC system, but for now, halting the chain upon
			// unexpected consensus bugs sounds like the better option.
			onExit(cs)
		}
	}()

	for {
		if maxSteps > 0 {
			if cs.nSteps >= maxSteps {
				cs.Logger.Info("reached max steps. exiting receive routine")
				cs.nSteps = 0
				return
			}
		}
		var mi msgInfo

		select {
		case <-cs.txNotifier.TxsAvailable():
			go cs.handleTxsAvailable()
		case mi = <-cs.peerMsgQueue:
			cs.wal.Write(mi)
			// handles proposals, block parts, votes
			// may generate internal events (votes, complete proposals, 2/3 majorities)
			go cs.handleMsg(mi)
		case mi = <-cs.internalMsgQueue:
			err := cs.wal.WriteSync(mi) // NOTE: fsync
			if err != nil {
				panic(fmt.Sprintf("Failed to write %v msg to consensus wal due to %v. Check your FS and restart the node", mi, err))
			}

			if _, ok := mi.Msg.(*VoteMessage); ok {
				// we actually want to simulate failing during
				// the previous WriteSync, but this isn't easy to do.
				// Equivalent would be to fail here and manually remove
				// some bytes from the end of the wal.
				fail.Fail() // XXX
			}

			// handles proposals, block parts, votes
			go cs.handleMsg(mi)
		case ti := <-cs.aggregatedTockChan: // tockChan:
			// TODO: this commit purpose serve to prepare multiple round on TimeoutTicker
			// so, not handled to each height yet
			cs.wal.Write(ti)
			// if the timeout is relevant to the rs
			// go to the next step
			go cs.handleTimeout(ti)
		case height := <-cs.newHeightQueue:
			newHeightRound := cs.getRoundState(height)
			if newHeightRound == nil {
				cs.updateHeight(height)
				newHeightRound = cs.getRoundState(height)
			}

			cs.scheduleRound0(newHeightRound)
		case <-cs.Quit():
			onExit(cs)
			return
		}
	}
}

// state transitions on complete-proposal, 2/3-any, 2/3-one
func (cs *ConsensusState) handleMsg(mi msgInfo) {
	var (
		added bool
		err   error
	)
	msg, peerID := mi.Msg, mi.PeerID
	switch msg := msg.(type) {
	case *ProposalMessage:
		// will not cause transition.
		// once proposal is set, we can receive block parts
		err = cs.setProposal(msg.Proposal)
	case *BlockPartMessage:
		// if the proposal is complete, we'll enterPrevote or tryFinalizeCommit
		added, err = cs.addProposalBlockPart(msg, peerID)
		if added {
			cs.statsMsgQueue <- mi
		}

		// if err != nil && msg.Round != cs.Round {
		// 	cs.Logger.Debug("Received block part from wrong round", "height", cs.Height, "csRound", cs.Round, "blockRound", msg.Round)
		// 	err = nil
		// }
	case *VoteMessage:
		// attempt to add the vote and dupeout the validator if its a duplicate signature
		// if the vote gives us a 2/3-any or 2/3-one, we transition
		added, err = cs.tryAddVote(msg.Vote, peerID)
		if added {
			cs.statsMsgQueue <- mi
		}

		// if err == ErrAddingVote {
		// TODO: punish peer
		// We probably don't want to stop the peer here. The vote does not
		// necessarily comes from a malicious peer but can be just broadcasted by
		// a typical peer.
		// https://github.com/tendermint/tendermint/issues/1281
		// }

		// NOTE: the vote is broadcast to peers by the reactor listening
		// for vote events

		// TODO: If rs.Height == vote.Height && rs.Round < vote.Round,
		// the peer is sending us CatchupCommit precommits.
		// We could make note of this and help filter in broadcastHasVoteMessage().
	default:
		cs.Logger.Error("Unknown msg type", "type", reflect.TypeOf(msg))
		return
	}

	if err != nil { // nolint:staticcheck
		// Causes TestReactorValidatorSetChanges to timeout
		// https://github.com/tendermint/tendermint/issues/3406
		// cs.Logger.Error("Error with msg", "height", cs.Height, "round", cs.Round,
		// 	"peer", peerID, "err", err, "msg", msg)
	}
}

func (cs *ConsensusState) handleTimeout(ti timeoutInfo) {
	cs.Logger.Debug("Received tock", "timeout", ti.Duration, "height", ti.Height, "round", ti.Round, "step", ti.Step)
	rs := cs.getRoundState(ti.Height)

	// timeouts must be for current height, round, step
	if rs == nil || ti.Round < rs.Round || (ti.Round == rs.Round && ti.Step < rs.Step) {
		if rs != nil {
			cs.Logger.Debug("Ignoring tock because we're ahead", "height", rs.Height, "round", rs.Round, "step", rs.Step)
		}
		return
	}

	rs.Lock()
	defer rs.Unlock()

	switch ti.Step {
	case cstypes.RoundStepNewHeight:
		// NewRound event fired from enterNewRound.
		// XXX: should we fire timeout here (for timeout commit)?
		cs.enterNewRound(ti.Height, 0)
	case cstypes.RoundStepNewRound:
		cs.enterPropose(ti.Height, 0)
	case cstypes.RoundStepPropose:
		cs.eventBus.PublishEventTimeoutPropose(rs.RoundStateEvent())
		cs.enterPrevote(ti.Height, ti.Round)
	case cstypes.RoundStepPrevoteWait:
		cs.eventBus.PublishEventTimeoutWait(rs.RoundStateEvent())
		cs.enterPrecommit(ti.Height, ti.Round)
	case cstypes.RoundStepPrecommitWait:
		cs.eventBus.PublishEventTimeoutWait(rs.RoundStateEvent())
		cs.enterPrecommit(ti.Height, ti.Round)
		cs.enterNewRound(ti.Height, ti.Round+1)
	default:
		panic(fmt.Sprintf("Invalid timeout step: %v", ti.Step))
	}

}

func (cs *ConsensusState) handleTxsAvailable() {
	cs.mtx.Lock()
	defer cs.mtx.Unlock()

	rs := cs.getRoundState(cs.GetState().LastBlockHeight + 1)

	// We only need to do this for round 0.
	if rs.Round != 0 {
		return
	}

	switch rs.Step {
	case cstypes.RoundStepNewHeight: // timeoutCommit phase
		if cs.needProofBlock(rs.Height) {
			// enterPropose will be called by enterNewRound
			return
		}

		// +1ms to ensure RoundStepNewRound timeout always happens after RoundStepNewHeight
		timeoutCommit := rs.StartTime.Sub(tmtime.Now()) + 1*time.Millisecond
		cs.scheduleTimeout(timeoutCommit, rs.Height, 0, cstypes.RoundStepNewRound)
	case cstypes.RoundStepNewRound: // after timeoutCommit
		cs.enterPropose(rs.Height, 0)
	}
}

//-----------------------------------------------------------------------------
// State functions
// Used internally by handleTimeout and handleMsg to make state transitions

// Enter: `timeoutNewHeight` by startTime (commitTime+timeoutCommit),
// 	or, if SkipTimeoutCommit==true, after receiving all precommits from (height,round-1)
// Enter: `timeoutPrecommits` after any +2/3 precommits from (height,round-1)
// Enter: +2/3 precommits for nil at (height,round-1)
// Enter: +2/3 prevotes any or +2/3 precommits for block or any from (height, round)
// NOTE: cs.StartTime was already set for height.
func (cs *ConsensusState) enterNewRound(height int64, round int) {
	logger := cs.Logger.With("height", height, "round", round)
	heightRound := cs.getRoundState(height)
	if heightRound == nil {
		return
	}

	if round < heightRound.Round || (heightRound.Round == round && heightRound.Step != cstypes.RoundStepNewHeight) {
		logger.Debug(fmt.Sprintf("enterNewRound(%v/%v): Invalid args. Current step: %v/%v", height, round, heightRound.Round, heightRound.Step))
		return
	}

	if now := tmtime.Now(); heightRound.StartTime.After(now) {
		logger.Info("Need to set a buffer and log message here for sanity.", "startTime", heightRound.StartTime, "now", now)
	}

	logger.Info(fmt.Sprintf("enterNewRound(%v/%v). Current: %v/%v", height, round, heightRound.Round, heightRound.Step))
	// Increment validators if necessary
	validators := heightRound.Validators
	if heightRound.Round < round {
		validators.IncrementProposerPriority(round - heightRound.Round)
	}

	// Setup new round
	// we don't fire newStep for this step,
	// but we fire an event, so update the round step first
	cs.updateRoundStep(height, round, cstypes.RoundStepNewRound)
	heightRound.Validators = validators
	if round == 0 {
		// We've already reset these upon new height,
		// and meanwhile we might have received a proposal
		// for round 0.
	} else {
		logger.Info("Resetting Proposal info")

		// Unlocking txs if exist proposal block at previous round
		if heightRound.ProposalBlock != nil {
			cs.blockExec.UnreserveBlock(cs.state, heightRound.ProposalBlock)
		}

		heightRound.Proposal = nil
		heightRound.ProposalBlock = nil
		heightRound.ProposalBlockParts = nil
	}
	heightRound.Votes.SetRound(round + 1) // also track next round (round+1) to allow round-skipping
	heightRound.TriggeredTimeoutPrecommit = false

	cs.eventBus.PublishEventNewRound(heightRound.NewRoundEvent())
	cs.metrics.Rounds.Set(float64(round))

	if height > cs.state.ConsensusParams.Block.LenULB {
		ulbHeight := height - cs.state.ConsensusParams.Block.LenULB
		ulbRound := -1
		if ulbRs := cs.GetRoundState(ulbHeight); ulbRs != nil {
			ulbRound = ulbRs.Round
		}
		// NOTE: If the consensus of the previously ulb block fails,
		// sleep to sufficient time for receive a new previous block and consensus to proceed.
		// If there is no waiting time, the connected now height blocks will fail consecutively,
		// so round number will not dcrease.
		if ulbRound > 0 && round == 0 {
			logger.Info(fmt.Sprintf("Wait for cut off to continuous failure. Ulb: %v/%v", ulbHeight, ulbRound))
			time.Sleep(cs.config.PreviousFailure(ulbRound))
		}
	}

	// Wait for txs to be available in the mempool
	// before we enterPropose in round 0. If the last block changed the app hash,
	// we may need an empty "proof" block, and enterPropose immediately.
	waitForTxs := cs.config.WaitForTxs() && round == 0 && !cs.needProofBlock(height)
	if waitForTxs {
		if cs.config.CreateEmptyBlocksInterval > 0 {
			cs.scheduleTimeout(cs.config.CreateEmptyBlocksInterval, height, round,
				cstypes.RoundStepNewRound)
		}
	} else {
		cs.enterPropose(height, round)
	}
}

// needProofBlock returns true on the first height (so the genesis app hash is signed right away)
// and where the last block (height-1) caused the app hash to change
func (cs *ConsensusState) needProofBlock(height int64) bool {
	if height == 1 {
		return true
	}

	lastBlockMeta := cs.blockStore.LoadBlockMeta(height - 1)
	return !bytes.Equal(cs.state.AppHash, lastBlockMeta.Header.AppHash)
}

// Enter (CreateEmptyBlocks): from enterNewRound(height,round)
// Enter (CreateEmptyBlocks, CreateEmptyBlocksInterval > 0 ): after enterNewRound(height,round), after timeout of CreateEmptyBlocksInterval
// Enter (!CreateEmptyBlocks) : after enterNewRound(height,round), once txs are in the mempool
func (cs *ConsensusState) enterPropose(height int64, round int) {
	logger := cs.Logger.With("height", height, "round", round)

	heightRound := cs.getRoundState(height)
	if heightRound == nil {
		return
	}

	if round < heightRound.Round || (heightRound.Round == round && cstypes.RoundStepPropose <= heightRound.Step) {
		logger.Debug(fmt.Sprintf("enterPropose(%v/%v): Invalid args. Current step: %v/%v", height, round, heightRound.Round, heightRound.Step))
		return
	}
	logger.Info(fmt.Sprintf("enterPropose(%v/%v). Current: %v/%v", height, round, heightRound.Round, heightRound.Step))

	defer func() {
		// Done enterPropose:
		cs.updateRoundStep(height, round, cstypes.RoundStepPropose)
		cs.newStep(height)

		// If we have the whole proposal + POL, then goto Prevote now.
		// else, we'll enterPrevote when the rest of the proposal is received (in AddProposalBlockPart),
		// or else after timeoutPropose
		if cs.isProposalComplete(height) {
			cs.enterPrevote(height, heightRound.Round)
		}
	}()

	// If we don't get the proposal and all block parts quick enough, enterPrevote
	cs.scheduleTimeout(cs.config.Propose(round), height, round, cstypes.RoundStepPropose)

	// Nothing more to do if we're not a validator
	if cs.privValidator == nil {
		logger.Debug("This node is not a validator")
		return
	}

	// if not a validator, we're done
	address := cs.privValidator.GetPubKey().Address()
	if !heightRound.Validators.HasAddress(address) {
		logger.Debug("This node is not a validator", "addr", address, "vals", heightRound.Validators)
		return
	}
	logger.Debug("This node is a validator")

	if cs.isProposer(height, address) {
		logger.Info("enterPropose: Our turn to propose", "proposer", heightRound.Validators.GetProposer().Address, "privValidator", cs.privValidator)
		cs.decideProposal(height, round)
	} else {
		logger.Info("enterPropose: Not our turn to propose", "proposer", heightRound.Validators.GetProposer().Address, "privValidator", cs.privValidator)
	}
}

func (cs *ConsensusState) isProposer(height int64, address []byte) bool {
	heightRound := cs.getRoundState(height)
	if heightRound == nil {
		panic("Must be just initialized height round")
	}

	return bytes.Equal(heightRound.Validators.GetProposer().Address, address)
}

func (cs *ConsensusState) defaultDecideProposal(height int64, round int) {
	var block *types.Block
	var blockParts *types.PartSet
	heightRound := cs.getRoundState(height)
	if heightRound == nil {
		panic("Must be just initialized height round")
	}

	// Decide on block
	if heightRound.ValidBlock != nil && cs.validatePreviousBlock(heightRound.ValidBlock) == nil {
		// If there is valid block, choose that.
		block, blockParts = heightRound.ValidBlock, heightRound.ValidBlockParts
	} else {
		// Create a new proposal block from state/txs from the mempool.
		block, blockParts = cs.createProposalBlock(height)
		if block == nil { // on error
			return
		}
	}

	// Flush the WAL. Otherwise, we may not recompute the same proposal to sign, and the privValidator will refuse to sign anything.
	cs.wal.FlushAndSync()

	// Make proposal
	propBlockId := types.BlockID{Hash: block.Hash(), PartsHeader: blockParts.Header()}
	proposal := types.NewProposal(height, round, heightRound.ValidRound, propBlockId)
	if err := cs.privValidator.SignProposal(cs.state.ChainID, proposal); err == nil {

		// send proposal and block parts on internal msg queue
		cs.sendInternalMessage(msgInfo{&ProposalMessage{proposal}, ""})
		// NOTE: message handlers are executed in parallel(goroutine).
		// So sometimes the block part message is processed before the proposal message.
		// There is a slight delay.
		time.Sleep(time.Millisecond * 100)

		for i := 0; i < blockParts.Total(); i++ {
			part := blockParts.GetPart(i)
			cs.sendInternalMessage(msgInfo{&BlockPartMessage{heightRound.Height, heightRound.Round, part}, ""})
		}
		cs.Logger.Info("Signed proposal", "height", height, "round", round, "proposal", proposal)
		cs.Logger.Debug(fmt.Sprintf("Signed proposal block: %v", block))
	} else if !cs.replayMode {
		cs.Logger.Error("enterPropose: Error signing proposal", "height", height, "round", round, "err", err)
	}
}

// Returns true if the proposal block is complete &&
// (if POLRound was proposed, we have +2/3 prevotes from there).
func (cs *ConsensusState) isProposalComplete(height int64) bool {
	heightRound := cs.getRoundState(height)
	if heightRound == nil {
		panic("Must be just initialized height round")
	}

	if heightRound.Proposal == nil || heightRound.ProposalBlock == nil {
		return false
	}
	// we have the proposal. if there's a POLRound,
	// make sure we have the prevotes from it too
	if heightRound.Proposal.POLRound < 0 {
		return true
	}
	// if this is false the proposer is lying or we haven't received the POL yet
	return heightRound.Votes.Prevotes(heightRound.Proposal.POLRound).HasTwoThirdsMajority()

}

// Create the next block to propose and return it.
// We really only need to return the parts, but the block
// is returned for convenience so we can log the proposal block.
// Returns nil block upon error.
// NOTE: keep it side-effect free for clarity.
func (cs *ConsensusState) createProposalBlock(height int64) (block *types.Block, blockParts *types.PartSet) {
	var ulbCommit *types.Commit
	var ulbValidators *types.ValidatorSet
	var validators *types.ValidatorSet
	var ulbNextValidators *types.ValidatorSet
	var appHash []byte
	var resultsHash []byte

	var valsErr error
	if validators, valsErr = sm.LoadValidators(cs.blockExec.DB(), height); valsErr != nil {
		panic(fmt.Sprintf("Cannot load Validators. height=%v, LastBlockHeight=%v, error=%v", height, cs.state.LastBlockHeight, valsErr.Error()))
	}

	if height <= cs.state.ConsensusParams.Block.LenULB {
		// We're creating a proposal for the previous ULB block.
		// The commit is empty, but not nil.
		ulbCommit = types.NewCommit(types.BlockID{}, nil)
		ulbValidators = types.NewValidatorSet(nil)
		ulbNextValidators = types.NewValidatorSet(nil)
	} else if rs := cs.getRoundState(height); rs != nil && rs.LastCommit.HasTwoThirdsMajority() {
		ulbHeight := height - cs.state.ConsensusParams.Block.LenULB

		// Make the commit from ULBCommit
		ulbCommit = rs.LastCommit.MakeCommit()
		ulbValidators = rs.LastValidators

		// Getting committed block informations from db
		var appHashErr error
		appHash, appHashErr = sm.LoadAppHash(cs.blockExec.DB(), ulbHeight)
		if appHashErr != nil {
			panic(fmt.Sprintf("Cannot load ulb AppHash. ulbHeight=%v, LastBlockHeight=%v, error=%v", ulbHeight, cs.state.LastBlockHeight, appHashErr.Error()))
		}
		ulbABCIResponses, resErr := sm.LoadABCIResponses(cs.blockExec.DB(), ulbHeight)
		if resErr != nil {
			panic(fmt.Sprintf("Cannot load ulb ABCI responses. ulbHeight=%v, LastBlockHeight=%v, error=%v", ulbHeight, cs.state.LastBlockHeight, resErr.Error()))
		}
		resultsHash = ulbABCIResponses.ResultsHash()

		nextValidatorsHeight := ulbHeight + cs.state.ConsensusParams.Block.LenULB + 1
		var ulbNextVarErr error
		ulbNextValidators, ulbNextVarErr = sm.LoadValidators(cs.blockExec.DB(), nextValidatorsHeight)
		if ulbNextVarErr != nil {
			panic(fmt.Sprintf("Cannot load ulb NextValidators. NextValidatorHeight=%v, LastBlockHeight=%v, error=%v", nextValidatorsHeight, cs.state.LastBlockHeight, ulbNextVarErr.Error()))
		}
	} else {
		// This shouldn't happen.
		cs.Logger.Error("enterPropose: Cannot propose anything: No commit for the previous block.", "height", height)
		return
	}

	var prevBlockID types.BlockID
	var prevTotalTxs int64
	if height != 1 {
		if cs.state.LastBlockHeight >= height-1 {
			prevMeta := cs.blockStore.LoadBlockMeta(height - 1)
			if prevMeta == nil {
				panic("createProposalBlock must be call when after received previous block")
			}
			//attach to commited block
			prevBlockID = prevMeta.BlockID
			prevTotalTxs = prevMeta.Header.TotalTxs
		} else if prevRs := cs.GetRoundState(height - 1); prevRs != nil && prevRs.ProposalBlock != nil {
			//attach to progressing block
			prevBlockID = types.BlockID{Hash: prevRs.ProposalBlock.Hash(), PartsHeader: prevRs.ProposalBlockParts.Header()}
			prevTotalTxs = prevRs.ProposalBlock.TotalTxs
		} else {
			//prev height already going next rounds, so reseted ProposalBlock situation now
			cs.Logger.Info("consensus of previous proposal block is failed into createProposalBlock")
			return
		}
	}

	proposerAddr := cs.privValidator.GetPubKey().Address()
	return cs.blockExec.CreateProposalBlockFromArgs(
		height,
		prevBlockID, prevTotalTxs,
		cs.state,
		ulbCommit, ulbValidators,
		validators.Hash(), ulbNextValidators.Hash(), appHash, resultsHash,
		proposerAddr)
}

func (cs *ConsensusState) validatePreviousBlock(block *types.Block) error {
	previousHeight := block.Height - 1
	if previousHeight > cs.state.LastBlockHeight {
		var prevID types.BlockID

		prevRs := cs.GetRoundState(previousHeight)
		if prevRs == nil {
			//one more check if after commited
			if previousHeight <= cs.state.LastBlockHeight {
				prevMeta := cs.blockStore.LoadBlockMeta(previousHeight)
				if prevMeta == nil {
					panic(fmt.Sprintf("cannot found commit block meta height=%v", previousHeight))
				}
				prevID = prevMeta.BlockID
			} else {
				return fmt.Errorf("cannot found RoundState at previous height")
			}
		} else {
			prevID = types.BlockID{Hash: prevRs.ProposalBlock.Hash(), PartsHeader: prevRs.ProposalBlockParts.Header()}
		}

		if !block.LastBlockID.Equals(prevID) {
			return &sm.ErrLastBlockIDMismatch{block.LastBlockID, prevID}
		}
	} else if previousHeight > 0 {
		if prevMeta := cs.blockStore.LoadBlockMeta(previousHeight); prevMeta == nil {
			panic(fmt.Sprintf("cannot found commit block meta height=%v", previousHeight))
		} else if !block.LastBlockID.Equals(prevMeta.BlockID) {
			return &sm.ErrLastBlockIDMismatch{block.LastBlockID, prevMeta.BlockID}
		}
	}

	return nil
}

// Enter: `timeoutPropose` after entering Propose.
// Enter: proposal block and POL is ready.
// Prevote for LockedBlock if we're locked, or ProposalBlock if valid.
// Otherwise vote nil.
func (cs *ConsensusState) enterPrevote(height int64, round int) {
	heightRound := cs.getRoundState(height)
	if heightRound == nil {
		return
	}

	if round < heightRound.Round || (heightRound.Round == round && cstypes.RoundStepPrevote <= heightRound.Step) {
		cs.Logger.Debug(fmt.Sprintf("enterPrevote(%v/%v): Invalid args. Current step: %v/%v", height, round, heightRound.Round, heightRound.Step))
		return
	}

	defer func() {
		// Done enterPrevote:
		cs.updateRoundStep(height, round, cstypes.RoundStepPrevote)
		cs.newStep(height)
	}()

	cs.Logger.Info(fmt.Sprintf("enterPrevote(%v/%v). Current: %v/%v", height, round, heightRound.Round, heightRound.Step))

	// Sign and broadcast vote as necessary
	cs.doPrevote(height, round)

	// Once `addVote` hits any +2/3 prevotes, we will go to PrevoteWait
	// (so we have more time to try and collect +2/3 prevotes for a single block)
}

func (cs *ConsensusState) defaultDoPrevote(height int64, round int) {
	logger := cs.Logger.With("height", height, "round", round)

	heightRound := cs.getRoundState(height)
	if heightRound == nil {
		panic("Must be just initialized height round")
	}

	// If a block is locked, prevote that.
	if heightRound.LockedBlock != nil {
		// Validate previous block if when progressing
		err := cs.validatePreviousBlock(heightRound.LockedBlock)
		if err != nil {
			// ProposalBlock is invalid, prevote nil.
			logger.Info("enterPrevote: locked block linked previous block is invalid. unlocking", "err", err)
			heightRound.LockedRound = -1
			heightRound.LockedBlock = nil
			heightRound.LockedBlockParts = nil
			cs.eventBus.PublishEventUnlock(heightRound.RoundStateEvent())
		} else {
			logger.Info("enterPrevote: Block was locked")
			cs.signAddVote(height, types.PrevoteType, heightRound.LockedBlock.Hash(), heightRound.LockedBlockParts.Header())
			return
		}
	}

	// If ProposalBlock is nil, prevote nil.
	if heightRound.ProposalBlock == nil {
		logger.Info("enterPrevote: ProposalBlock is nil")
		cs.signAddVote(height, types.PrevoteType, nil, types.PartSetHeader{})
		return
	}

	// Validate proposal block
	err := cs.blockExec.ValidateBlock(cs.state, heightRound.ProposalBlock)
	if err != nil {
		// ProposalBlock is invalid, prevote nil.
		isErrorLevel := true
		switch err.(type) {
		case *sm.ErrLastBlockIDMismatch:
			heightRound.ValidRound = -1
			heightRound.ValidBlock = nil
			heightRound.ValidBlockParts = nil
			isErrorLevel = false
		}
		if isErrorLevel {
			logger.Error("enterPrevote: ProposalBlock is invalid", "err", err)
		} else {
			logger.Info("enterPrevote: ProposalBlock is invalid", "err", err)
		}
		cs.signAddVote(height, types.PrevoteType, nil, types.PartSetHeader{})
		return
	}

	// Validate previous block if when progressing
	err = cs.validatePreviousBlock(heightRound.ProposalBlock)
	if err != nil {
		// ProposalBlock is invalid, prevote nil.
		logger.Info("enterPrevote: previous block is invalid", "err", err)
		heightRound.ValidRound = -1
		heightRound.ValidBlock = nil
		heightRound.ValidBlockParts = nil
		cs.signAddVote(height, types.PrevoteType, nil, types.PartSetHeader{})
		return
	}

	// Prevote cs.ProposalBlock
	// NOTE: the proposal signature is validated when it is received,
	// and the proposal block parts are validated as they are received (against the merkle hash in the proposal)
	logger.Info("enterPrevote: ProposalBlock is valid")
	cs.signAddVote(height, types.PrevoteType, heightRound.ProposalBlock.Hash(), heightRound.ProposalBlockParts.Header())
}

// Enter: any +2/3 prevotes at next round.
func (cs *ConsensusState) enterPrevoteWait(height int64, round int) {
	logger := cs.Logger.With("height", height, "round", round)

	heightRound := cs.getRoundState(height)
	if heightRound == nil {
		return
	}

	if round < heightRound.Round || (heightRound.Round == round && cstypes.RoundStepPrevoteWait <= heightRound.Step) {
		logger.Debug(fmt.Sprintf("enterPrevoteWait(%v/%v): Invalid args. Current step: %v/%v", height, round, heightRound.Round, heightRound.Step))
		return
	}
	if !heightRound.Votes.Prevotes(round).HasTwoThirdsAny() {
		panic(fmt.Sprintf("enterPrevoteWait(%v/%v), but Prevotes does not have any +2/3 votes", height, round))
	}
	logger.Info(fmt.Sprintf("enterPrevoteWait(%v/%v). Current: %v/%v", height, round, heightRound.Round, heightRound.Step))

	defer func() {
		// Done enterPrevoteWait:
		cs.updateRoundStep(height, round, cstypes.RoundStepPrevoteWait)
		cs.newStep(height)
	}()

	// Wait for some more prevotes; enterPrecommit
	cs.scheduleTimeout(cs.config.Prevote(round), height, round, cstypes.RoundStepPrevoteWait)
}

// Enter: `timeoutPrevote` after any +2/3 prevotes.
// Enter: `timeoutPrecommit` after any +2/3 precommits.
// Enter: +2/3 precomits for block or nil.
// Lock & precommit the ProposalBlock if we have enough prevotes for it (a POL in this round)
// else, unlock an existing lock and precommit nil if +2/3 of prevotes were nil,
// else, precommit nil otherwise.
func (cs *ConsensusState) enterPrecommit(height int64, round int) {
	logger := cs.Logger.With("height", height, "round", round)

	heightRound := cs.getRoundState(height)
	if heightRound == nil {
		return
	}

	if round < heightRound.Round || (heightRound.Round == round && cstypes.RoundStepPrecommit <= heightRound.Step) {
		logger.Debug(fmt.Sprintf("enterPrecommit(%v/%v): Invalid args. Current step: %v/%v", height, round, heightRound.Round, heightRound.Step))
		return
	}

	logger.Info(fmt.Sprintf("enterPrecommit(%v/%v). Current: %v/%v", height, round, heightRound.Round, heightRound.Step))

	defer func() {
		// Done enterPrecommit:
		cs.updateRoundStep(height, round, cstypes.RoundStepPrecommit)
		cs.newStep(height)
	}()

	// check for a polka
	blockID, ok := heightRound.Votes.Prevotes(round).TwoThirdsMajority()

	// If we don't have a polka, we must precommit nil.
	if !ok {
		if heightRound.LockedBlock != nil {
			logger.Info("enterPrecommit: No +2/3 prevotes during enterPrecommit while we're locked. Precommitting nil")
		} else {
			logger.Info("enterPrecommit: No +2/3 prevotes during enterPrecommit. Precommitting nil.")
		}
		cs.signAddVote(height, types.PrecommitType, nil, types.PartSetHeader{})
		return
	}

	// At this point +2/3 prevoted for a particular block or nil.
	cs.eventBus.PublishEventPolka(heightRound.RoundStateEvent())

	// the latest POLRound should be this round.
	polRound, _ := heightRound.Votes.POLInfo()
	if polRound < round {
		panic(fmt.Sprintf("This POLRound should be %v but got %v", round, polRound))
	}

	// +2/3 prevoted nil. Unlock and precommit nil.
	if len(blockID.Hash) == 0 {
		if heightRound.LockedBlock == nil {
			logger.Info("enterPrecommit: +2/3 prevoted for nil.")
		} else {
			logger.Info("enterPrecommit: +2/3 prevoted for nil. Unlocking")
			heightRound.LockedRound = -1
			heightRound.LockedBlock = nil
			heightRound.LockedBlockParts = nil
			cs.eventBus.PublishEventUnlock(heightRound.RoundStateEvent())
		}
		cs.signAddVote(height, types.PrecommitType, nil, types.PartSetHeader{})
		return
	}

	// At this point, +2/3 prevoted for a particular block.

	// If we're already locked on that block, precommit it, and update the LockedRound
	if heightRound.LockedBlock.HashesTo(blockID.Hash) {
		if err := cs.validatePreviousBlock(heightRound.LockedBlock); err != nil {
			logger.Info("enterPrecommit: lockedBlock linked previous block is invalid. Unlocking", "err", err)
			heightRound.LockedRound = -1
			heightRound.LockedBlock = nil
			heightRound.LockedBlockParts = nil
			cs.eventBus.PublishEventUnlock(heightRound.RoundStateEvent())
			cs.signAddVote(height, types.PrecommitType, nil, types.PartSetHeader{})
			return
		}

		logger.Info("enterPrecommit: +2/3 prevoted locked block. Relocking")
		heightRound.LockedRound = round
		cs.eventBus.PublishEventRelock(heightRound.RoundStateEvent())
		cs.signAddVote(height, types.PrecommitType, blockID.Hash, blockID.PartsHeader)
		return
	}

	// If +2/3 prevoted for proposal block, stage and precommit it
	if heightRound.ProposalBlock.HashesTo(blockID.Hash) {
		// Validate the block.
		if err := cs.blockExec.ValidateBlock(cs.state, heightRound.ProposalBlock); err != nil {
			switch err.(type) {
			case *sm.ErrLastBlockIDMismatch:
				heightRound.ValidRound = -1
				heightRound.ValidBlock = nil
				heightRound.ValidBlockParts = nil
				cs.signAddVote(height, types.PrecommitType, nil, types.PartSetHeader{})
				return
			}
			panic(fmt.Sprintf("enterPrecommit: +2/3 prevoted for an invalid block: %v", err))
		}
		// Validate previous block if when progressing
		if err := cs.validatePreviousBlock(heightRound.ProposalBlock); err != nil {
			// ProposalBlock is invalid, precommit nil.
			logger.Info("enterPrecommit: previous block is invalid", "err", err)
			heightRound.ValidRound = -1
			heightRound.ValidBlock = nil
			heightRound.ValidBlockParts = nil
			cs.signAddVote(height, types.PrecommitType, nil, types.PartSetHeader{})
			return
		}

		logger.Info("enterPrecommit: +2/3 prevoted proposal block. Locking", "hash", blockID.Hash)
		heightRound.LockedRound = round
		heightRound.LockedBlock = heightRound.ProposalBlock
		heightRound.LockedBlockParts = heightRound.ProposalBlockParts
		cs.eventBus.PublishEventLock(heightRound.RoundStateEvent())
		cs.signAddVote(height, types.PrecommitType, blockID.Hash, blockID.PartsHeader)
		return
	}

	// There was a polka in this round for a block we don't have.
	// Fetch that block, unlock, and precommit nil.
	// The +2/3 prevotes for this round is the POL for our unlock.
	// TODO: In the future save the POL prevotes for justification.
	heightRound.LockedRound = -1
	heightRound.LockedBlock = nil
	heightRound.LockedBlockParts = nil
	if !heightRound.ProposalBlockParts.HasHeader(blockID.PartsHeader) {
		cs.blockExec.UnreserveBlock(cs.state, heightRound.ProposalBlock)
		heightRound.ProposalBlock = nil
		heightRound.ProposalBlockParts = types.NewPartSetFromHeader(blockID.PartsHeader)
	}
	cs.eventBus.PublishEventUnlock(heightRound.RoundStateEvent())
	cs.signAddVote(height, types.PrecommitType, nil, types.PartSetHeader{})
}

// Enter: any +2/3 precommits for next round.
func (cs *ConsensusState) enterPrecommitWait(height int64, round int) {
	logger := cs.Logger.With("height", height, "round", round)

	heightRound := cs.getRoundState(height)
	if heightRound == nil {
		return
	}
	if round < heightRound.Round || (heightRound.Round == round && heightRound.TriggeredTimeoutPrecommit) {
		logger.Debug(
			fmt.Sprintf(
				"enterPrecommitWait(%v/%v): Invalid args. "+
					"Current state is Round: %v, TriggeredTimeoutPrecommit:%v",
				height, round, heightRound.Round, heightRound.TriggeredTimeoutPrecommit))
		return
	}
	if !heightRound.Votes.Precommits(round).HasTwoThirdsAny() {
		panic(fmt.Sprintf("enterPrecommitWait(%v/%v), but Precommits does not have any +2/3 votes", height, round))
	}
	logger.Info(fmt.Sprintf("enterPrecommitWait(%v/%v). Current: %v/%v/%v", height, round, heightRound.Height, heightRound.Round, heightRound.Step))

	defer func() {
		// Done enterPrecommitWait:
		heightRound.TriggeredTimeoutPrecommit = true
		cs.newStep(height)
	}()

	// Wait for some more precommits; enterNewRound
	cs.scheduleTimeout(cs.config.Precommit(round), height, round, cstypes.RoundStepPrecommitWait)

}

// Enter: +2/3 precommits for block
func (cs *ConsensusState) enterCommit(height int64, commitRound int) {
	logger := cs.Logger.With("height", height, "commitRound", commitRound)

	heightRound := cs.getRoundState(height)
	if heightRound == nil {
		return
	}
	if commitRound != heightRound.Round || (heightRound.Round == commitRound && cstypes.RoundStepCommit <= heightRound.Step) {
		logger.Debug(fmt.Sprintf("enterCommit(%v/%v): Invalid args. Current step: %v/%v/%v", height, commitRound, heightRound.Height, heightRound.Round, heightRound.Step))
		return
	}
	logger.Info(fmt.Sprintf("enterCommit(%v/%v). Current: %v/%v/%v", height, commitRound, heightRound.Height, heightRound.Round, heightRound.Step))

	defer func() {
		// Done enterCommit:
		// keep cs.Round the same, commitRound points to the right Precommits set.
		cs.updateRoundStep(height, heightRound.Round, cstypes.RoundStepCommit)
		cs.newStep(height)

		// Maybe finalize immediately.
		cs.tryFinalizeCommit(height)
	}()

	blockID, ok := heightRound.Votes.Precommits(commitRound).TwoThirdsMajority()
	if !ok {
		panic("RunActionCommit() expects +2/3 precommits")
	}

	// The Locked* fields no longer matter.
	// Move them over to ProposalBlock if they match the commit hash,
	// otherwise they'll be cleared in updateToState.
	if heightRound.LockedBlock.HashesTo(blockID.Hash) {
		logger.Info("Commit is for locked block. Set ProposalBlock=LockedBlock", "blockHash", blockID.Hash)
		heightRound.ProposalBlock = heightRound.LockedBlock
		heightRound.ProposalBlockParts = heightRound.LockedBlockParts
	}

	// If we don't have the block being committed, set up to get it.
	if !heightRound.ProposalBlock.HashesTo(blockID.Hash) {
		if !heightRound.ProposalBlockParts.HasHeader(blockID.PartsHeader) {
			logger.Info("Commit is for a block we don't know about. Set ProposalBlock=nil", "proposal", heightRound.ProposalBlock.Hash(), "commit", blockID.Hash)
			// We're getting the wrong block.
			// Set up ProposalBlockParts and keep waiting.
			cs.blockExec.UnreserveBlock(cs.state, heightRound.ProposalBlock)
			heightRound.ProposalBlock = nil
			heightRound.ProposalBlockParts = types.NewPartSetFromHeader(blockID.PartsHeader)
			cs.eventBus.PublishEventValidBlock(heightRound.RoundStateEvent())
			cs.evsw.FireEvent(types.EventValidBlock, heightRound)
		}
		// else {
		// We just need to keep waiting.
		// }
	}
}

// If we have the block AND +2/3 commits for it, finalize.
func (cs *ConsensusState) tryFinalizeCommit(height int64) {
	logger := cs.Logger.With("height", height)

	heightRound := cs.getRoundState(height)
	if heightRound == nil {
		return
	}

	blockID, ok := heightRound.Votes.Precommits(heightRound.Round).TwoThirdsMajority()
	if !ok || len(blockID.Hash) == 0 {
		logger.Error("Attempt to finalize failed. There was no +2/3 majority, or +2/3 was for <nil>.")
		return
	}
	if !heightRound.ProposalBlock.HashesTo(blockID.Hash) {
		// TODO: this happens every time if we're not a validator (ugly logs)
		// TODO: ^^ wait, why does it matter that we're a validator?
		logger.Info("Attempt to finalize failed. We don't have the commit block.", "proposal-block", heightRound.ProposalBlock.Hash(), "commit-block", blockID.Hash)
		return
	}

	//	go
	cs.finalizeCommit(height)
}

// Increment height and goto cstypes.RoundStepNewHeight
func (cs *ConsensusState) finalizeCommit(height int64) {
	cs.finalizeMtx.Lock()
	defer cs.finalizeMtx.Unlock()

	heightRound := cs.getRoundState(height)
	if heightRound == nil {
		return
	}

	if heightRound.Step != cstypes.RoundStepCommit {
		cs.Logger.Debug(fmt.Sprintf("finalizeCommit(%v): Invalid args. Current step: %v/%v/%v", height, heightRound.Height, heightRound.Round, heightRound.Step))
		return
	}

	blockID, ok := heightRound.Votes.Precommits(heightRound.Round).TwoThirdsMajority()
	block, blockParts := heightRound.ProposalBlock, heightRound.ProposalBlockParts

	if !ok {
		panic(fmt.Sprintf("Cannot finalizeCommit, commit does not have two thirds majority"))
	}
	if !blockParts.HasHeader(blockID.PartsHeader) {
		panic(fmt.Sprintf("Expected ProposalBlockParts header to be commit header"))
	}
	if !block.HashesTo(blockID.Hash) {
		panic(fmt.Sprintf("Cannot finalizeCommit, ProposalBlock does not hash to commit hash"))
	}

	//Wait finalize previous block
	for {
		got, now := height, cs.state.LastBlockHeight
		wanted := now + 1
		if got == wanted {
			break
		}

		cs.Logger.Debug("Previous block is not finalized yet", "Current Finalizing height", got, "Previous finalized height", now)
		atomic.StoreInt32(&cs.waitFinalize, 1)
		cs.waitFinalizeCond.Wait()
	}

	if err := cs.blockExec.ValidateBlock(cs.state, block); err != nil {
		switch err.(type) {
		case *sm.ErrLastBlockIDMismatch:
			cs.Logger.Info(fmt.Sprintf("finalizeCommit(%v): previous block is invalid. Unlocking. err=%v", height, err))
			heightRound.LockedRound = -1
			heightRound.LockedBlock = nil
			heightRound.LockedBlockParts = nil

			cs.eventBus.PublishEventUnlock(heightRound.RoundStateEvent())
			cs.enterNewRound(height, heightRound.Round+1)
			return

		default:
			panic(fmt.Sprintf("+2/3 committed an invalid block: %v", err))
		}
	}

	cs.Logger.Info(fmt.Sprintf("Finalizing commit of block with %d txs", block.NumTxs),
		"height", block.Height, "hash", block.Hash(), "root", block.AppHash)
	cs.Logger.Info(fmt.Sprintf("%v", block))

	if block.Height > 1 && !cs.config.SkipTimeoutCommit {
		if prevRs := cs.getRoundState(block.Height - 1); prevRs != nil {
			now := tmtime.Now()
			duration := cs.config.Commit(prevRs.CommitTime).Sub(now)
			time.Sleep(duration)
		}
	}

	fail.Fail() // XXX

	lenULB := cs.state.ConsensusParams.Block.LenULB

	// Save to blockStore.
	if cs.blockStore.Height() < block.Height {
		// NOTE: the seenCommit is local justification to commit this block,
		// but may differ from the LastCommit included in the next block
		precommits := heightRound.Votes.Precommits(heightRound.Round)
		seenCommit := precommits.MakeCommit()
		cs.blockStore.SaveBlock(block, blockParts, seenCommit, lenULB)
	} else {
		// Happens during replay if we already saved the block but didn't commit
		cs.Logger.Info("Calling finalizeCommit on already stored block", "height", block.Height)
	}

	fail.Fail() // XXX

	// Write EndHeightMessage{} for this height, implying that the blockstore
	// has saved the block.
	//
	// If we crash before writing this EndHeightMessage{}, we will recover by
	// running ApplyBlock during the ABCI handshake when we restart.  If we
	// didn't save the block to the blockstore before writing
	// EndHeightMessage{}, we'd have to change WAL replay -- currently it
	// complains about replaying for heights where an #ENDHEIGHT entry already
	// exists.
	//
	// Either way, the ConsensusState should not be resumed until we
	// successfully call ApplyBlock (ie. later here, or in Handshake after
	// restart).
	endMsg := EndHeightMessage{height}
	if err := cs.wal.WriteSync(endMsg); err != nil { // NOTE: fsync
		panic(fmt.Sprintf("Failed to write %v msg to consensus wal due to %v. Check your FS and restart the node", endMsg, err))
	}

	fail.Fail() // XXX

	// Create a copy of the state for staging and an event cache for txs.
	stateCopy := cs.state.Copy()

	// Execute and commit the block, update and save the state, and update the mempool.
	// NOTE The block.AppHash wont reflect these txs until the next block.
	var err error
	stateCopy, err = cs.blockExec.ApplyBlock(stateCopy, types.BlockID{Hash: block.Hash(), PartsHeader: blockParts.Header()}, block)
	if err != nil {
		cs.Logger.Error("Error on ApplyBlock. Did the application crash? Please restart tendermint", "err", err)
		err := cmn.Kill()
		if err != nil {
			cs.Logger.Error("Failed to kill this process - please do so manually", "err", err)
		}
		return
	}

	heightRound.CommitRound = heightRound.Round
	heightRound.CommitTime = tmtime.Now()

	fail.Fail() // XXX

	// must be called before we update state
	cs.recordMetrics(height, block)

	// NewHeightStep!
	cs.updateToState(stateCopy)

	fail.Fail() // XXX

	// clean up to ulb round state
	if height > lenULB {
		cs.cleanupFinalizedRoundState(height - lenULB)
	}

	// By here,
	// * cs.Height has been increment to height+1
	// * cs.Step is now cstypes.RoundStepNewHeight
	// * cs.StartTime is set to when we will start round0.

	if atomic.LoadInt32(&cs.waitFinalize) == 1 {
		cs.waitFinalizeCond.Broadcast()
		atomic.StoreInt32(&cs.waitFinalize, 0)
	}
}

func (cs *ConsensusState) recordMetrics(height int64, block *types.Block) {
	heightRound := cs.getRoundState(height)
	if heightRound == nil {
		panic("Must be just initialized height round")
	}

	cs.metrics.Validators.Set(float64(heightRound.Validators.Size()))
	cs.metrics.ValidatorsPower.Set(float64(heightRound.Validators.TotalVotingPower()))
	missingValidators := 0
	missingValidatorsPower := int64(0)
	for i, val := range heightRound.Validators.Validators {
		var vote *types.CommitSig
		if i < len(block.LastCommit.Precommits) {
			vote = block.LastCommit.Precommits[i]
		}
		if vote == nil {
			missingValidators++
			missingValidatorsPower += val.VotingPower
		}
	}
	cs.metrics.MissingValidators.Set(float64(missingValidators))
	cs.metrics.MissingValidatorsPower.Set(float64(missingValidatorsPower))
	cs.metrics.ByzantineValidators.Set(float64(len(block.Evidence.Evidence)))
	byzantineValidatorsPower := int64(0)
	for _, ev := range block.Evidence.Evidence {
		if _, val := heightRound.Validators.GetByAddress(ev.Address()); val != nil {
			byzantineValidatorsPower += val.VotingPower
		}
	}
	cs.metrics.ByzantineValidatorsPower.Set(float64(byzantineValidatorsPower))

	if height > 1 {
		lastBlockMeta := cs.blockStore.LoadBlockMeta(height - 1)
		cs.metrics.BlockIntervalSeconds.Set(
			block.Time.Sub(lastBlockMeta.Header.Time).Seconds(),
		)
	}

	cs.metrics.NumTxs.Set(float64(block.NumTxs))
	cs.metrics.BlockSizeBytes.Set(float64(block.Size()))
	cs.metrics.TotalTxs.Set(float64(block.TotalTxs))
	cs.metrics.CommittedHeight.Set(float64(block.Height))

}

//-----------------------------------------------------------------------------

func (cs *ConsensusState) defaultSetProposal(proposal *types.Proposal) error {
	heightRound := cs.getRoundState(proposal.Height)
	if heightRound == nil {
		return nil
	}
	heightRound.Lock()
	defer heightRound.Unlock()

	// Already have one
	// TODO: possibly catch double proposals
	if heightRound.Proposal != nil {
		return nil
	}

	// Does not apply
	if proposal.Height != heightRound.Height || proposal.Round != heightRound.Round {
		return nil
	}

	// Verify POLRound, which must be -1 or in range [0, proposal.Round).
	if proposal.POLRound < -1 ||
		(proposal.POLRound >= 0 && proposal.POLRound >= proposal.Round) {
		return ErrInvalidProposalPOLRound
	}

	// Verify signature
	if !heightRound.Validators.GetProposer().PubKey.VerifyBytes(proposal.SignBytes(cs.state.ChainID), proposal.Signature) {
		return ErrInvalidProposalSignature
	}

	heightRound.Proposal = proposal
	// We don't update cs.ProposalBlockParts if it is already set.
	// This happens if we're already in cstypes.RoundStepCommit or if there is a valid block in the current round.
	// TODO: We can check if Proposal is for a different block as this is a sign of misbehavior!
	if heightRound.ProposalBlockParts == nil {
		heightRound.ProposalBlockParts = types.NewPartSetFromHeader(proposal.BlockID.PartsHeader)
	}
	cs.Logger.Info("Received proposal", "proposal", proposal)
	return nil
}

// NOTE: block is not necessarily valid.
// Asynchronously triggers either enterPrevote (before we timeout of propose) or tryFinalizeCommit, once we have the full block.
func (cs *ConsensusState) addProposalBlockPart(msg *BlockPartMessage, peerID p2p.ID) (added bool, err error) {
	height, round, part := msg.Height, msg.Round, msg.Part

	heightRound := cs.getRoundState(height)
	if heightRound == nil {
		return
	}
	heightRound.Lock()
	defer heightRound.Unlock()

	// We're not expecting a block part.
	if heightRound.ProposalBlockParts == nil {
		// NOTE: this can happen when we've gone to a higher round and
		// then receive parts from the previous round - not necessarily a bad peer.
		cs.Logger.Info("Received a block part when we're not expecting any",
			"height", height, "round", round, "index", part.Index, "peer", peerID)
		return false, nil
	}

	added, err = heightRound.ProposalBlockParts.AddPart(part)
	if err != nil {
		return added, err
	}
	if added && heightRound.ProposalBlockParts.IsComplete() {
		// Added and completed!
		_, err = cdc.UnmarshalBinaryLengthPrefixedReader(
			heightRound.ProposalBlockParts.GetReader(),
			&heightRound.ProposalBlock,
			cs.state.ConsensusParams.Block.MaxBytes,
		)
		if err != nil {
			return added, err
		}
		// NOTE: it's possible to receive complete proposal blocks for future rounds without having the proposal
		cs.Logger.Info("Received complete proposal block", "height", heightRound.ProposalBlock.Height, "hash", heightRound.ProposalBlock.Hash())
		cs.eventBus.PublishEventCompleteProposal(heightRound.CompleteProposalEvent())

		cs.blockExec.ReserveBlock(cs.state, heightRound.ProposalBlock)

		cs.scheduleNewHeightRound0(height + 1)

		// Update Valid* if we can.
		prevotes := heightRound.Votes.Prevotes(heightRound.Round)
		blockID, hasTwoThirds := prevotes.TwoThirdsMajority()
		if hasTwoThirds && !blockID.IsZero() && (heightRound.ValidRound < heightRound.Round) {
			if heightRound.ProposalBlock.HashesTo(blockID.Hash) && cs.validatePreviousBlock(heightRound.ProposalBlock) == nil {
				cs.Logger.Info("Updating valid block to new proposal block",
					"valid-round", heightRound.Round, "valid-block-hash", heightRound.ProposalBlock.Hash())
				heightRound.ValidRound = heightRound.Round
				heightRound.ValidBlock = heightRound.ProposalBlock
				heightRound.ValidBlockParts = heightRound.ProposalBlockParts
			}
			// TODO: In case there is +2/3 majority in Prevotes set for some
			// block and cs.ProposalBlock contains different block, either
			// proposer is faulty or voting power of faulty processes is more
			// than 1/3. We should trigger in the future accountability
			// procedure at this point.
		}

		if heightRound.Step <= cstypes.RoundStepPropose && cs.isProposalComplete(height) {
			// Move onto the next step
			cs.enterPrevote(height, heightRound.Round)
			if hasTwoThirds { // this is optimisation as this will be triggered when prevote is added
				cs.enterPrecommit(height, heightRound.Round)
			}
		} else if heightRound.Step == cstypes.RoundStepCommit {
			// If we're waiting on the proposal block...
			cs.tryFinalizeCommit(height)
		}
		return added, nil
	}
	return added, nil
}

// Attempt to add the vote. if its a duplicate signature, dupeout the validator
func (cs *ConsensusState) tryAddVote(vote *types.Vote, peerID p2p.ID) (bool, error) {
	added, err := cs.addVote(vote, peerID)
	if err != nil {
		// If the vote height is off, we'll just ignore it,
		// But if it's a conflicting sig, add it to the cs.evpool.
		// If it's otherwise invalid, punish peer.
		if err == ErrVoteHeightMismatch {
			return added, err
		} else if voteErr, ok := err.(*types.ErrVoteConflictingVotes); ok {
			addr := cs.privValidator.GetPubKey().Address()
			if bytes.Equal(vote.ValidatorAddress, addr) {
				cs.Logger.Error("Found conflicting vote from ourselves. Did you unsafe_reset a validator?", "height", vote.Height, "round", vote.Round, "type", vote.Type)
				return added, err
			}
			cs.evpool.AddEvidence(voteErr.DuplicateVoteEvidence)
			return added, err
		} else {
			// Either
			// 1) bad peer OR
			// 2) not a bad peer? this can also err sometimes with "Unexpected step" OR
			// 3) tmkms use with multiple validators connecting to a single tmkms instance (https://github.com/tendermint/tendermint/issues/3839).
			cs.Logger.Info("Error attempting to add vote", "err", err)
			return added, ErrAddingVote
		}
	}
	return added, nil
}

//-----------------------------------------------------------------------------

func (cs *ConsensusState) addVote(vote *types.Vote, peerID p2p.ID) (added bool, err error) {
	cs.Logger.Debug("addVote", "voteHeight", vote.Height, "voteType", vote.Type, "valIndex", vote.ValidatorIndex)

	height := vote.Height
	heightRound := cs.getRoundState(height)
	if heightRound == nil {
		return false, nil
	}
	heightRound.Lock()
	defer heightRound.Unlock()

	added, err = heightRound.Votes.AddVote(vote, peerID)
	if !added {
		// Either duplicate, or error upon cs.roundState.Votes.AddByIndex()
		return
	}

	cs.eventBus.PublishEventVote(types.EventDataVote{Vote: vote})
	cs.evsw.FireEvent(types.EventVote, vote)

	switch vote.Type {
	case types.PrevoteType:
		prevotes := heightRound.Votes.Prevotes(vote.Round)
		cs.Logger.Info("Added to prevote", "vote", vote, "prevotes", prevotes.StringShort())

		// If +2/3 prevotes for a block or nil for *any* round:
		if blockID, ok := prevotes.TwoThirdsMajority(); ok {

			// There was a polka!
			// If we're locked but this is a recent polka, unlock.
			// If it matches our ProposalBlock, update the ValidBlock

			// Unlock if `cs.roundState.LockedRound < vote.Round <= cs.roundState.Round`
			// NOTE: If vote.Round > cs.roundState.Round, we'll deal with it when we get to vote.Round
			if (heightRound.LockedBlock != nil) &&
				(heightRound.LockedRound < vote.Round) &&
				(vote.Round <= heightRound.Round) &&
				!heightRound.LockedBlock.HashesTo(blockID.Hash) {

				cs.Logger.Info("Unlocking because of POL.", "lockedRound", heightRound.LockedRound, "POLRound", vote.Round)
				heightRound.LockedRound = -1
				heightRound.LockedBlock = nil
				heightRound.LockedBlockParts = nil
				cs.eventBus.PublishEventUnlock(heightRound.RoundStateEvent())
			}

			// Update Valid* if we can.
			// NOTE: our proposal block may be nil or not what received a polka..
			if len(blockID.Hash) != 0 && (heightRound.ValidRound < vote.Round) && (vote.Round == heightRound.Round) {

				if heightRound.ProposalBlock.HashesTo(blockID.Hash) {
					cs.Logger.Info(
						"Updating ValidBlock because of POL.", "validRound", heightRound.ValidRound, "POLRound", vote.Round)
					heightRound.ValidRound = vote.Round
					heightRound.ValidBlock = heightRound.ProposalBlock
					heightRound.ValidBlockParts = heightRound.ProposalBlockParts
				} else {
					cs.Logger.Info(
						"Valid block we don't know about. Set ProposalBlock=nil",
						"proposal", heightRound.ProposalBlock.Hash(), "blockId", blockID.Hash)
					// We're getting the wrong block.
					cs.blockExec.UnreserveBlock(cs.state, heightRound.ProposalBlock)
					heightRound.ProposalBlock = nil
				}
				if !heightRound.ProposalBlockParts.HasHeader(blockID.PartsHeader) {
					heightRound.ProposalBlockParts = types.NewPartSetFromHeader(blockID.PartsHeader)
				}
				cs.evsw.FireEvent(types.EventValidBlock, heightRound)
				cs.eventBus.PublishEventValidBlock(heightRound.RoundStateEvent())
			}
		}

		// If +2/3 prevotes for *anything* for future round:
		if heightRound.Round < vote.Round && prevotes.HasTwoThirdsAny() {
			// Round-skip if there is any 2/3+ of votes ahead of us
			cs.enterNewRound(height, vote.Round)
		} else if heightRound.Round == vote.Round && cstypes.RoundStepPrevote <= heightRound.Step { // current round
			blockID, ok := prevotes.TwoThirdsMajority()
			if ok && (cs.isProposalComplete(height) || len(blockID.Hash) == 0) {
				cs.enterPrecommit(height, vote.Round)
			} else if prevotes.HasTwoThirdsAny() {
				cs.enterPrevoteWait(height, vote.Round)
			}
		} else if heightRound.Proposal != nil && 0 <= heightRound.Proposal.POLRound && heightRound.Proposal.POLRound == vote.Round {
			// If the proposal is now complete, enter prevote of cs.roundState.Round.
			if cs.isProposalComplete(height) {
				cs.enterPrevote(height, heightRound.Round)
			}
		}

	case types.PrecommitType:
		precommits := heightRound.Votes.Precommits(vote.Round)
		cs.Logger.Info("Added to precommit", "vote", vote, "precommits", precommits.StringShort())

		blockID, ok := precommits.TwoThirdsMajority()
		if ok {
			// Executed as TwoThirdsMajority could be from a higher round
			cs.enterNewRound(height, vote.Round)
			cs.enterPrecommit(height, vote.Round)
			if len(blockID.Hash) != 0 {
				cs.enterCommit(height, vote.Round)
				if cs.config.SkipTimeoutCommit && precommits.HasAll() {
					cs.enterNewRound(heightRound.Height+1, 0)
				}
			} else {
				cs.enterPrecommitWait(height, vote.Round)
			}
		} else if heightRound.Round <= vote.Round && precommits.HasTwoThirdsAny() {
			cs.enterNewRound(height, vote.Round)
			cs.enterPrecommitWait(height, vote.Round)
		}

	default:
		panic(fmt.Sprintf("Unexpected vote type %X", vote.Type)) // go-wire should prevent this.
	}

	return
}

func (cs *ConsensusState) signVote(height int64, type_ types.SignedMsgType, hash []byte, header types.PartSetHeader) (*types.Vote, error) {
	// Flush the WAL. Otherwise, we may not recompute the same vote to sign, and the privValidator will refuse to sign anything.
	cs.wal.FlushAndSync()
	heightRound := cs.getRoundState(height)
	if heightRound == nil {
		panic("Must be just initialized height round")
	}

	addr := cs.privValidator.GetPubKey().Address()
	valIndex, _ := heightRound.Validators.GetByAddress(addr)

	vote := &types.Vote{
		ValidatorAddress: addr,
		ValidatorIndex:   valIndex,
		Height:           heightRound.Height,
		Round:            heightRound.Round,
		Timestamp:        cs.voteTime(height),
		Type:             type_,
		BlockID:          types.BlockID{Hash: hash, PartsHeader: header},
	}
	err := cs.privValidator.SignVote(cs.state.ChainID, vote)
	return vote, err
}

func (cs *ConsensusState) voteTime(height int64) time.Time {
	heightRound := cs.getRoundState(height)
	if heightRound == nil {
		panic("Must be just initialized height round")
	}

	now := tmtime.Now()
	minVoteTime := now
	// TODO: We should remove next line in case we don't vote for v in case cs.ProposalBlock == nil,
	// even if cs.LockedBlock != nil. See https://github.com/tendermint/spec.
	timeIotaMs := time.Duration(cs.state.ConsensusParams.Block.TimeIotaMs) * time.Millisecond
	if heightRound.LockedBlock != nil {
		// See the BFT time spec https://tendermint.com/docs/spec/consensus/bft-time.html
		minVoteTime = heightRound.LockedBlock.Time.Add(timeIotaMs)
	} else if heightRound.ProposalBlock != nil {
		minVoteTime = heightRound.ProposalBlock.Time.Add(timeIotaMs)
	}

	if now.After(minVoteTime) {
		return now
	}
	return minVoteTime
}

// sign the vote and publish on internalMsgQueue
func (cs *ConsensusState) signAddVote(height int64, type_ types.SignedMsgType, hash []byte, header types.PartSetHeader) *types.Vote {
	heightRound := cs.getRoundState(height)
	if heightRound == nil {
		panic("Must be just initialized height round")
	}

	// if we don't have a key or we're not in the validator set, do nothing
	if cs.privValidator == nil || !heightRound.Validators.HasAddress(cs.privValidator.GetPubKey().Address()) {
		return nil
	}
	vote, err := cs.signVote(height, type_, hash, header)
	if err == nil {
		cs.sendInternalMessage(msgInfo{&VoteMessage{vote}, ""})
		cs.Logger.Info("Signed and pushed vote", "height", heightRound.Height, "round", heightRound.Round, "vote", vote, "err", err)
		return vote
	}
	if !cs.replayMode {
		cs.Logger.Error("Error signing vote", "height", heightRound.Height, "round", heightRound.Round, "vote", vote, "err", err)
	}
	return nil
}

//---------------------------------------------------------

func CompareRS(r1 int, s1 cstypes.RoundStepType, r2 int, s2 cstypes.RoundStepType) int {
	if r1 < r2 {
		return -1
	} else if r1 > r2 {
		return 1
	}
	if s1 < s2 {
		return -1
	} else if s1 > s2 {
		return 1
	}
	return 0
}

func CompareHRS(h1 int64, r1 int, s1 cstypes.RoundStepType, h2 int64, r2 int, s2 cstypes.RoundStepType) int {
	if h1 < h2 {
		return -1
	} else if h1 > h2 {
		return 1
	}
	if r1 < r2 {
		return -1
	} else if r1 > r2 {
		return 1
	}
	if s1 < s2 {
		return -1
	} else if s1 > s2 {
		return 1
	}
	return 0
}
