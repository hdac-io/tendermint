package commands

import (
	"fmt"

	"github.com/spf13/cobra"
	cfg "github.com/hdac-io/tendermint/config"
	cmn "github.com/hdac-io/tendermint/libs/common"
	"github.com/hdac-io/tendermint/p2p"
	"github.com/hdac-io/tendermint/privval"
	"github.com/hdac-io/tendermint/types"
	tmtime "github.com/hdac-io/tendermint/types/time"
)

// InitFilesCmd initialises a fresh Tendermint Core instance.
var InitFilesCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize Tendermint",
	RunE:  initFiles,
}

func initFiles(cmd *cobra.Command, args []string) error {
	return initFilesWithConfig(config)
}

func initFilesWithConfig(config *cfg.Config) error {
	// private validator
	privValKeyFile := config.PrivValidatorKeyFile()
	privValStateFile := config.PrivValidatorStateFile()
	var pv types.PrivValidator
	if cmn.FileExists(privValKeyFile) {
		switch config.Consensus.Version {
		case "tendermint":
			pv = privval.LoadFilePV(privValKeyFile, privValStateFile)
		case "friday":
			pv = privval.LoadFridayFilePV(privValKeyFile, privValStateFile)
		default:
			return fmt.Errorf("invalid consensus version %s", config.Consensus.Version)
		}
		logger.Info("Found private validator", "keyFile", privValKeyFile,
			"stateFile", privValStateFile)
	} else {
		switch config.Consensus.Version {
		case "tendermint":
			fpv := privval.GenFilePV(privValKeyFile, privValStateFile)
			fpv.Save()
			pv = fpv
		case "friday":
			ffpv := privval.GenFridayFilePV(privValKeyFile, privValStateFile)
			ffpv.Save()
			pv = ffpv
		default:
			return fmt.Errorf("invalid consensus version %s", config.Consensus.Version)
		}
		logger.Info("Generated private validator", "keyFile", privValKeyFile,
			"stateFile", privValStateFile)
	}

	nodeKeyFile := config.NodeKeyFile()
	if cmn.FileExists(nodeKeyFile) {
		logger.Info("Found node key", "path", nodeKeyFile)
	} else {
		if _, err := p2p.LoadOrGenNodeKey(nodeKeyFile); err != nil {
			return err
		}
		logger.Info("Generated node key", "path", nodeKeyFile)
	}

	// genesis file
	genFile := config.GenesisFile()
	if cmn.FileExists(genFile) {
		logger.Info("Found genesis file", "path", genFile)
	} else {
		genDoc := types.GenesisDoc{
			ChainID:         fmt.Sprintf("test-chain-%v", cmn.RandStr(6)),
			GenesisTime:     tmtime.Now(),
			ConsensusParams: types.DefaultFridayConsensusParams(),
		}
		key := pv.GetPubKey()
		genDoc.Validators = []types.GenesisValidator{{
			Address: key.Address(),
			PubKey:  key,
			Power:   10,
		}}

		if err := genDoc.SaveAs(genFile); err != nil {
			return err
		}
		logger.Info("Generated genesis file", "path", genFile)
	}

	return nil
}
