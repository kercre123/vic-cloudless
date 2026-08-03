package xiaozhi

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tosone/minimp3"

	"github.com/digital-dream-labs/vector-cloud/internal/log"
)

// GameGoogleTTSVIEnabled reports whether Google Vietnamese game comments are allowed
// (configured on Xiaozhi tab; works under both Vosk and Xiaozhi listen modes).
func GameGoogleTTSVIEnabled() bool {
	return LoadConfig().GameGoogleTTSVI
}

// RunGoogleViAnnounce fetches Google Translate TTS (vi) and plays via ALSA.
// Falls back to Acapela SayText on failure.
func RunGoogleViAnnounce(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	// Google TTS URL length / rate limits — keep short.
	if utf8.RuneCountInString(text) > 160 {
		r := []rune(text)
		text = string(r[:160])
	}
	log.Printf("[Xiaozhi][Chess] announce Google VI: %q", text)
	DisarmRelistenPending()

	pcm, err := fetchGoogleTranslatePCM16k(text, "vi")
	if err != nil {
		log.Println("[Xiaozhi][Chess] Google VI TTS failed, SayText fallback:", err)
		return RunChessAnnounce(text)
	}
	// Google Translate TTS is quieter than Acapela / Xiaozhi; soft-boost before ALSA.
	// Keep gain modest — 3× + noisy MP3 decode saturates and sounds rè.
	pcm = ApplyGain(pcm, 1.7)
	if err := playPCM16kALSA(pcm); err != nil {
		log.Println("[Xiaozhi][Chess] Google VI ALSA failed, SayText fallback:", err)
		return RunChessAnnounce(text)
	}
	log.Println("[Xiaozhi][Chess] announce done (Google VI)")
	return nil
}

func fetchGoogleTranslatePCM16k(text, lang string) ([]byte, error) {
	u := "https://translate.google.com/translate_tts?ie=UTF-8&client=tw-ob&tl=" +
		url.QueryEscape(lang) + "&q=" + url.QueryEscape(text)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 10) AppleWebKit/537.36 Chrome/120.0.0.0 Mobile Safari/537.36")
	req.Header.Set("Referer", "https://translate.google.com/")
	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("google tts HTTP %d", resp.StatusCode)
	}
	mp3, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return nil, err
	}
	if len(mp3) < 64 {
		return nil, fmt.Errorf("google tts empty body")
	}
	dec, data, err := minimp3.DecodeFull(mp3)
	if err != nil {
		return nil, fmt.Errorf("minimp3: %w", err)
	}
	if dec.Channels < 1 || dec.SampleRate < 8000 || len(data) == 0 {
		return nil, fmt.Errorf("bad pcm decode sr=%d ch=%d", dec.SampleRate, dec.Channels)
	}
	mono := data
	if dec.Channels > 1 {
		mono = stereoToMonoS16(data, dec.Channels)
	}
	if dec.SampleRate == 16000 {
		return mono, nil
	}
	return resampleS16(mono, dec.SampleRate, 16000), nil
}

func stereoToMonoS16(in []byte, ch int) []byte {
	if ch <= 1 {
		return in
	}
	frame := ch * 2
	n := len(in) / frame
	out := make([]byte, n*2)
	for i := 0; i < n; i++ {
		out[i*2] = in[i*frame]
		out[i*2+1] = in[i*frame+1]
	}
	return out
}

func resampleS16(in []byte, fromRate, toRate int) []byte {
	if fromRate <= 0 || toRate <= 0 || fromRate == toRate {
		return in
	}
	inN := len(in) / 2
	if inN < 2 {
		return in
	}
	outN := int(float64(inN) * float64(toRate) / float64(fromRate))
	if outN < 1 {
		outN = 1
	}
	out := make([]byte, outN*2)
	for i := 0; i < outN; i++ {
		src := float64(i) * float64(fromRate) / float64(toRate)
		i0 := int(src)
		if i0 >= inN-1 {
			i0 = inN - 2
		}
		frac := src - float64(i0)
		s0 := int16(uint16(in[i0*2]) | uint16(in[i0*2+1])<<8)
		s1 := int16(uint16(in[(i0+1)*2]) | uint16(in[(i0+1)*2+1])<<8)
		v := int16(float64(s0)*(1-frac) + float64(s1)*frac)
		out[i*2] = byte(v)
		out[i*2+1] = byte(v >> 8)
	}
	return out
}

func playPCM16kALSA(pcm []byte) error {
	if len(pcm) == 0 {
		return fmt.Errorf("empty pcm")
	}
	if !UseAlsaPlayback() {
		return fmt.Errorf("ALSA playback disabled")
	}
	if err := StreamStart(); err != nil {
		return err
	}
	// Soft head pad so tinyplay ring isn't empty/cold.
	pad := make([]byte, 1600) // 50ms
	_, _ = StreamAppend(pad)

	const chunk = 4096
	for off := 0; off < len(pcm); {
		end := off + chunk
		if end > len(pcm) {
			end = len(pcm)
		}
		if _, err := StreamAppend(pcm[off:end]); err != nil {
			_ = StreamEnd()
			return err
		}
		off = end
	}
	// Tail pad helps tinyplay drain cleanly without underrun click on end.
	_, _ = StreamAppend(pad)
	// StreamEnd waits for ALSA pump / tinyplay drain — do not sleep then cut.
	if err := StreamEnd(); err != nil {
		return err
	}
	SetPlaying(false)
	return nil
}
