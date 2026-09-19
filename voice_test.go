package voice_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pacenote-sim/clientplugin"
	"github.com/pacenote-sim/clientplugin/clientplugintest"

	voice "github.com/pacenote-sim/client-voice"
)

// wav is a 16-bit PCM WAV of a sine tone: rate, channels, frames.
func wav(rate, channels, frames int) []byte {
	data := make([]byte, frames*channels*2)
	for f := range frames {
		v := int16(math.Sin(2*math.Pi*440*float64(f)/float64(rate)) * 10000)
		for ch := range channels {
			binary.LittleEndian.PutUint16(data[(f*channels+ch)*2:], uint16(v))
		}
	}
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+len(data)))
	b.WriteString("WAVEfmt ")
	_ = binary.Write(&b, binary.LittleEndian, uint32(16))
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))
	_ = binary.Write(&b, binary.LittleEndian, uint16(channels))
	_ = binary.Write(&b, binary.LittleEndian, uint32(rate))
	_ = binary.Write(&b, binary.LittleEndian, uint32(rate*channels*2))
	_ = binary.Write(&b, binary.LittleEndian, uint16(channels*2))
	_ = binary.Write(&b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(data)))
	b.Write(data)
	return b.Bytes()
}

type recorder struct {
	mu    sync.Mutex
	clips []voice.PCM
	fail  bool
}

func (r *recorder) Play(_ context.Context, clip voice.PCM) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return errors.New("no device")
	}
	r.clips = append(r.clips, clip)
	return nil
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.clips)
}

func peak(clip voice.PCM) int {
	m := 0
	for i := 0; i+1 < len(clip.Data); i += 2 {
		v := int(int16(binary.LittleEndian.Uint16(clip.Data[i:])))
		if v < 0 {
			v = -v
		}
		m = max(m, v)
	}
	return m
}

type machine struct {
	mu    sync.Mutex
	lines []string
}

func saidByMachine(m *machine) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.lines...)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("not in time")
}

func newCompanion(t *testing.T, routes http.Handler) (*voice.Companion, *recorder, *clientplugintest.Host, *machine) {
	t.Helper()
	h := clientplugintest.NewHost(t, routes)
	c := voice.New()
	rec := &recorder{}
	c.Sink = rec
	m := &machine{}
	c.Speak = func(_ context.Context, text string) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.lines = append(m.lines, text)
		return nil
	}
	require.NoError(t, c.Start(context.Background(), h))
	t.Cleanup(func() { _ = c.Stop() })
	return c, rec, h, m
}

func TestItIsAPlugin(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	m, err := clientplugin.LoadManifest(".")
	r.NoError(err)
	r.Equal(voice.Name, m.Name)
	r.Equal(voice.Name, m.ServerPlugin)
	c, ok := clientplugin.Default.Companion(voice.Name)
	r.True(ok, "init() registered it")
	_, isPlayer := c.(clientplugin.Player)
	r.True(isPlayer, "it is the one that makes sound")
	_, isActor := c.(clientplugin.Actor)
	r.True(isActor, "its page has controls")
	r.Equal([]clientplugin.EventKind{clientplugin.KindStintStarted}, c.Wants())
}

