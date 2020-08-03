package abcicli

import (
	"sync"

	"github.com/hdac-io/tendermint/abci/types"
	cmn "github.com/hdac-io/tendermint/libs/common"
)

type fridayLocalClient struct {
	localClient
}

func NewFridayLocalClient(mtx *sync.Mutex, app types.Application) *fridayLocalClient {
	if mtx == nil {
		mtx = new(sync.Mutex)
	}
	cli := &fridayLocalClient{
		localClient: localClient{
			mtx:         mtx,
			Application: app,
		},
	}
	cli.BaseService = *cmn.NewBaseService(nil, "fridayLocalClient", cli)
	return cli
}

func (app *fridayLocalClient) DeliverTxAsync(params types.RequestDeliverTx) *ReqRes {
	println("I'm tendermint DeliverTxAsync! for friday!", params.Index)
	go func() {
		res := app.Application.DeliverTx(params)
		app.callback(
			types.ToRequestDeliverTx(params),
			types.ToResponseDeliverTx(res),
		)
	}()

	return nil
}
