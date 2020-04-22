package v0

import (
	"time"

	cmn "github.com/hdac-io/tendermint/libs/common"
	"github.com/hdac-io/tendermint/p2p"
	"github.com/hdac-io/tendermint/types"
)

type ULBLengthHandler func() int64

type FridayBlockPool struct {
	BlockPool
	ulbHandler ULBLengthHandler
}

// NewBlockPool returns a new BlockPool with the height equal to start. Block
// requests and errors will be sent to requestsCh and errorsCh accordingly.
func NewFridayBlockPool(start int64, requestsCh chan<- BlockRequest, errorsCh chan<- peerError, ulbHandler ULBLengthHandler) *FridayBlockPool {
	bp := &FridayBlockPool{
		BlockPool: BlockPool{
			peers: make(map[p2p.ID]*bpPeer),

			requesters: make(map[int64]*bpRequester),
			height:     start,
			numPending: 0,

			requestsCh: requestsCh,
			errorsCh:   errorsCh,
		},
		ulbHandler: ulbHandler,
	}
	bp.BaseService = *cmn.NewBaseService(nil, "BlockPool", bp)
	return bp
}

// IsCaughtUp returns true if this node is caught up, false - otherwise.
// TODO: relax conditions, prevent abuse.
// NOTE: fix check pool height condition for ulb
func (pool *FridayBlockPool) IsCaughtUp() bool {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()

	// Need at least 1 peer to be considered caught up.
	if len(pool.peers) == 0 {
		pool.Logger.Debug("Blockpool has no peers")
		return false
	}

	// Some conditions to determine if we're caught up.
	// Ensures we've either received a block or waited some amount of time,
	// and that we're synced to the highest known height.
	// Note we use maxPeerHeight - ulb because to sync block H requires block H+ulb
	// to verify the LastCommit.
	receivedBlockOrTimedOut := pool.height > 0 || time.Since(pool.startTime) > 5*time.Second
	ourChainIsLongestAmongPeers := pool.maxPeerHeight == 0 || pool.height >= (pool.maxPeerHeight-pool.ulbHandler())
	isCaughtUp := receivedBlockOrTimedOut && ourChainIsLongestAmongPeers
	return isCaughtUp
}

// PeekTwoBlocks returns blocks at pool.height and pool.height+ULBLength.
// We need to see the second block's Commit to validate the first block.
// So we peek two blocks at a time.
// The caller will verify the commit.
func (pool *FridayBlockPool) PeekTwoBlocks() (first *types.Block, second *types.Block) {
	pool.mtx.Lock()
	defer pool.mtx.Unlock()

	ulbLength := pool.ulbHandler()
	if ulbLength <= 0 {
		panic("returned invalid ulb length")
	}

	if r := pool.requesters[pool.height]; r != nil {
		first = r.getBlock()
	}
	if r := pool.requesters[pool.height+ulbLength]; r != nil {
		second = r.getBlock()
	}
	return
}