func TestAudioIsPlayedAtTheVolume(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()
	c, rec, h, _ := newCompanion(t, nil)
	r.Equal("volume 80", h.Statuses()[len(h.Statuses())-1])
	r.Contains(h.Pages()[len(h.Pages())-1], `data-action="volume"`)

	clip := wav(24000, 1, 2400)
	r.NoError(c.Play(ctx, clip, "audio/wav"))
	r.Equal(1, rec.count())
	r.Equal(24000, rec.clips[0].Rate)
	r.Equal(1, rec.clips[0].Channels)
	r.InDelta(8000, peak(rec.clips[0]), 100, "80 % of a 10 000 peak")

	// The content type may be missing or say mpeg; RIFF bytes are WAV either way.
	r.NoError(c.Play(ctx, clip, ""))
	r.NoError(c.Play(ctx, clip, "audio/x-wav; codec=1"))
	r.Equal(3, rec.count())

	// What cannot be decoded is an error, not silence.
	r.ErrorIs(c.Play(ctx, nil, "audio/wav"), voice.ErrFormat)
	r.ErrorIs(c.Play(ctx, []byte("not audio at all"), "audio/mpeg"), voice.ErrFormat)
	r.ErrorIs(c.Play(ctx, []byte("RIFF....WAVEjunk"), "audio/wav"), voice.ErrFormat)
	r.ErrorIs(c.Play(ctx, []byte("OggS....not ours"), "audio/ogg"), voice.ErrFormat)
	r.NoError(c.Play(ctx, clip, "audio/ogg"), "RIFF bytes are WAV whatever the label says")
	r.Equal(4, rec.count())
	r.ErrorIs(c.Play(ctx, make([]byte, voice.MaxClipBytes+1), "audio/wav"), voice.ErrFormat)
	eightBit := wav(8000, 1, 10)
	eightBit[34] = 8
	r.ErrorIs(c.Play(ctx, eightBit, "audio/wav"), voice.ErrFormat)
	r.Equal(4, rec.count())

	// A sink that fails says so.
	rec.fail = true
	r.ErrorContains(c.Play(ctx, clip, "audio/wav"), "no device")
}

func TestWordsAreSpokenThroughTheServerOrTheMachine(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	var asked []string
	var mu sync.Mutex
	down := false
	routes := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/speak" {
			http.NotFound(w, req)
			return
		}
		body := make([]byte, 4096)
		n, _ := req.Body.Read(body)
		mu.Lock()
		asked = append(asked, string(body[:n]))
		isDown := down
		mu.Unlock()
		if isDown {
			http.Error(w, "no key", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(wav(16000, 1, 1600))
	})
	c, rec, h, spoken := newCompanion(t, routes)

	r.NoError(c.Say(ctx, " Brake later into Turn 1. "))
	r.Equal(1, rec.count(), "the server's audio was played")
	r.Equal(16000, rec.clips[0].Rate)
	mu.Lock()
	r.JSONEq(`{"text":"Brake later into Turn 1."}`, asked[0])
	mu.Unlock()
	r.Contains(h.Pages()[len(h.Pages())-1], "Brake later into Turn 1.")
	r.Empty(saidByMachine(spoken))

	// The server cannot speak: the machine does, without holding the caller
	// up, and the status says why.
	mu.Lock()
	down = true
	mu.Unlock()
	r.NoError(c.Say(ctx, "Pit this lap."))
	r.Equal(1, rec.count())
	waitFor(t, func() bool { return len(saidByMachine(spoken)) == 1 })
	r.Equal([]string{"Pit this lap."}, saidByMachine(spoken))
	r.Contains(h.Statuses()[len(h.Statuses())-1], "the machine's voice: the voice plugin answered 503")
	r.NoError(c.Say(ctx, "   "), "nothing to say is nothing")

	// A machine with no voice either: logged, not an error to the caller,
	// who has a lap to keep up with.
	c.Speak = func(context.Context, string) error { return voice.ErrNoSystemVoice }
	r.NoError(c.Say(ctx, "Box."))
	r.NoError(c.Stop())
	r.NoError(c.Start(ctx, h))

	// The server is back: the status drops the fallback.
	mu.Lock()
	down = false
	mu.Unlock()
	r.NoError(c.Say(ctx, "Green flag."))
	r.NotContains(h.Statuses()[len(h.Statuses())-1], "machine")

	// The page keeps the last lines, newest first, and only so many.
	for i := range 12 {
		_ = c.Say(ctx, "line "+string(rune('a'+i)))
	}
	page := h.Pages()[len(h.Pages())-1]
	r.Contains(page, "line l")
	r.NotContains(page, "line a")
	r.Less(strings.Index(page, "line l"), strings.Index(page, "line k"), "newest first")

	// A new stint clears them.
	r.NoError(c.Notify(ctx, &clientplugin.StintStarted{At: time.Now()}))
	r.NotContains(h.Pages()[len(h.Pages())-1], "Heard")
	r.NoError(c.Notify(ctx, &clientplugin.LapCompleted{}), "an event it did not ask for is nothing")
}

