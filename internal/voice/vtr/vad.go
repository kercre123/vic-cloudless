package vtr

import (
	"fmt"
	"log"

	"github.com/maxhawkins/go-webrtcvad"
)

var InactiveFrames int
var ActiveFrames int
var OverallFrames int
var VADInst *webrtcvad.VAD
var VADExists bool

func SplitVAD(buf []byte) [][]byte {
	var chunk [][]byte
	for len(buf) >= 320 {
		chunk = append(chunk, buf[:320])
		buf = buf[320:]
	}
	return chunk
}

func DetectEndOfSpeech(chunk []byte) (stop bool, isActive bool) {
	if !VADExists {
		var err error
		VADInst, err = webrtcvad.New()
		if err != nil {
			log.Fatal(err)
		}
		VADInst.SetMode(1)
		VADExists = true
	}
	inactiveNumMax := 10
	for _, chunk := range SplitVAD(chunk) {
		active, err := VADInst.Process(16000, chunk)
		OverallFrames++
		if err != nil {
			fmt.Println("VAD err:")
			fmt.Println(err)
			InactiveFrames = 0
			ActiveFrames = 0
			OverallFrames = 0
			VADExists = false
			VADInst = nil
			return true, false
		}
		if active {
			ActiveFrames = ActiveFrames + 1
			InactiveFrames = 0
		} else {
			InactiveFrames = InactiveFrames + 1
		}
		if InactiveFrames >= inactiveNumMax && ActiveFrames > 18 {
			fmt.Println("End of speech detected.")
			VADExists = false
			VADInst = nil
			InactiveFrames = 0
			ActiveFrames = 0
			OverallFrames = 0
			return true, true
		}
	}
	if ActiveFrames < 5 {
		return false, false
	}
	return false, true
}
