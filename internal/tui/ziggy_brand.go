//go:build brand

package tui

import _ "embed"

var (
	//go:embed brand/ziggy-open.png
	ziggyOpenPNG []byte
	//go:embed brand/ziggy-blink.png
	ziggyBlinkPNG []byte
)

func init() { ziggyPNGs = [][]byte{ziggyOpenPNG, ziggyBlinkPNG} }
