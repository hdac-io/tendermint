package bls

import (
	"sync"
	"testing"
	kilicBls "github.com/kilic/bls12-381"
)

func TestStressLargetSet(t *testing.T) {

	var privs []PrivKeyBls
	var pubs []PubKeyBls

	testKeyCount := 100

	//Initialzie large set keys
	for i := 0; i < testKeyCount; i++ {
		priv, pub := GenerateKey()
		privs = append(privs, priv)
		pubs = append(pubs, pub)
	}
	wg := sync.WaitGroup{}

	for i := 0; i < testKeyCount-1; i++ {
		for j := i + 1; j < testKeyCount-1; j++ {
			wg.Add(1)
			testMsg := []byte{0x4d, 0x5a, 0x00, 0x90}
			sig, err := privs[i].Sign(testMsg)
			go func(x, y int, sig []byte) {
				if err != nil {
					panic("T1")
				}
				if pubs[x].VerifyBytes(testMsg, sig) != true {
					panic("T2")
				}
				if pubs[y].VerifyBytes(testMsg, sig) == true {
					panic("T3")
				}

				wg.Done()
				// assert.NoError(t, err)
				// assert.True(t, pubs[x].VerifyBytes(testMsg, sig))
				// assert.False(t, pubs[y].VerifyBytes(testMsg, sig))
			}(i, j, sig)
		}
	}
	wg.Wait()
}

func TestSameOutput(t *testing.T) {
	kilicBls.blssig.
}
