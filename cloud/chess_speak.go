package main

import (
	"context"
	"time"

	extint "github.com/digital-dream-labs/vector-cloud/internal/proto/external_interface"

	"github.com/digital-dream-labs/vector-cloud/internal/log"
	"github.com/digital-dream-labs/vector-cloud/internal/xiaozhi"
)

// SpeakTextEngine says text via engine SayText (Acapela), photo-shutter style control.
// Used for chess move auto-comments — no vic-gateway :443 required.
func SpeakTextEngine(text string) error {
	release := func() {
		_, _, _ = engineProtoManager.Write(&extint.GatewayWrapper{
			OneofMessageType: &extint.GatewayWrapper_ControlRelease{
				ControlRelease: &extint.ControlRelease{},
			},
		})
	}

	_, _, err := engineProtoManager.Write(&extint.GatewayWrapper{
		OneofMessageType: &extint.GatewayWrapper_ControlRequest{
			ControlRequest: &extint.ControlRequest{
				Priority: extint.ControlRequest_OVERRIDE_BEHAVIORS,
			},
		},
	})
	if err != nil {
		log.Println("[Chess] SayText control:", err)
		return err
	}
	defer release()

	// Settle so engine grants OVERRIDE before SayText (same idea as photo shutter).
	time.Sleep(200 * time.Millisecond)

	svc := &rpcService{}
	cctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	est := time.Duration(len([]rune(text))*100) * time.Millisecond
	if est < 2500*time.Millisecond {
		est = 2500 * time.Millisecond
	}
	if est > 12*time.Second {
		est = 12 * time.Second
	}

	done := make(chan error, 1)
	go func() {
		_, err := svc.SayText(cctx, &extint.SayTextRequest{
			Text:           text,
			UseVectorVoice: true,
			DurationScalar: 1.0,
		})
		done <- err
	}()

	log.Printf("[Chess] SayText playing (~%v): %q", est.Round(time.Millisecond), text)
	select {
	case err := <-done:
		if err != nil {
			log.Println("[Chess] SayText:", err)
			// Still hold for a bit — some failures are response-path only.
			time.Sleep(est)
			return err
		}
		log.Println("[Chess] SayText FINISHED")
		return nil
	case <-time.After(est):
		log.Println("[Chess] SayText held for estimate (no FINISHED yet)")
		return nil
	}
}

func initChessSpeak() {
	xiaozhi.SetSpeakText(SpeakTextEngine)
}
