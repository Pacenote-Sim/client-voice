package voice

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// ErrNoSystemVoice reports a machine with no voice of its own that this
// plugin knows how to ask.
var ErrNoSystemVoice = errors.New("voice: this machine has no voice this plugin can use")

// systemSpeak speaks with the operating system's own voice: `say` on macOS,
// System.Speech through PowerShell on Windows, espeak or spd-say on Linux
// when one is installed. It is the fallback for words that came without
// audio when the voice server plugin cannot answer.
func systemSpeak(ctx context.Context, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "say", text) //nolint:gosec // G204: the words are an argument, never a shell line.
	case "windows":
		script := "Add-Type -AssemblyName System.Speech; (New-Object System.Speech.Synthesis.SpeechSynthesizer).Speak(" + psQuote(text) + ")"
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script) //nolint:gosec // G204: the words are quoted into one PowerShell string.
	default:
		for _, name := range []string{"spd-say", "espeak", "espeak-ng"} {
			if path, err := exec.LookPath(name); err == nil {
				cmd = exec.CommandContext(ctx, path, text) //nolint:gosec // G204: the words are an argument, never a shell line.
				break
			}
		}
	}
	if cmd == nil {
		return ErrNoSystemVoice
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("voice: the system voice failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// psQuote is text as a single-quoted PowerShell string.
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
