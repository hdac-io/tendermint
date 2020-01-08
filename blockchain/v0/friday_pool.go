package v0

import (
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
