//go:build !vicos

package xiaozhi

import "fmt"

const (
	EncSampleRate   = 16000
	EncFrameMs      = 60
	EncFrameSamples = EncSampleRate * EncFrameMs / 1000 // 960
	DecSampleRate   = 24000
	DecFrameSamples = DecSampleRate * EncFrameMs / 1000 // 1440
)

// Encoder is a stub (opus not available in non-vicos builds).
type Encoder struct{}

func NewEncoder() (*Encoder, error) {
	return nil, fmt.Errorf("opus not available in this build (requires vicos tag)")
}

func (e *Encoder) EncodeFrame(pcmBytes []byte) ([]byte, error) {
	return nil, fmt.Errorf("opus not available")
}

// Decoder is a stub.
type Decoder struct{}

func NewDecoder() (*Decoder, error) {
	return nil, fmt.Errorf("opus not available in this build (requires vicos tag)")
}

func (d *Decoder) DecodeFrame(opusData []byte) ([]byte, error) {
	return nil, fmt.Errorf("opus not available")
}

func Downsample24kTo16k(input []byte) []byte { return input }

func ChunkPCM(pcm []byte) [][]byte {
	const chunkSize = 1024
	var chunks [][]byte
	for len(pcm) >= chunkSize {
		chunks = append(chunks, pcm[:chunkSize])
		pcm = pcm[chunkSize:]
	}
	if len(pcm) > 0 {
		pad := make([]byte, chunkSize)
		copy(pad, pcm)
		chunks = append(chunks, pad)
	}
	return chunks
}

func ApplyGain(pcm []byte, gain float64) []byte { return pcm }
