package core

import (
	"github.com/hdac-io/tendermint/evidence"
	ctypes "github.com/hdac-io/tendermint/rpc/core/types"
	rpctypes "github.com/hdac-io/tendermint/rpc/lib/types"
	"github.com/hdac-io/tendermint/types"
)

// BroadcastEvidence broadcasts evidence of the misbehavior.
// More: https://docs.tendermint.com/master/rpc/#/Info/broadcast_evidence
func BroadcastEvidence(ctx *rpctypes.Context, ev types.Evidence) (*ctypes.ResultBroadcastEvidence, error) {
	err := evidencePool.AddEvidence(ev)
	if _, ok := err.(evidence.ErrEvidenceAlreadyStored); err == nil || ok {
		return &ctypes.ResultBroadcastEvidence{Hash: ev.Hash()}, nil
	}
	return nil, err
}