func TestTheControlsAreItsOwnAndKept(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()
	c, rec, h, spoken := newCompanion(t, nil)
	clip := wav(24000, 1, 240)

	r.NoError(clientplugintest.Act(ctx, c, "volume", map[string]string{"level": "50"}))
	r.Equal("50", h.Setting("volume"), "kept through the host")
	r.Equal("volume 50", h.Statuses()[len(h.Statuses())-1])
	r.NoError(c.Play(ctx, clip, "audio/wav"))
	r.InDelta(5000, peak(rec.clips[0]), 100)
	r.ErrorContains(clientplugintest.Act(ctx, c, "volume", map[string]string{"level": "loud"}), "0 to 100")
	r.ErrorContains(clientplugintest.Act(ctx, c, "volume", map[string]string{"level": "101"}), "0 to 100")
	r.ErrorContains(clientplugintest.Act(ctx, c, "dance", nil), "no control")

	// Text only: shown, not played, not spoken.
	r.NoError(clientplugintest.Act(ctx, c, "mode", map[string]string{"mode": "text"}))
	r.Equal("text only", h.Statuses()[len(h.Statuses())-1])
	r.NoError(c.Play(ctx, clip, "audio/wav"))
	r.NoError(c.Say(ctx, "Turn four."))
	r.Equal(1, rec.count())
	r.Empty(saidByMachine(spoken))
	r.Contains(h.Pages()[len(h.Pages())-1], "Turn four.")

	// Off: nothing at all, not even on the page.
	r.NoError(clientplugintest.Act(ctx, c, "mode", map[string]string{"mode": "off"}))
	r.Equal("off", h.Statuses()[len(h.Statuses())-1])
	r.NoError(c.Say(ctx, "Turn five."))
	r.NotContains(h.Pages()[len(h.Pages())-1], "Turn five.")
	r.ErrorContains(clientplugintest.Act(ctx, c, "mode", map[string]string{"mode": "loud"}), "on, text or off")

	// A restart reads the settings back.
	again := voice.New()
	again.Sink = &recorder{}
	r.NoError(again.Start(ctx, h))
	r.Equal("off", h.Statuses()[len(h.Statuses())-1])
	r.NoError(clientplugintest.Act(ctx, again, "mode", map[string]string{"mode": "on"}))
	r.Equal("volume 50", h.Statuses()[len(h.Statuses())-1])
	r.NoError(again.Stop())

	// Before Start, controls change the companion and keep nothing.
	fresh := voice.New()
	r.NoError(clientplugintest.Act(ctx, fresh, "volume", map[string]string{"level": "10"}))
	r.NoError(fresh.Say(ctx, "x"), "no host: nothing to speak through")
	r.NoError(fresh.Stop())
}

func TestClipsAreConvertedForTheOutput(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	mono := wav(24000, 1, 2400)
	clip, err := voice.Decode("audio/wav", mono)
	r.NoError(err)
	stereo := voice.Converted(clip, 48000, 2)
	r.Equal(48000, stereo.Rate)
	r.Equal(2, stereo.Channels)
	r.Len(stereo.Data, 4800*2*2, "twice the frames, twice the channels")
	r.InDelta(peak(clip), peak(stereo), 200, "the same loudness")
	same := voice.Converted(clip, 24000, 1)
	r.Equal(clip.Data, same.Data)
	empty := voice.Converted(voice.PCM{Rate: 24000, Channels: 1}, 48000, 2)
	r.Empty(empty.Data)
	r.Equal(peak(clip), peak(voice.Scaled(clip, 100)))
	r.Zero(peak(voice.Scaled(clip, 0)))
}

type fakeOutput struct {
	rate, ch int
	clips    []voice.Clip
}

