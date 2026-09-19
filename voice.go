// Package voice is the client half of the voice server plugin, and the one
// plugin in the client that makes sound. Every other companion's Host.Play
// and Host.Say come here: audio that arrived with engineer's lines is played,
// words that came alone are spoken through the voice server plugin, and when
// that cannot answer, through the machine's own voice. The volume and the
// mode are its settings, on its page.
package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/pacenote-sim/clientplugin"

	"github.com/pacenote-sim/client-voice/internal/otosink"
)

func init() { clientplugin.RegisterCompanion(New()) }

// Name is the server plugin this is the client half of.
const Name = "voice"

// The settings, by key, and their defaults.
const (
	KeyVolume     = "volume"
	KeyMode       = "mode"
	DefaultVolume = 80
	// ModeOn plays and speaks; ModeText shows the words and plays nothing;
	// ModeOff does neither.
	ModeOn   = "on"
	ModeText = "text"
	ModeOff  = "off"
	// KeepLines is how many recent lines the page shows.
	KeepLines = 8
)

// Companion is the companion.
type Companion struct {
	// Sink is the speakers; a test replaces it. Speak is the machine's own
	// voice; a test replaces it too.
	Sink  Sink
	Speak func(ctx context.Context, text string) error

	mu     sync.Mutex
	host   clientplugin.Host
	ctx    context.Context
	cancel context.CancelFunc
	volume int
	mode   string
	lines  []string
	last   string
	// fallback is why the machine's voice is speaking, or "" when the voice
	// plugin is. speaking ends the machine's voice in progress.
	fallback string
	speaking context.CancelFunc
	wg       sync.WaitGroup
}

// New is a companion at its defaults, playing through the speakers.
func New() *Companion {
	return &Companion{Sink: speakers{otosink.New()}, Speak: systemSpeak, volume: DefaultVolume, mode: ModeOn}
}

// Name implements [clientplugin.Companion].
func (*Companion) Name() string { return Name }

// Wants implements [clientplugin.Companion]: only the start of a stint, to
// clear the page.
func (*Companion) Wants() []clientplugin.EventKind {
	return []clientplugin.EventKind{clientplugin.KindStintStarted}
}

// Start implements [clientplugin.Companion]: the settings come back from the host.
func (c *Companion) Start(ctx context.Context, h clientplugin.Host) error {
	c.mu.Lock()
	c.host = h
	c.ctx, c.cancel = context.WithCancel(ctx)
	if v, err := strconv.Atoi(h.Setting(KeyVolume)); err == nil && v >= 0 && v <= 100 {
		c.volume = v
	}
	if m := h.Setting(KeyMode); m == ModeOn || m == ModeText || m == ModeOff {
		c.mode = m
	}
	c.mu.Unlock()
	c.show()
	return nil
}

// Stop implements [clientplugin.Companion].
func (c *Companion) Stop() error {
	c.mu.Lock()
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	c.wg.Wait()
	return nil
}

// Notify implements [clientplugin.Companion].
func (c *Companion) Notify(_ context.Context, e clientplugin.Event) error {
	if _, ok := e.(*clientplugin.StintStarted); ok {
		c.mu.Lock()
		c.lines, c.last = nil, ""
		c.mu.Unlock()
		c.show()
	}
	return nil
}

// Play implements [clientplugin.Player]: a clip another plugin received with
// its words, played at the volume, unless the mode says not to.
func (c *Companion) Play(ctx context.Context, audio []byte, contentType string) error {
	c.mu.Lock()
	mode, volume, sink := c.mode, c.volume, c.Sink
	c.mu.Unlock()
	if mode != ModeOn {
		return nil
	}
	clip, err := decode(contentType, audio)
	if err != nil {
		return err
	}
	if err := sink.Play(ctx, scaled(clip, volume)); err != nil {
		return fmt.Errorf("voice: %w", err)
	}
	return nil
}

// Say implements [clientplugin.Player]: words that came without audio. The
// voice server plugin speaks them; when it cannot, the machine does. The
// words are shown on the page in every mode but off.
func (c *Companion) Say(ctx context.Context, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	c.mu.Lock()
	mode, h := c.mode, c.host
	if mode != ModeOff {
		c.last = text
		c.lines = append(c.lines, text)
		if len(c.lines) > KeepLines {
			c.lines = c.lines[len(c.lines)-KeepLines:]
		}
	}
	c.mu.Unlock()
	if mode != ModeOff {
		c.show()
	}
	if mode != ModeOn || h == nil {
		return nil
	}
	audio, contentType, err := c.speakThroughServer(ctx, h, text)
	if err == nil {
		c.setFallback("")
		return c.Play(ctx, audio, contentType)
	}
	h.Log().Info("the voice plugin could not speak; using the machine's voice", "reason", err.Error())
	c.setFallback(err.Error())
	c.speakAside(text)
	return nil
}

// speakAside speaks with the machine's voice without holding up the caller:
// the words are for the corner ahead, and the companion that asked has a
// lap to keep up with. The latest line wins over one still being spoken.
func (c *Companion) speakAside(text string) {
	c.mu.Lock()
	if c.speaking != nil {
		c.speaking()
	}
	parent := c.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	c.speaking = cancel
	speak := c.Speak
	h := c.host
	c.mu.Unlock()
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer cancel()
		if err := speak(ctx, text); err != nil && !errors.Is(err, context.Canceled) && h != nil {
			h.Log().Warn("the machine's voice failed too", "reason", err.Error())
		}
	}()
}

