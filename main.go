package main
import (
  "fmt"
  "os"
  "github.com/tosone/minimp3"
)
func main() {
  b, _ := os.ReadFile("/tmp/ttstest/sample.mp3")
  dec, data, err := minimp3.DecodeFull(b)
  fmt.Println("err", err, "rate", dec.SampleRate, "ch", dec.Channels, "pcm", len(data))
}
