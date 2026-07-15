package main

import (
	"context"
	"time"

	extint "github.com/digital-dream-labs/vector-cloud/internal/proto/external_interface"

	"github.com/digital-dream-labs/vector-cloud/internal/log"
)

// PlayPhotoShutter plays anim_photo_shutter_01 (WirePod photo-click face).
//
// Xiaozhi listen/TTS owns the face without SDK control, so a bare PlayAnimation
// is usually ignored. Briefly take OVERRIDE_BEHAVIORS control, play the shutter
// anim, wait for it to show, then release — no eye-color / OLED white hacks.
func PlayPhotoShutter(ctx context.Context) error {
	_ = ctx

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
		log.Println("[Xiaozhi] analyze_photo shutter control:", err)
		return err
	}
	defer release()

	// Small settle so engine grants control before the anim request.
	time.Sleep(80 * time.Millisecond)

	svc := &rpcService{}
	cctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := svc.PlayAnimation(cctx, &extint.PlayAnimationRequest{
			Animation: &extint.Animation{Name: "anim_photo_shutter_01"},
			Loops:     1,
		})
		done <- err
	}()

	// anim_photo_shutter_01 is short — hold long enough for eyes/click to show,
	// but do not block capture on a hung PlayAnimationResponse forever.
	select {
	case err := <-done:
		if err != nil {
			log.Println("[Xiaozhi] analyze_photo shutter anim:", err)
		} else {
			log.Println("[Xiaozhi] analyze_photo shutter anim OK")
		}
	case <-time.After(700 * time.Millisecond):
		log.Println("[Xiaozhi] analyze_photo shutter anim playing (no wait on response)")
	}

	return nil
}