func (f *fakeOutput) Format() (int, int) { return f.rate, f.ch }
func (f *fakeOutput) Play(_ context.Context, c voice.Clip) error {
	if f.rate == 0 {
		f.rate, f.ch = c.Rate, c.Channels
	}
	f.clips = append(f.clips, c)
	return nil
}

func TestTheSpeakersTakeEveryClipInTheirOwnFormat(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	out := &fakeOutput{}
	sink := voice.Speakers(out)
	first, err := voice.Decode("audio/wav", wav(24000, 1, 240))
	r.NoError(err)
	r.NoError(sink.Play(context.Background(), first))
	r.Equal(24000, out.clips[0].Rate, "the first clip sets the format")
	second, err := voice.Decode("audio/wav", wav(48000, 2, 480))
	r.NoError(err)
	r.NoError(sink.Play(context.Background(), second))
	r.Equal(24000, out.clips[1].Rate, "the next clip is converted to it")
	r.Equal(1, out.clips[1].Channels)
	r.Len(out.clips[1].Data, 240*2)
}

func TestTheMachinesVoiceIsAskedProperly(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	r.NoError(voice.SystemSpeak(context.Background(), "  "), "nothing to say is nothing")
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	// With the context already over, no voice speaks: the command fails at
	// once, or this machine has none. Either is an error, and nothing is heard.
	r.Error(voice.SystemSpeak(cancelled, "hello"))
}

// mpeg2.mp3 is one of the go-mp3 decoder's own example files (Apache-2.0), the one
// real MP3 in these tests: the voice server plugin sends MP3 by default.
func TestAnMP3IsDecoded(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	audio, err := os.ReadFile("testdata/mpeg2.mp3")
	r.NoError(err)
	clip, err := voice.Decode("audio/mpeg", audio)
	r.NoError(err)
	r.Equal(2, clip.Channels, "the decoder gives stereo")
	r.Contains([]int{44100, 48000, 32000, 22050, 24000, 16000}, clip.Rate)
	r.Greater(len(clip.Data), 10000)
	r.Positive(peak(clip))
	c, rec, _, _ := newCompanion(t, nil)
	r.NoError(c.Play(context.Background(), audio, "audio/mpeg"))
	r.Equal(1, rec.count())
	r.NoError(c.Play(context.Background(), audio, ""), "no label: not RIFF, so MP3")
	r.Equal(2, rec.count())
}

// The driver's own mode is what a companion's server half is told: with the
// sound off, or down to the words alone, nothing would be heard and no audio
// should be bought for this client.
func TestTheDriverSModeSaysWhetherAnythingIsHeard(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	c := voice.New()
	c.Sink = &recorder{}
	h := clientplugintest.NewHost(t, nil)
	r.NoError(c.Start(context.Background(), h))
	r.True(c.Audible(), "sound is on by default")

	r.NoError(c.Act(context.Background(), voice.KeyMode, map[string]string{"mode": voice.ModeText}))
	r.False(c.Audible(), "the words alone are not audio")
	r.NoError(c.Act(context.Background(), voice.KeyMode, map[string]string{"mode": voice.ModeOff}))
	r.False(c.Audible())
	r.NoError(c.Act(context.Background(), voice.KeyMode, map[string]string{"mode": voice.ModeOn}))
	r.True(c.Audible())
	r.NoError(c.Stop())
}

// What a line costs to play. It happens once a corner, and it happens while
// the driver is approaching that corner, so the clip has to be decoded and
// scaled in the time between the words arriving and the words being wanted.
func BenchmarkDecodingAClip(b *testing.B) {
	audio, err := os.ReadFile("testdata/mpeg2.mp3")
	if err != nil {
		b.Skip("no fixture")
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := voice.Decode("audio/mpeg", audio); err != nil {
			b.Fatal(err)
		}
	}
}

// Scaling is what the volume does to a decoded clip, sample by sample.
func BenchmarkScalingAClip(b *testing.B) {
	audio, err := os.ReadFile("testdata/mpeg2.mp3")
	if err != nil {
		b.Skip("no fixture")
	}
	clip, err := voice.Decode("audio/mpeg", audio)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = voice.Scaled(clip, 70)
	}
}
