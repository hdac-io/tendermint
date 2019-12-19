package types

const (
	PubKeyEd25519   = "ed25519"
	PubKeyBls12_381 = "bls12-381"
)

func Ed25519ValidatorUpdate(pubkey []byte, power int64) ValidatorUpdate {
	return ValidatorUpdate{
		// Address:
		PubKey: PubKey{
			Type: PubKeyEd25519,
			Data: pubkey,
		},
		Power: power,
	}
}

func BlsValidatorUpdate(pubkey []byte, power int64) ValidatorUpdate {
	return ValidatorUpdate{
		// Address:
		PubKey: PubKey{
			Type: PubKeyBls12_381,
			Data: pubkey,
		},
		Power: power,
	}
}
