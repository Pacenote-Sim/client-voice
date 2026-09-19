package voice

import (
	"context"

	"github.com/pacenote-sim/client-voice/internal/otosink"
)

// The decoding and conversion, for the tests.

// Decode is decode.
func Decode(contentType string, audio []byte) (PCM, error) { return decode(contentType, audio) }

// Converted is converted.
func Converted(clip PCM, rate, channels int) PCM { return converted(clip, rate, channels) }

// Scaled is scaled.
func Scaled(clip PCM, volume int) PCM { return scaled(clip, volume) }

// SystemSpeak is systemSpeak, for a test that has a machine to try.
func SystemSpeak(ctx context.Context, text string) error { return systemSpeak(ctx, text) }

// Output is output; Speakers wraps one the way New does.
type Output = output

// Speakers is the Sink New builds, over the given output.
func Speakers(out Output) Sink { return speakers{out} }

// Clip is otosink.Clip.
type Clip = otosink.Clip
