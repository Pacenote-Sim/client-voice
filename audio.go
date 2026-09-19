package voice

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"

	mp3 "github.com/hajimehoshi/go-mp3"
)

// PCM is decoded audio: signed 16-bit little-endian samples, interleaved.
type PCM struct {
	Rate     int
	Channels int
	Data     []byte
}

// Sink is where decoded audio goes: the speakers, through oto, or a test's
// recorder. Play starts the clip and returns; a clip still playing when the
// next arrives is cut, because a line for the corner behind the car is worth
// nothing.
type Sink interface {
	Play(ctx context.Context, clip PCM) error
}

// ErrFormat reports audio this plugin cannot decode.
var ErrFormat = errors.New("voice: audio in a format this plugin cannot play")

// MaxPCMBytes bounds the sound a clip decodes to. The cost of decoding is the
// decoder's own and there is no shortening it, so what is bounded is how much
// of it a mistaken caller can ask for.
const MaxPCMBytes = 64 << 20

// MaxClipBytes bounds a clip: a spoken line is tens of kilobytes.
const MaxClipBytes = 8 << 20

// decode turns a clip into PCM by its content type: audio/mpeg through the
// MP3 decoder, audio/wav read directly. Both are what the voice server plugin
// produces.
func decode(contentType string, audio []byte) (PCM, error) {
	if len(audio) == 0 {
		return PCM{}, fmt.Errorf("%w: empty", ErrFormat)
	}
	if len(audio) > MaxClipBytes {
		return PCM{}, fmt.Errorf("%w: %d bytes is not a spoken line", ErrFormat, len(audio))
	}
	ct := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	switch {
	case ct == "audio/mpeg" || ct == "audio/mp3" || (ct == "" && !bytes.HasPrefix(audio, []byte("RIFF"))):
		return decodeMP3(audio)
	case ct == "audio/wav" || ct == "audio/x-wav" || ct == "audio/wave" || bytes.HasPrefix(audio, []byte("RIFF")):
		return decodeWAV(audio)
	}
	return PCM{}, fmt.Errorf("%w: %s", ErrFormat, contentType)
}

func decodeMP3(audio []byte) (PCM, error) {
	d, err := mp3.NewDecoder(bytes.NewReader(audio))
	if err != nil {
		return PCM{}, fmt.Errorf("%w: %w", ErrFormat, err)
	}
	data, err := io.ReadAll(io.LimitReader(d, MaxPCMBytes))
	if err != nil {
		return PCM{}, fmt.Errorf("%w: %w", ErrFormat, err)
	}
	return PCM{Rate: d.SampleRate(), Channels: 2, Data: data}, nil
}

// decodeWAV reads a RIFF/WAVE file holding 16-bit PCM.
func decodeWAV(audio []byte) (PCM, error) {
	if len(audio) < 12 || string(audio[:4]) != "RIFF" || string(audio[8:12]) != "WAVE" {
		return PCM{}, fmt.Errorf("%w: not a WAV file", ErrFormat)
	}
	var out PCM
	var bits int
	pos := 12
	for pos+8 <= len(audio) {
		id := string(audio[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(audio[pos+4 : pos+8]))
		body := pos + 8
		if size < 0 || body+size > len(audio) {
			size = len(audio) - body
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return PCM{}, fmt.Errorf("%w: a short fmt chunk", ErrFormat)
			}
			format := binary.LittleEndian.Uint16(audio[body : body+2])
			out.Channels = int(binary.LittleEndian.Uint16(audio[body+2 : body+4]))
			out.Rate = int(binary.LittleEndian.Uint32(audio[body+4 : body+8]))
			bits = int(binary.LittleEndian.Uint16(audio[body+14 : body+16]))
			if format != 1 && format != 0xFFFE {
				return PCM{}, fmt.Errorf("%w: WAV format %d is not PCM", ErrFormat, format)
			}
		case "data":
			out.Data = append([]byte(nil), audio[body:body+size]...)
		}
		pos = body + size + size%2
	}
	switch {
	case out.Rate == 0 || out.Channels == 0:
		return PCM{}, fmt.Errorf("%w: no fmt chunk", ErrFormat)
	case bits != 16:
		return PCM{}, fmt.Errorf("%w: %d-bit WAV; this plugin plays 16-bit", ErrFormat, bits)
	case len(out.Data) == 0:
		return PCM{}, fmt.Errorf("%w: no samples", ErrFormat)
	}
	return out, nil
}

// scaled is the clip at volume percent: every sample multiplied.
func scaled(clip PCM, volume int) PCM {
	if volume >= 100 {
		return clip
	}
	out := PCM{Rate: clip.Rate, Channels: clip.Channels, Data: make([]byte, len(clip.Data)&^1)}
	for i := 0; i+1 < len(clip.Data); i += 2 {
		s := int(int16(binary.LittleEndian.Uint16(clip.Data[i:]))) //nolint:gosec // G115: the bytes are a signed sample; this reads them as one.
		s = s * volume / 100
		binary.LittleEndian.PutUint16(out.Data[i:], uint16(int16(s))) //nolint:gosec // G115: s is within int16 after scaling down.
	}
	return out
}

// converted is the clip at another rate and channel count, for an output
// opened for a different clip. Linear interpolation; a spoken line does not
// need better.
func converted(clip PCM, rate, channels int) PCM {
	if clip.Rate == rate && clip.Channels == channels {
		return clip
	}
	frames := len(clip.Data) / 2 / max(clip.Channels, 1)
	if frames == 0 {
		return PCM{Rate: rate, Channels: channels}
	}
	sample := func(frame, ch int) int {
		if ch >= clip.Channels {
			ch = clip.Channels - 1
		}
		i := (frame*clip.Channels + ch) * 2
		return int(int16(binary.LittleEndian.Uint16(clip.Data[i:]))) //nolint:gosec // G115: the bytes are a signed sample; this reads them as one.
	}
	outFrames := frames * rate / clip.Rate
	out := PCM{Rate: rate, Channels: channels, Data: make([]byte, outFrames*channels*2)}
	for f := range outFrames {
		src := float64(f) * float64(clip.Rate) / float64(rate)
		i0 := int(src)
		i1 := min(i0+1, frames-1)
		t := src - float64(i0)
		for ch := range channels {
			v := float64(sample(i0, ch))*(1-t) + float64(sample(i1, ch))*t
			binary.LittleEndian.PutUint16(out.Data[(f*channels+ch)*2:], uint16(int16(v))) //nolint:gosec // G115: an interpolation of int16 values.
		}
	}
	return out
}
