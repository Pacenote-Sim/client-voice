module github.com/pacenote-sim/client-voice

go 1.26.1

// The contract and the protocol are required by version: a checkout of this
// repository alone builds against the tagged releases.

require (
	github.com/ebitengine/oto/v3 v3.5.0
	github.com/hajimehoshi/go-mp3 v0.3.4
	github.com/pacenote-sim/clientplugin v0.1.0
	github.com/stretchr/testify v1.12.1
)

require (
	github.com/ebitengine/purego v0.11.0 // indirect
	github.com/jfreymuth/pulse v0.1.3 // indirect
	github.com/pacenote-sim/protocol v0.2.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/sys v0.47.0 // indirect
)
