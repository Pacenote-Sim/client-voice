# client-voice

The client half of the [voice](https://github.com/Pacenote-Sim/voice) server plugin, and the one
plugin in a Pacenote client that makes sound. Every other companion's `Play` and `Say` come here:
audio that arrived with engineer's lines is played; words that came alone are spoken through the
voice server plugin, and when it cannot answer, through the machine's own voice. The volume and the
mode are its settings, on its page. The client itself has none.

## What it does

| | |
|---|---|
| `Play(audio)` | decodes MP3 or 16-bit WAV, scales it to the volume, plays it. A clip still playing when the next arrives is cut: a line for the corner behind the car is worth nothing |
| `Say(words)` | `POST /plugin/voice/speak` with the words, plays what comes back; when the voice plugin has no key or cannot be reached, `say` on macOS, System.Speech on Windows, espeak or spd-say on Linux |
| its page | a volume slider, a mode: spoken, text only, off; the last lines heard |
| its settings | kept through the client, by this plugin, and read back at start |

Sound goes through [oto](https://github.com/ebitengine/oto), which needs no cgo on Windows or
macOS, so a server can build a client with this plugin in it the same way as any other.

### With the sound off

The mode on this page is also an answer the app gives other companions: with it on text or off,
anything handed to `Play` would be heard by nobody. A companion that asks — engineer does, on every
lap it posts — tells its own server half not to buy audio for this client at all. Audio is paid for
by the character at whatever vendor the voice plugin uses, and a driver reading their lines should
not be spending it.

## Building and testing

`make` runs what CI runs. The speakers are behind an interface and every test plays into a recorder;
the oto output itself is heard, not tested. `TESTING.md` has the rest.

`testdata/mpeg2.mp3` is the go-mp3 decoder's example file, Apache-2.0, the one real MP3 the tests
decode.

## Licence

GNU General Public License, version 3 — see `LICENSE` — with the Pacenote Plugin Exception in
`LICENSE-EXCEPTION`, the same one the client carries. It lets this plugin be compiled into a client
alongside plugins under other licences, including closed ones, and lets that client be handed out
under those plugins' own terms while this plugin's part stays GPL. The contract it is built on
(`github.com/pacenote-sim/clientplugin`) is Apache-2.0.
