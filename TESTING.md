# Testing the voice companion

`make` runs what CI runs, in order: format, build, vet, lint, the suite with the race detector and
shuffled order, coverage over 90 %, every benchmark once, and `go mod tidy` a no-op.

```
make            # everything, in order
make bench      # what a clip costs to decode and to scale
```

## What the suite asserts

| | |
|---|---|
| playing | MP3 and 16-bit WAV decoded, scaled to the volume, and handed to the speakers; a clip still playing when the next arrives is cut |
| speaking | words without audio sent to the voice plugin, and when it cannot answer, the machine's own voice — without holding up the companion that asked |
| the modes | spoken, text only, off: what each plays, what each shows, and what each tells another companion about whether anything would be heard |
| the page | the volume, the mode, the lines heard, and the controls that change them |
| what a mistaken caller sends | an empty clip, a clip too large, something that is not audio, and a format sniffed from its bytes rather than trusted from its header |

The speakers are a stand-in in the tests. `internal/otosink` is the real ones and has no tests of its
own: it needs a sound device, and it is heard on a machine rather than asserted.

## What it costs

| | |
|---|---|
| decoding | about 3 ms per second of audio, so 12 ms for a ten-word line |
| scaling to the volume | 2.8 ms for the same clip, one allocation |

Both are the decoder's cost rather than this package's, and both happen once a line, on the straight
before the corner the line is about.
