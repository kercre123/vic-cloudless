package stream

import (
	"strconv"

	chippergrpc2 "github.com/digital-dream-labs/api/go/chipperpb"
	"github.com/digital-dream-labs/vector-cloud/internal/voice/vtr"
)

// it works well at 533MHz, but transcription is instant at 730
var doFreqStuff bool = true

// WIRE: main entrypoint for a request!
// we are keeping the OG code commented in case we want to make some sort of hybrid solution

func (strm *Streamer) init(streamSize int) {
	// set up error response if context times out/is canceled
	go strm.cancelResponse()

	// // start routine to buffer communication between main routine and upload routine
	go strm.bufferRoutine(streamSize)
	// if strm.opts.checkOpts != nil {
	// 	go strm.testRoutine(streamSize)
	// }

	// // connect to server
	// var err *CloudError
	// if strm.conn, err = strm.opts.connectFn(strm.ctx); err != nil {
	// 	strm.receiver.OnError(err.Kind, err.Err)
	// 	strm.cancel()
	// 	return
	// }

	// start routine to upload audio via GRPC until response or error
	// go func() {
	// 	responseInited := false
	// 	for data := range strm.audioStream {
	// 		if err := strm.sendAudio(data); err != nil {
	// 			return
	// 		}
	// 		if !responseInited {
	// 			go strm.responseRoutine()
	// 			responseInited = true
	// 		}
	// 	}
	// }()

	go func() {
		var curFreq string
		var underClockAfter bool
		if doFreqStuff {
			curFreq = vtr.GetFreq()
			o, err := strconv.Atoi(curFreq)
			if err == nil {
				if o < 1267200 {
					underClockAfter = true
					go vtr.SetFreq("1267200", "800000")
				}
			}
		}
		for data := range strm.audioStream {
			text := vtr.Process(data)
			if text != "" {
				intent, iParam, _ := vtr.ProcessTextAll(text, vtr.IntentList)
				sendIntentGraphResponse(&chippergrpc2.IntentGraphResponse{
					ResponseType: chippergrpc2.IntentGraphMode_INTENT,
					IsFinal:      true,
					IntentResult: &chippergrpc2.IntentResult{
						Action:     intent,
						Parameters: iParam,
					},
				}, strm.receiver)
				if doFreqStuff {
					if underClockAfter {
						vtr.SetFreq(curFreq, "400000")
					}
				}
				return
			}
		}
	}()

}
