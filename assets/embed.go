// Package assets carries the canonical initramfs template/library assets
// (init.sh.in, init-wrapper.go.in, subparts-mount.sh) baked into the
// peacock-mkinitfs binary via //go:embed. Keeping the .go file inside this
// directory is necessary because //go:embed paths are resolved relative to
// the Go source file and may not climb above it.
package assets

import (
	"embed"
	"fmt"
)

// FS holds the embedded asset tree, rooted at initramfs/.
//
//go:embed all:initramfs
var FS embed.FS

// Asset returns the bytes for a named asset under initramfs/.
func Asset(name string) ([]byte, error) {
	data, err := FS.ReadFile("initramfs/" + name)
	if err != nil {
		return nil, fmt.Errorf("embedded asset %q not found: %w", name, err)
	}
	return data, nil
}
