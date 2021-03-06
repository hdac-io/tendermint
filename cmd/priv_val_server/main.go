package main

import (
	"flag"
	"os"
	"time"

	"github.com/hdac-io/tendermint/crypto/bls"
	cmn "github.com/hdac-io/tendermint/libs/common"
	"github.com/hdac-io/tendermint/libs/log"
	"github.com/hdac-io/tendermint/types"

	"github.com/hdac-io/tendermint/privval"
)

func main() {
	var (
		addr             = flag.String("addr", ":26659", "Address of client to connect to")
		chainID          = flag.String("chain-id", "mychain", "chain id")
		privValKeyPath   = flag.String("priv-key", "", "priv val key file path")
		privValStatePath = flag.String("priv-state", "", "priv val state file path")
		isFridayPV       = flag.Bool("friday", false, "run for friday")

		logger = log.NewTMLogger(
			log.NewSyncWriter(os.Stdout),
		).With("module", "priv_val")
	)
	flag.Parse()

	logger.Info(
		"Starting private validator",
		"addr", *addr,
		"chainID", *chainID,
		"privKeyPath", *privValKeyPath,
		"privStatePath", *privValStatePath,
	)

	var pv types.PrivValidator
	if *isFridayPV {
		pv = privval.LoadFridayFilePV(*privValKeyPath, *privValStatePath)
	} else {
		pv = privval.LoadFilePV(*privValKeyPath, *privValStatePath)
	}

	var dialer privval.SocketDialer
	protocol, address := cmn.ProtocolAndAddress(*addr)
	switch protocol {
	case "unix":
		dialer = privval.DialUnixFn(address)
	case "tcp":
		connTimeout := 3 * time.Second // TODO
		dialer = privval.DialTCPFn(address, connTimeout, bls.GenPrivKey())
	default:
		logger.Error("Unknown protocol", "protocol", protocol)
		os.Exit(1)
	}

	sd := privval.NewSignerDialerEndpoint(logger, dialer)
	ss := privval.NewSignerServer(sd, *chainID, pv)

	err := ss.Start()
	if err != nil {
		panic(err)
	}

	// Stop upon receiving SIGTERM or CTRL-C.
	cmn.TrapSignal(logger, func() {
		err := ss.Stop()
		if err != nil {
			panic(err)
		}
	})

	// Run forever.
	select {}
}
