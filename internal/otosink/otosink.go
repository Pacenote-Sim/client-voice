// Package otosink plays PCM through the machine's speakers with oto. It needs
// a sound device, so it has no tests of its own here; the companion is tested
// against a sink that records, and this is heard on a real machine.
package otosink

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ebitengine/oto/v3"
)

// Clip is what the companion hands over: 16-bit little-endian PCM.
type Clip struct {
	Rate     int
	Channels int
	Data     []byte
}

// Sink is the speakers. The output is opened at the first clip's rate and
// channel count and kept; later clips are converted to it by the caller,
// which asks Format.
type Sink struct {
	mu      sync.Mutex
	ctx     *oto.Context
	rate    int
	ch      int
	current *oto.Player
}

// New is a sink with no output open yet.
func New() *Sink { return &Sink{} }

// Format is the output's rate and channels once open, or zeros.
func (s *Sink) Format() (rate, channels int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rate, s.ch
}

// Play starts the clip and cuts whatever was playing.
func (s *Sink) Play(_ context.Context, clip Clip) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx == nil {
		c, ready, err := oto.NewContext(&oto.NewContextOptions{
			SampleRate: clip.Rate, ChannelCount: clip.Channels, Format: oto.FormatSignedInt16LE,
			BufferSize: 100 * time.Millisecond, ApplicationName: "Pacenote",
		})
		if err != nil {
			return fmt.Errorf("otosink: opening the speakers: %w", err)
		}
		select {
		case <-ready:
		case <-time.After(5 * time.Second):
			return fmt.Errorf("otosink: the speakers did not become ready")
		}
		s.ctx, s.rate, s.ch = c, clip.Rate, clip.Channels
	}
	if clip.Rate != s.rate || clip.Channels != s.ch {
		return fmt.Errorf("otosink: the output is %d Hz %d ch; convert the clip first", s.rate, s.ch)
	}
	if s.current != nil {
		s.current.Pause() // the line for the corner behind the car is worth nothing
	}
	p := s.ctx.NewPlayer(bytes.NewReader(clip.Data))
	p.Play()
	s.current = p
	return nil
}