// setFallback records why the machine's voice is in use, and shows it.
func (c *Companion) setFallback(reason string) {
	c.mu.Lock()
	changed := c.fallback != reason
	c.fallback = reason
	c.mu.Unlock()
	if changed {
		c.show()
	}
}

// speakThroughServer asks the voice server plugin for the words as audio.
func (c *Companion) speakThroughServer(ctx context.Context, h clientplugin.Host, text string) ([]byte, string, error) {
	body, _ := json.Marshal(map[string]string{"text": text})
	res, err := h.Do(ctx, http.MethodPost, "/speak", bytes.NewReader(body), http.Header{"Content-Type": {"application/json"}})
	if err != nil {
		return nil, "", err //nolint:wrapcheck // the host's words name the plugin.
	}
	defer res.Body.Close() //nolint:errcheck // a read answer.
	if res.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return nil, "", fmt.Errorf("the voice plugin answered %d: %s", res.StatusCode, strings.TrimSpace(string(msg)))
	}
	audio, err := io.ReadAll(io.LimitReader(res.Body, MaxClipBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("reading the audio: %w", err)
	}
	return audio, res.Header.Get("Content-Type"), nil
}

// Audible implements [clientplugin.Muted]: whether a clip handed to Play would
// be heard. It is the driver's own mode, and it is what tells a companion's
// server half not to buy audio nobody will listen to.
func (c *Companion) Audible() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mode == ModeOn
}

// Act implements [clientplugin.Actor]: the page's controls.
func (c *Companion) Act(_ context.Context, action string, values map[string]string) error {
	c.mu.Lock()
	h := c.host
	c.mu.Unlock()
	switch action {
	case KeyVolume:
		v, err := strconv.Atoi(strings.TrimSpace(values["level"]))
		if err != nil || v < 0 || v > 100 {
			return errors.New("volume is 0 to 100")
		}
		if h != nil {
			if err := h.SetSetting(KeyVolume, strconv.Itoa(v)); err != nil {
				return err //nolint:wrapcheck // the host's words.
			}
		}
		c.mu.Lock()
		c.volume = v
		c.mu.Unlock()
	case KeyMode:
		m := values["mode"]
		if m != ModeOn && m != ModeText && m != ModeOff {
			return errors.New("mode is on, text or off")
		}
		if h != nil {
			if err := h.SetSetting(KeyMode, m); err != nil {
				return err //nolint:wrapcheck // the host's words.
			}
		}
		c.mu.Lock()
		c.mode = m
		c.mu.Unlock()
	default:
		return fmt.Errorf("voice: no control called %q", action)
	}
	c.show()
	return nil
}

// show writes the status line and the page.
func (c *Companion) show() {
	c.mu.Lock()
	h := c.host
	status := fmt.Sprintf("volume %d", c.volume)
	switch {
	case c.mode == ModeText:
		status = "text only"
	case c.mode == ModeOff:
		status = "off"
	case c.fallback != "":
		status = fmt.Sprintf("volume %d · the machine's voice: %s", c.volume, c.fallback)
	}
	page := c.render()
	c.mu.Unlock()
	if h == nil {
		return
	}
	h.Status(status)
	h.Page(page)
}

func (c *Companion) render() string {
	var b strings.Builder
	b.WriteString("<form data-action=\"" + KeyVolume + "\"><label>Volume</label>\n")
	fmt.Fprintf(&b, "  <input type=\"range\" name=\"level\" min=\"0\" max=\"100\" step=\"5\" value=\"%d\"></form>\n", c.volume)
	b.WriteString("<label>Voice</label>\n<select name=\"mode\" data-action=\"" + KeyMode + "\">\n")
	for _, m := range []struct{ value, label string }{{ModeOn, "Spoken"}, {ModeText, "Text only"}, {ModeOff, "Off"}} {
		sel := ""
		if m.value == c.mode {
			sel = " selected"
		}
		fmt.Fprintf(&b, "  <option value=\"%s\"%s>%s</option>\n", m.value, sel, m.label)
	}
	b.WriteString("</select>\n")
	if len(c.lines) > 0 {
		b.WriteString("<h3>Heard</h3>\n<ul>\n")
		for i := len(c.lines) - 1; i >= 0; i-- {
			b.WriteString("  <li>" + html.EscapeString(c.lines[i]) + "</li>\n")
		}
		b.WriteString("</ul>\n")
	}
	return b.String()
}

// output is the speakers as otosink offers them.
type output interface {
	Format() (rate, channels int)
	Play(ctx context.Context, clip otosink.Clip) error
}

// speakers is the oto sink behind the Sink interface, converting each clip to
// the output's format once the output is open.
type speakers struct{ out output }

func (s speakers) Play(ctx context.Context, clip PCM) error {
	if rate, ch := s.out.Format(); rate > 0 {
		clip = converted(clip, rate, ch)
	}
	return s.out.Play(ctx, otosink.Clip{Rate: clip.Rate, Channels: clip.Channels, Data: clip.Data}) //nolint:wrapcheck // the sink's words.
}

var (
	_ clientplugin.Player = (*Companion)(nil)
	_ clientplugin.Actor  = (*Companion)(nil)
	_ clientplugin.Muted  = (*Companion)(nil)
)
