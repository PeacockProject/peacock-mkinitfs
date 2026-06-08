# peacock-mkinitfs — Makefile
#
# Targets:
#   build      Build the binary into ./peacock-mkinitfs.
#   clean      Remove build artifacts.
#   install    Install the binary into $(PREFIX)/bin (default /usr).
#              Embedded assets ship inside the binary; no separate assets
#              install step is needed.

PREFIX  ?= /usr
BINDIR  ?= $(PREFIX)/bin

GO      ?= go
GOFLAGS ?= -trimpath
LDFLAGS ?= -s -w

BINARY  := peacock-mkinitfs
PKG     := ./cmd/peacock-mkinitfs

.PHONY: build clean install

build:
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

clean:
	rm -f $(BINARY)

install: build
	install -d "$(DESTDIR)$(BINDIR)"
	install -m 0755 $(BINARY) "$(DESTDIR)$(BINDIR)/$(BINARY)"
