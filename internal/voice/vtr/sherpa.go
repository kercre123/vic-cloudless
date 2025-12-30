package vtr

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

var voiceBuf []byte
var overarchingIgnore bool
var finalResp string

func InitVosk() {
	loadIntents()
}

func sendUtterance(wsURL string, sr int, utterance []int16) string {
	u, err := url.Parse(wsURL)
	if err != nil {
		return ""
	}

	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		return ""
	}
	defer conn.Close()

	audio := int16ToFloat32BytesLE(utterance)

	hdr := make([]byte, 8)
	binary.LittleEndian.PutUint32(hdr[0:4], uint32(sr))
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(len(audio)))

	payload := append(hdr, audio...)

	if err := conn.WriteMessage(websocket.BinaryMessage, payload); err != nil {
		return ""
	}
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	var final string
	for {
		mt, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if mt == websocket.TextMessage {
			var s sherpResp
			json.Unmarshal(msg, &s)
			final = s.Text
			finalResp = s.Text
			overarchingIgnore = false
			fmt.Printf("%s", string(msg))
			break
		}
	}

	// stupid
	_ = conn.WriteMessage(websocket.TextMessage, []byte("Done!"))
	return final
}

func int16ToFloat32BytesLE(in []int16) []byte {
	f := make([]float32, len(in))
	const scale = 1.0 / 32768.0
	for i, s := range in {
		f[i] = float32(s) * float32(scale)
	}
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, f)
	return b.Bytes()
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

type sherpResp struct {
	Lang       string   `json:"lang"`
	Emotion    string   `json:"emotion"`
	Event      string   `json:"event"`
	Text       string   `json:"text"`
	Timestamps []any    `json:"timestamps"`
	Durations  []any    `json:"durations"`
	Tokens     []string `json:"tokens"`
	YsLogProbs []any    `json:"ys_log_probs"`
	Words      []any    `json:"words"`
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
		go sendUtterance("ws://localhost:6006", 16000, bytesToInt16LE(voiceBuf))
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
