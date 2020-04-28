package friday

import (
	amino "github.com/tendermint/go-amino"
	"github.com/hdac-io/tendermint/types"
)

var cdc = amino.NewCodec()

func init() {
	RegisterConsensusMessages(cdc)
	RegisterWALMessages(cdc)
	types.RegisterBlockAmino(cdc)
}
