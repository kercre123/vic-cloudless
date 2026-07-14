//go:build vicos

package xiaozhi

import (
	"encoding/binary"
	"fmt"
	"math"

	"gopkg.in/hraban/opus.v2"
)

const (
	// EncSampleRate is the input PCM rate from the mic (16kHz).
	EncSampleRate = 16000
	// EncFrameMs is the Opus frame duration in milliseconds.
	EncFrameMs = 60
	// EncFrameSamples is samples per Opus frame at 16kHz.
	EncFrameSamples = EncSampleRate * EncFrameMs / 1000 // 960

	// DecSampleRate is the server TTS output sample rate (24kHz).
	DecSampleRate = 24000
	// DecFrameSamples is the max samples per Opus frame at 24kHz (60ms).
	DecFrameSamples = DecSampleRate * EncFrameMs / 1000 // 1440
)

// Encoder wraps an Opus encoder for mic PCM → Opus.
type Encoder struct {
	enc *opus.Encoder
}

// NewEncoder creates a 16kHz mono Opus encoder for uplink.
func NewEncoder() (*Encoder, error) {
	enc, err := opus.NewEncoder(EncSampleRate, 1, opus.AppVoIP)
	if err != nil {
		return nil, fmt.Errorf("opus encoder: %w", err)
	}
	enc.SetBitrate(24000)
	return &Encoder{enc: enc}, nil
}

// EncodeFrame encodes exactly EncFrameSamples PCM bytes (s16le) to an Opus packet.
// Input must be exactly EncFrameSamples*2 bytes.
func (e *Encoder) EncodeFrame(pcmBytes []byte) ([]byte, error) {
	if len(pcmBytes) != EncFrameSamples*2 {
		return nil, fmt.Errorf("expected %d bytes, got %d", EncFrameSamples*2, len(pcmBytes))
	}
	samples := make([]int16, EncFrameSamples)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(pcmBytes[i*2:]))
	}
	out := make([]byte, 4096)
	n, err := e.enc.Encode(samples, out)
	if err != nil {
		return nil, err
	}
	return out[:n], nil
}

// Decoder wraps an Opus decoder for server TTS 24kHz → PCM.
type Decoder struct {
	dec *opus.Decoder
}

// NewDecoder creates a 24kHz mono Opus decoder for server TTS audio.
func NewDecoder() (*Decoder, error) {
	dec, err := opus.NewDecoder(DecSampleRate, 1)
	if err != nil {
		return nil, fmt.Errorf("opus decoder: %w", err)
	}
	return &Decoder{dec: dec}, nil
}

// DecodeFrame decodes an Opus frame to 24kHz PCM s16le bytes.
func (d *Decoder) DecodeFrame(opusData []byte) ([]byte, error) {
	pcm := make([]int16, DecFrameSamples)
	n, err := d.dec.Decode(opusData, pcm)
	if err != nil {
		return nil, err
	}
	out := make([]byte, n*2)
	for i := 0; i < n; i++ {
		binary.LittleEndian.PutUint16(out[i*2:], uint16(pcm[i]))
	}
	return out, nil
}

// Downsample24kTo16k linearly downsamples 24kHz s16le PCM to 16kHz (ratio 3→2).
// Port of downsample24kTo16kLinear from wirepodxiaozhi convert.go.
func Downsample24kTo16k(input []byte) []byte {
	int16s := bytesToInt16s(input)
	outputLen := (len(int16s) * 2) / 3
	output := make([]int16, outputLen)
	j := 0
	for i := 0; i < len(int16s)-2; i += 3 {
		first := (2*int32(int16s[i]) + int32(int16s[i+1])) / 3
		second := (int32(int16s[i+1]) + 2*int32(int16s[i+2])) / 3
		if j < len(output) {
			output[j] = int16(first)
		}
		if j+1 < len(output) {
			output[j+1] = int16(second)
		}
		j += 2
	}
	return int16sToBytes(output)
}

// ChunkPCM splits PCM bytes into 1024-byte chunks (padding last if needed).
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

// ApplyGain applies a soft-limited gain to 16-bit PCM (tanh limiter to prevent hard clipping).
func ApplyGain(pcm []byte, gain float64) []byte {
	if gain <= 1.0 || len(pcm) < 2 {
		return pcm
	}
	out := make([]byte, len(pcm))
	const scale = 0.98
	for i := 0; i+1 < len(pcm); i += 2 {
		s := int16(binary.LittleEndian.Uint16(pcm[i : i+2]))
		x := float64(s) / 32768.0
		y := math.Tanh(x*gain) * scale
		iv := int32(math.Round(y * 32767.0))
		if iv > math.MaxInt16 {
			iv = math.MaxInt16
		} else if iv < math.MinInt16 {
			iv = math.MinInt16
		}
		binary.LittleEndian.PutUint16(out[i:i+2], uint16(int16(iv)))
	}
	return out
}

func bytesToInt16s(data []byte) []int16 {
	s := make([]int16, len(data)/2)
	for i := range s {
		s[i] = int16(binary.LittleEndian.Uint16(data[i*2 : i*2+2]))
	}
	return s
}

func int16sToBytes(data []int16) []byte {
	b := make([]byte, len(data)*2)
	for i, v := range data {
		binary.LittleEndian.PutUint16(b[i*2:], uint16(v))
	}
	return b
}
