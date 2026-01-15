package vtr

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"strings"

	sherpa_onnx "github.com/k2-fsa/sherpa-onnx-go-linux"
	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

var voiceBuf []byte
var overarchingIgnore bool
var finalResp string

var rec *sherpa_onnx.OfflineRecognizer

func InitVosk() {
	loadIntents()
	config := sherpa.OfflineRecognizerConfig{
		ModelConfig: sherpa_onnx.OfflineModelConfig{
			NemoCTC: sherpa_onnx.OfflineNemoEncDecCtcModelConfig{
				Model: "/sherpa/citrinet-256-ls/model.onnx",
			},
			Tokens:     "/sherpa/citrinet-256-ls/tokens.txt",
			NumThreads: 4,
		},
		DecodingMethod: "greedy_search",
	}
	rec = sherpa.NewOfflineRecognizer(&config)
}

func sendUtterance(sr int, utterance []int16) string {
	audio := int16ToFloat32BytesLE(utterance)

	hdr := make([]byte, 8)
	binary.LittleEndian.PutUint32(hdr[0:4], uint32(sr))
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(len(audio)))

	str := sherpa.NewOfflineStream(rec)
	str.AcceptWaveform(sr, audio)
	rec.Decode(str)
	result := str.GetResult()

	finalResp = strings.ToLower(result.Text)
	overarchingIgnore = false
	return finalResp
}

func int16ToFloat32BytesLE(in []int16) []float32 {
	f := make([]float32, len(in))
	const scale = 1.0 / 32768.0
	for i, s := range in {
		f[i] = float32(s) * float32(scale)
	}
	return f
}

func bytesToInt16LE(b []byte) []int16 {
	if len(b)%2 != 0 {
		panic("byte slice length must be even")
	}

	out := make([]int16, len(b)/2)
	for i := 0; i < len(out); i++ {
		out[i] = int16(b[i*2]) | int16(b[i*2+1])<<8
	}
	return out
}

func Process(chunk []byte) string {
	if finalResp != "" {
		returnStr := finalResp
		finalResp = ""
		voiceBuf = []byte{}
		return returnStr
	}
	if overarchingIgnore {
		return ""
	}
	if len(chunk) == 0 {
		fmt.Println("empty chunk")
		return ""
	}
	stop, _ := DetectEndOfSpeech(chunk)
	voiceBuf = append(voiceBuf, chunk...)
	if stop {
		overarchingIgnore = true
		go sendUtterance(16000, bytesToInt16LE(voiceBuf))
	} else {
		overarchingIgnore = false
	}
	return ""
}

func GetFreq() string {
	file, err := os.ReadFile("/sys/devices/system/cpu/cpu0/cpufreq/scaling_max_freq")
	if err == nil {
		return strings.TrimSpace(string(file))
	}
	return "533333"
}

func SetFreq(cpu, ram string) {
	go exec.Command("/usr/bin/sudo", "/usr/sbin/setfreq", cpu, ram).Run()
}
