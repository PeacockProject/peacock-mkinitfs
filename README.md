# peacock-mkinitfs

Standalone CLI that builds the Peacock initramfs `cpio.gz`.

This binary used to live as the `internal/mkinitfs` package inside
[PeacockProject/Peacock](https://github.com/PeacockProject/Peacock). It was
extracted so the initramfs build path is a real shell-level tool that can
be installed via the `peacock-ports` tree and shipped independently of the
Peacock CLI's release cadence.

The Peacock CLI's `build` command invokes `peacock-mkinitfs build ...` via
`exec.Command`; nothing imports this code in-process.

## What it does

Given a static busybox binary, a target architecture, and optional staged
build trees for `util-linux` / `lvm2`, `peacock-mkinitfs build` produces a
`cpio.gz` containing:

- `/bin/busybox` plus the usual applet symlinks.
- `/sbin/resize2fs` (host fallback if `--resize2fs` is empty).
- `/sbin/dmsetup` and the required `libdevmapper` / `libblkid` / `libuuid`
  shared libraries, harvested from `--lvm2`.
- `losetup`, `partx`, `blkid`, `lsblk`, `mount`, ... copied verbatim from
  `--util-linux/{sbin,bin,usr/bin,lib,usr/lib}`.
- `/bin/peacock-splash` and `/bin/msm-fb-refresher` when the corresponding
  flags are provided.
- `/etc/peacock/conspiracy.png` when one of the well-known asset paths is
  present in the working directory.
- `/usr/lib/peacock/subparts-mount.sh` (shell helper sourced by the
  generated init script).
- `/init.sh` — the templated init script.
- `/init` — a tiny statically linked Go binary that execs `/init.sh`,
  needed by kernels built without `BINFMT_SCRIPT`.

The three template/library assets (`init.sh.in`, `init-wrapper.go.in`,
`subparts-mount.sh`) are embedded into the binary via `//go:embed` so
`peacock-mkinitfs` is self-contained and works in any working directory
without a peacock-ports install. Callers can still override individual
assets at runtime via `--assets-dir` (or, for legacy callers, the staged
port via `--initramfs-tools`).

## Usage

```sh
peacock-mkinitfs build \
  --device oppo-a16 \
  --arch armv7h \
  --init openrc \
  --busybox /path/to/build/busybox-1.36/busybox \
  --util-linux /path/to/build/util-linux \
  --lvm2 /path/to/build/lvm2 \
  --splash /path/to/build/peacock-splash/usr/bin/peacock-splash \
  --output /tmp/initramfs.cpio.gz
```

Run `peacock-mkinitfs build --help` for the full flag list.

## Build / install

```sh
make build
sudo make install PREFIX=/usr
```

This drops the binary at `$PREFIX/bin/peacock-mkinitfs`. The embedded
assets ship inside the binary; there is no separate asset install step.

For the in-tree port build (peacock-ports `base/peacock-mkinitfs`), the
build script effectively runs `go build -o stage/usr/bin/peacock-mkinitfs
./cmd/peacock-mkinitfs`, which matches what `make build` does.

## Source

- Repo: <https://github.com/PeacockProject/peacock-mkinitfs>
- Original code path before extraction:
  `PeacockProject/Peacock/internal/mkinitfs/` (now deleted).

## License

Same as Peacock — see the upstream Peacock repository for licensing terms;
no separate `LICENSE` file ships in this repo (the parent project does not
carry one).
