package v1

import (
	"github.com/hdac-io/tendermint/p2p"
)


type ULBLengthHandler func() int64

type FridayBlockPool struct {
	BlockPool
	ulbHandler ULBLengthHandler
}

// NewFridayBlockPool creates a new specialized FridayBlockPool.
func NewFridayBlockPool(height int64, toBcR bcReactor, ulbHandler ULBLengthHandler) *FridayBlockPool {
	return &FridayBlockPool{
		BlockPool : BlockPool{
			Height:            height,
			MaxPeerHeight:     0,
			peers:             make(map[p2p.ID]*BpPeer),
			blocks:            make(map[int64]p2p.ID),
			plannedRequests:   make(map[int64]struct{}),
			nextRequestHeight: height,
			toBcR:             toBcR,	
		},
		ulbHandler : ulbHandler,
	}
}

// FirstTwoBlocksAndPeers returns the blocks and the delivery peers at pool's height H and H+lenULB.
func (pool *FridayBlockPool) FirstTwoBlocksAndPeers() (first, second *BlockData, err error) {
	ulbLength := pool.ulbHandler()
	if ulbLength < 0 {
		panic("returned invalid ulb length")
	}

	first, err = pool.BlockAndPeerAtHeight(pool.Height)
	second, err2 := pool.BlockAndPeerAtHeight(pool.Height + ulbLength)
	if err == nil {
		err = err2
	}
	return
}


// InvalidateFirstTwoBlocks removes the peers that sent us the first two blocks, blocks are removed by RemovePeer().
func (pool *FridayBlockPool) InvalidateFirstTwoBlocks(err error) {
	ulbLength := pool.ulbHandler()
	if ulbLength < 0 {
		panic("returned invalid ulb length")
	}

	first, err1 := pool.BlockAndPeerAtHeight(pool.Height)
	second, err2 := pool.BlockAndPeerAtHeight(pool.Height + ulbLength)

	if err1 == nil {
		pool.RemovePeer(first.peer.ID, err)
	}
	if err2 == nil {
		pool.RemovePeer(second.peer.ID, err)
	}
}

