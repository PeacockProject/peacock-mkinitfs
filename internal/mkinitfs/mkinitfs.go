// Package mkinitfs builds the Peacock initramfs cpio archive.
//
// Originally lived as peacock/internal/mkinitfs inside the Peacock CLI repo
// (PeacockProject/Peacock). Extracted to peacock-mkinitfs so the binary can be
// shipped via the peacock-ports tree and invoked out-of-process by the
// Peacock CLI.
//
// Behavior is intentionally identical to the in-tree version it replaces;
// the only structural change is that the three template/library assets are
// now embedded into the binary via //go:embed (see assets.go) so the
// binary is self-contained without needing the peacock-initramfs-tools port.
package mkinitfs

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/PeacockProject/peacock-mkinitfs/assets"
)

// InitConfig holds configuration for the init script validation.
type InitConfig struct {
	InitSystem        string // "openrc" or "systemd"
	RootLabel         string // Filesystem label for root partition (e.g., "ROOT")
	BusyboxPath       string // Path to static busybox binary
	Resize2fsPath     string // Path to resize2fs binary (optional, will try to find if empty)
	SplashPath        string // Path to peacock-splash binary (optional)
	RefresherPath     string // Path to msm-fb-refresher binary (optional)
	Architecture      string // Target arch (e.g., "armv7h", "aarch64", "x86_64")
	DeviceName        string // Device codename (e.g., "samsung-jflte")
	EnableS4CameraLED bool   // Enable S4-specific camera LED debug flashes in initramfs
	// UtilLinuxBuildDir points at the staged util-linux port build directory
	// (sbin/, bin/, usr/bin/, lib/, usr/lib/). When set, the initramfs builder
	// harvests losetup/partx/blkid/lsblk + shared libs from here. Falls back to
	// a no-op when empty (e.g. legacy callers or partial builds).
	UtilLinuxBuildDir string
	// Lvm2BuildDir points at the staged lvm2 port build directory which
	// provides sbin/dmsetup and libdevmapper. When set, the initramfs builder
	// prefers this over host paths for the dmsetup binary + its lib search.
	Lvm2BuildDir string
	// InitramfsToolsBuildDir is retained for backwards-compatibility with
	// callers that still point at the (now-deleted) peacock-initramfs-tools
	// port. When set, the named directory is checked for
	// usr/lib/peacock/<asset> before falling through to the embedded
	// fallback. New callers should leave this empty.
	InitramfsToolsBuildDir string
	// AssetsDir, when set, is checked for <AssetsDir>/<asset> before the
	// embedded fallback. Lets out-of-tree callers override individual assets
	// without rebuilding the binary. Optional.
	AssetsDir string
	// InitWrapperPath, when set, points at a PREBUILT /init wrapper binary to use
	// verbatim instead of compiling one with `go` (on-device installs have no Go
	// toolchain). Only used if its ELF arch matches Architecture. Falls back to
	// the /usr/lib/peacock/init-wrapper well-known path, then go, then a shell
	// wrapper. Optional.
	InitWrapperPath string
	// LogWriter receives stdout/stderr of subprocesses (go build, find, cpio,
	// gzip). Defaults to os.Stderr when nil.
	LogWriter io.Writer
}

func (c InitConfig) logWriter() io.Writer {
	if c.LogWriter != nil {
		return c.LogWriter
	}
	return os.Stderr
}

// loadAsset returns the bytes for the named initramfs asset, picking the
// first of these sources that resolves:
//
//  1. cfg.InitramfsToolsBuildDir/usr/lib/peacock/<asset>
//     (and the sibling /stage/usr/lib/peacock/<asset>) — legacy
//     peacock-initramfs-tools port layout.
//  2. cfg.AssetsDir/<asset> — explicit caller override.
//  3. The //go:embed fallback baked into the binary (assets.go).
//
// The returned "source" string is a human-readable identifier used only for
// error messages.
func loadAsset(cfg InitConfig, asset string) ([]byte, string, error) {
	candidates := []string{}
	if cfg.InitramfsToolsBuildDir != "" {
		candidates = append(candidates,
			filepath.Join(cfg.InitramfsToolsBuildDir, "usr", "lib", "peacock", asset),
			filepath.Join(cfg.InitramfsToolsBuildDir, "stage", "usr", "lib", "peacock", asset),
		)
	}
	if cfg.AssetsDir != "" {
		candidates = append(candidates, filepath.Join(cfg.AssetsDir, asset))
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			body, err := os.ReadFile(c)
			if err != nil {
				return nil, c, fmt.Errorf("failed to read %s: %w", c, err)
			}
			return body, c, nil
		}
	}
	body, err := assets.Asset(asset)
	if err != nil {
		return nil, "", fmt.Errorf("asset %s not found on disk and no embedded fallback: %w", asset, err)
	}
	return body, "embed:" + asset, nil
}

// GenerateInitScript writes the init script to the target path.
func GenerateInitScript(path string, cfg InitConfig) error {
	body, src, err := loadAsset(cfg, "init.sh.in")
	if err != nil {
		return err
	}
	tmpl, err := template.New("init").Parse(string(body))
	if err != nil {
		return fmt.Errorf("failed to parse init template %s: %w", src, err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, cfg); err != nil {
		return fmt.Errorf("failed to execute init template %s: %w", src, err)
	}

	if err := os.WriteFile(path, buf.Bytes(), 0755); err != nil {
		return fmt.Errorf("failed to write init script: %w", err)
	}
	return nil
}

// initWrapperMachine maps a target arch to its ELF e_machine value, so a
// prebuilt wrapper is only ever used when it actually matches the target.
func initWrapperMachine(arch string) (uint16, bool) {
	switch arch {
	case "aarch64":
		return 0xB7, true
	case "armv7h", "armv7", "arm":
		return 0x28, true
	case "x86_64":
		return 0x3E, true
	}
	return 0, false
}

// prebuiltWrapperOK reports whether path is an ELF for the given arch.
func prebuiltWrapperOK(path, arch string) bool {
	want, ok := initWrapperMachine(arch)
	if !ok {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var hdr [20]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return false
	}
	if hdr[0] != 0x7f || hdr[1] != 'E' || hdr[2] != 'L' || hdr[3] != 'F' {
		return false
	}
	got := uint16(hdr[18]) | uint16(hdr[19])<<8 // e_machine, little-endian
	return got == want
}

func buildInitWrapper(outPath string, cfg InitConfig) error {
	// 1. Prebuilt wrapper shipped as a feather package (e.g. peacock-init-wrapper
	//    in PRP) — copy it verbatim, avoiding a Go toolchain on-device. Only used
	//    when its ELF arch matches the target so a cross-build can't grab the
	//    wrong binary.
	candidates := []string{}
	if cfg.InitWrapperPath != "" {
		candidates = append(candidates, cfg.InitWrapperPath)
	}
	candidates = append(candidates, "/usr/lib/peacock/init-wrapper")
	for _, p := range candidates {
		if !prebuiltWrapperOK(p, cfg.Architecture) {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("failed to read prebuilt init wrapper %s: %w", p, err)
		}
		if err := os.WriteFile(outPath, data, 0o755); err != nil {
			return fmt.Errorf("failed to install prebuilt init wrapper: %w", err)
		}
		return nil
	}

	// 2. On-device installs (PRP recovery) have no Go toolchain. When `go` is
	// unavailable, emit a shell /init wrapper that does the same job (mount
	// devtmpfs, then exec the shell init script). This relies on the booting
	// kernel having CONFIG_BINFMT_SCRIPT (shebang support) — standard on
	// Peacock device kernels. The compiled binary path stays the default for
	// the desktop builder and kernels without BINFMT_SCRIPT.
	if _, lookErr := exec.LookPath("go"); lookErr != nil {
		shell := "#!/bin/busybox ash\n" +
			"# peacock initramfs /init (shell wrapper; emitted when no Go toolchain\n" +
			"# is present, e.g. on-device installs). Needs CONFIG_BINFMT_SCRIPT.\n" +
			"/bin/busybox mount -t devtmpfs devtmpfs /dev 2>/dev/null\n" +
			"echo 'PEACOCK: init wrapper (shell) start' > /dev/kmsg 2>/dev/null\n" +
			"exec /bin/busybox ash /init.sh\n"
		if werr := os.WriteFile(outPath, []byte(shell), 0o755); werr != nil {
			return fmt.Errorf("failed to write shell init wrapper: %w", werr)
		}
		return nil
	}

	arch := cfg.Architecture
	goarch := ""
	goarm := ""
	switch arch {
	case "armv7h":
		goarch = "arm"
		goarm = "7"
	case "armv7":
		goarch = "arm"
		goarm = "7"
	case "aarch64":
		goarch = "arm64"
	case "x86_64":
		goarch = "amd64"
	default:
		return fmt.Errorf("unsupported architecture for init wrapper: %s", arch)
	}

	wrapperSrc, _, err := loadAsset(cfg, "init-wrapper.go.in")
	if err != nil {
		return err
	}

	tmpDir, err := os.MkdirTemp("", "peacock-init-wrapper-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	srcPath := filepath.Join(tmpDir, "main.go")
	if err := os.WriteFile(srcPath, wrapperSrc, 0644); err != nil {
		return fmt.Errorf("failed to write init wrapper source: %w", err)
	}

	cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w", "-o", outPath, srcPath)
	cmd.Stdout = cfg.logWriter()
	cmd.Stderr = cfg.logWriter()
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+goarch)
	if goarm != "" {
		cmd.Env = append(cmd.Env, "GOARM="+goarm)
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to build init wrapper: %w", err)
	}
	return nil
}

func findFirstExisting(paths []string) string {
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func appendUniquePath(paths []string, p string) []string {
	if p == "" {
		return paths
	}
	clean := filepath.Clean(p)
	for _, existing := range paths {
		if existing == clean {
			return paths
		}
	}
	return append(paths, clean)
}

// runtimeVendorCandidates returns directories whose sbin/, bin/, usr/bin/,
// lib/, usr/lib/ trees are copied verbatim into the initramfs.
func runtimeVendorCandidates(utilLinuxBuildDir string) []string {
	var out []string
	if utilLinuxBuildDir != "" {
		out = appendUniquePath(out, utilLinuxBuildDir)
		out = appendUniquePath(out, filepath.Join(utilLinuxBuildDir, "stage"))
	}
	return out
}

// runtimeStageCandidates is reserved for a future initramfs-stage port that
// emits additional rootfs payload. Returns nothing today.
func runtimeStageCandidates(deviceName string) []string {
	_ = deviceName
	return nil
}

func copyFileOrSymlink(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		_ = os.RemoveAll(dst)
		return os.Symlink(target, dst)
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	content, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	mode := info.Mode() & 0o777
	if mode == 0 {
		mode = 0o644
	}
	return os.WriteFile(dst, content, mode)
}

// initramfsUtilLinuxTools is the whitelist of util-linux binaries the initramfs
// actually invokes (disk/partition probing). util-linux ships ~100 tools and
// the full bin/sbin tree blows past lk2nd's 16 MiB ramdisk cap, so we copy only
// these (dynamic builds — the init scripts call the plain names; mount/umount/dd
// come from busybox).
var initramfsUtilLinuxTools = map[string]bool{
	"losetup": true, "blkid": true, "partx": true, "lsblk": true,
	"fdisk": true, "sfdisk": true, "blockdev": true, "findfs": true,
	"wipefs": true, "hexdump": true, "blkdiscard": true, "partprobe": true,
}

// keepInitramfsBin keeps only whitelisted util-linux tools, dropping the rest
// and the redundant `.static` duplicates (the scripts use the dynamic names).
func keepInitramfsBin(rel string, info os.FileInfo) bool {
	if info.IsDir() {
		return true
	}
	base := filepath.Base(rel)
	if strings.HasSuffix(base, ".static") {
		return false
	}
	return initramfsUtilLinuxTools[base]
}

// keepInitramfsLib drops static build libraries (.a) and libtool archives (.la),
// which are never needed at runtime, while keeping the shared libs (.so*).
func keepInitramfsLib(rel string, info os.FileInfo) bool {
	if info.IsDir() {
		return true
	}
	return !strings.HasSuffix(rel, ".a") && !strings.HasSuffix(rel, ".la")
}

func copyTree(srcRoot, dstRoot string) error {
	return copyTreeFiltered(srcRoot, dstRoot, nil)
}

// copyTreeFiltered copies srcRoot into dstRoot. When keep is non-nil, only
// entries for which keep(rel, info) returns true are copied (directories are
// still created so kept files land in the right place).
func copyTreeFiltered(srcRoot, dstRoot string, keep func(rel string, info os.FileInfo) bool) error {
	srcInfo, err := os.Stat(srcRoot)
	if err != nil {
		return err
	}
	if !srcInfo.IsDir() {
		return fmt.Errorf("source is not a directory: %s", srcRoot)
	}
	if err := os.MkdirAll(dstRoot, 0755); err != nil {
		return err
	}

	return filepath.Walk(srcRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if keep != nil && !keep(rel, info) {
			return nil
		}

		dst := filepath.Join(dstRoot, rel)
		if info.IsDir() {
			return os.MkdirAll(dst, info.Mode()&0o777)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return err
		}
		return copyFileOrSymlink(path, dst)
	})
}

// Build creates the initramfs cpio.gz at `output` from the configuration in
// `cfg`. Existing files at `output` are overwritten.
func Build(output string, cfg InitConfig) error {
	fmt.Fprintf(cfg.logWriter(), "Generating init script for %s...\n", cfg.InitSystem)

	tmpDir, err := os.MkdirTemp("", "peacock-initramfs-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	for _, dir := range []string{"proc", "sys", "dev", "run", "tmp", "etc", "usr", "lib"} {
		if err := os.MkdirAll(filepath.Join(tmpDir, dir), 0755); err != nil {
			return fmt.Errorf("failed to create initramfs dir %s: %w", dir, err)
		}
	}

	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return err
	}

	bbDest := filepath.Join(binDir, "busybox")
	input, err := os.ReadFile(cfg.BusyboxPath)
	if err != nil {
		return fmt.Errorf("failed to read busybox binary: %w", err)
	}
	if err := os.WriteFile(bbDest, input, 0755); err != nil {
		return fmt.Errorf("failed to write busybox binary: %w", err)
	}

	commonApplets := []string{
		"sh", "ash", "mount", "umount", "mknod", "mkdir", "rmdir",
		"cat", "ls", "cp", "mv", "rm", "ln", "chmod", "chown",
		"echo", "printf", "test", "[", "sleep", "usleep",
		"grep", "sed", "awk", "cut", "sort", "uniq", "head", "tail",
		"find", "xargs", "tar", "gzip", "gunzip", "cpio",
		"dd", "sync", "switch_root", "reboot", "poweroff", "halt",
		"true", "false", "date", "touch", "stat", "df", "du",
		"ps", "kill", "killall", "pidof", "top",
	}
	for _, applet := range commonApplets {
		symlinkPath := filepath.Join(binDir, applet)
		if err := os.Symlink("busybox", symlinkPath); err != nil {
			fmt.Fprintf(cfg.logWriter(), "Warning: failed to create symlink %s: %v\n", applet, err)
		}
	}

	sbinDir := filepath.Join(tmpDir, "sbin")
	if err := os.MkdirAll(sbinDir, 0755); err != nil {
		return err
	}

	resize2fsPath := cfg.Resize2fsPath
	if resize2fsPath == "" {
		for _, path := range []string{"/usr/sbin/resize2fs", "/sbin/resize2fs", "/usr/bin/resize2fs"} {
			if _, err := os.Stat(path); err == nil {
				resize2fsPath = path
				break
			}
		}
	}
	if resize2fsPath != "" {
		resize2fsDest := filepath.Join(sbinDir, "resize2fs")
		resize2fsInput, err := os.ReadFile(resize2fsPath)
		if err != nil {
			return fmt.Errorf("failed to read resize2fs binary: %w", err)
		}
		if err := os.WriteFile(resize2fsDest, resize2fsInput, 0755); err != nil {
			return fmt.Errorf("failed to write resize2fs binary: %w", err)
		}
	} else {
		fmt.Fprintf(cfg.logWriter(), "Warning: resize2fs not found, rootfs resize will be skipped\n")
	}

	runtimeRoot := findFirstExisting(runtimeVendorCandidates(cfg.UtilLinuxBuildDir))
	stageRoots := runtimeStageCandidates(cfg.DeviceName)
	if runtimeRoot != "" {
		type runtimeCopy struct {
			srcRel string
			dstRel string
			keep   func(rel string, info os.FileInfo) bool
		}
		// Binary trees are whitelisted to the handful of util-linux tools the
		// initramfs uses; lib trees drop static .a/.la build artifacts. Without
		// this the full util-linux tree (~100 tools + static libs + .static
		// duplicates) overflows lk2nd's 16 MiB ramdisk cap.
		for _, item := range []runtimeCopy{
			{srcRel: "sbin", dstRel: "sbin", keep: keepInitramfsBin},
			{srcRel: "bin", dstRel: "bin", keep: keepInitramfsBin},
			{srcRel: filepath.Join("usr", "bin"), dstRel: filepath.Join("usr", "bin"), keep: keepInitramfsBin},
			{srcRel: "lib", dstRel: "lib", keep: keepInitramfsLib},
			{srcRel: filepath.Join("usr", "lib"), dstRel: filepath.Join("usr", "lib"), keep: keepInitramfsLib},
		} {
			srcDir := filepath.Join(runtimeRoot, item.srcRel)
			if _, err := os.Stat(srcDir); err != nil {
				continue
			}
			dstDir := filepath.Join(tmpDir, item.dstRel)
			if err := copyTreeFiltered(srcDir, dstDir, item.keep); err != nil {
				return fmt.Errorf("failed to copy runtime tree %s -> %s: %w", srcDir, dstDir, err)
			}
		}
	}

	dmsetupCandidates := []string{}
	if cfg.Lvm2BuildDir != "" {
		dmsetupCandidates = append(dmsetupCandidates,
			filepath.Join(cfg.Lvm2BuildDir, "sbin", "dmsetup"),
			filepath.Join(cfg.Lvm2BuildDir, "stage", "sbin", "dmsetup"),
		)
	}
	if runtimeRoot != "" {
		dmsetupCandidates = append(dmsetupCandidates, filepath.Join(runtimeRoot, "sbin", "dmsetup"))
	}
	for _, stage := range stageRoots {
		dmsetupCandidates = append(dmsetupCandidates, filepath.Join(stage, "sbin", "dmsetup"))
	}
	dmsetupCandidates = append(dmsetupCandidates,
		"/sbin/dmsetup",
		"/usr/sbin/dmsetup",
		"/usr/bin/dmsetup",
		"/bin/dmsetup",
	)
	dmsetupPath := findFirstExisting(dmsetupCandidates)
	if dmsetupPath != "" {
		dmsetupDest := filepath.Join(sbinDir, "dmsetup")
		if _, err := os.Stat(dmsetupDest); err != nil {
			dmsetupInput, err := os.ReadFile(dmsetupPath)
			if err != nil {
				return fmt.Errorf("failed to read dmsetup binary: %w", err)
			}
			if err := os.WriteFile(dmsetupDest, dmsetupInput, 0755); err != nil {
				return fmt.Errorf("failed to write dmsetup binary: %w", err)
			}
		}

		libDir := filepath.Join(tmpDir, "lib")
		if err := os.MkdirAll(libDir, 0755); err != nil {
			return err
		}

		libSearchDirs := []string{}
		if cfg.Lvm2BuildDir != "" {
			libSearchDirs = appendUniquePath(libSearchDirs, filepath.Join(cfg.Lvm2BuildDir, "lib"))
			libSearchDirs = appendUniquePath(libSearchDirs, filepath.Join(cfg.Lvm2BuildDir, "usr", "lib"))
			libSearchDirs = appendUniquePath(libSearchDirs, filepath.Join(cfg.Lvm2BuildDir, "stage", "lib"))
			libSearchDirs = appendUniquePath(libSearchDirs, filepath.Join(cfg.Lvm2BuildDir, "stage", "usr", "lib"))
		}
		if runtimeRoot != "" {
			libSearchDirs = appendUniquePath(libSearchDirs, filepath.Join(runtimeRoot, "lib"))
			libSearchDirs = appendUniquePath(libSearchDirs, filepath.Join(runtimeRoot, "usr", "lib"))
		}
		for _, stage := range stageRoots {
			libSearchDirs = appendUniquePath(libSearchDirs, filepath.Join(stage, "lib"))
			libSearchDirs = appendUniquePath(libSearchDirs, filepath.Join(stage, "usr", "lib"))
		}
		libSearchDirs = appendUniquePath(libSearchDirs, "/lib")
		libSearchDirs = appendUniquePath(libSearchDirs, "/usr/lib")
		libSearchDirs = appendUniquePath(libSearchDirs, "/lib/arm-linux-gnueabihf")
		libSearchDirs = appendUniquePath(libSearchDirs, "/usr/lib/arm-linux-gnueabihf")
		requiredLibs := []string{
			"ld-linux-armhf.so.3",
			"libdevmapper.so.1.02",
			"libfdisk.so.1",
			"libfdisk.so.1.1.0",
			"libblkid.so.1",
			"libblkid.so.1.1.0",
			"libsmartcols.so.1",
			"libsmartcols.so.1.1.0",
			"libuuid.so.1",
			"libuuid.so.1.3.0",
			"libm.so.6",
			"libgcc_s.so.1",
			"libc.so.6",
		}
		for _, libName := range requiredLibs {
			var src string
			for _, d := range libSearchDirs {
				candidate := filepath.Join(d, libName)
				if _, err := os.Stat(candidate); err == nil {
					src = candidate
					break
				}
			}
			if src == "" {
				continue
			}
			dst := filepath.Join(libDir, libName)
			if _, err := os.Stat(dst); err == nil {
				continue
			}
			libInput, err := os.ReadFile(src)
			if err != nil {
				return fmt.Errorf("failed to read %s: %w", src, err)
			}
			if err := os.WriteFile(dst, libInput, 0755); err != nil {
				return fmt.Errorf("failed to write %s: %w", libName, err)
			}
		}
	} else {
		fmt.Fprintf(cfg.logWriter(), "Warning: dmsetup not found, PRP-style dm-linear root probing will be unavailable\n")
	}

	if cfg.SplashPath != "" {
		splashDest := filepath.Join(binDir, "peacock-splash")
		splashInput, err := os.ReadFile(cfg.SplashPath)
		if err != nil {
			return fmt.Errorf("failed to read peacock-splash binary: %w", err)
		}
		if err := os.WriteFile(splashDest, splashInput, 0755); err != nil {
			return fmt.Errorf("failed to write peacock-splash binary: %w", err)
		}
	}

	// Optional handoff flare image used by initramfs right before root handover.
	conspiracySrc := findFirstExisting([]string{
		filepath.Join("conspiracy.png"),
		filepath.Join("assets", "conspiracy.png"),
		filepath.Join("prp", "assets", "conspiracy.png"),
	})
	if conspiracySrc != "" {
		conspiracyDir := filepath.Join(tmpDir, "etc", "peacock")
		if err := os.MkdirAll(conspiracyDir, 0755); err != nil {
			return fmt.Errorf("failed to create conspiracy image dir: %w", err)
		}
		conspiracyInput, err := os.ReadFile(conspiracySrc)
		if err != nil {
			return fmt.Errorf("failed to read conspiracy image: %w", err)
		}
		conspiracyDst := filepath.Join(conspiracyDir, "conspiracy.png")
		if err := os.WriteFile(conspiracyDst, conspiracyInput, 0644); err != nil {
			return fmt.Errorf("failed to write conspiracy image: %w", err)
		}
	}

	// Install subparts-mount.sh helper into /usr/lib/peacock/.
	subpartsContent, _, err := loadAsset(cfg, "subparts-mount.sh")
	if err != nil {
		fmt.Fprintf(cfg.logWriter(), "Warning: %v; the initramfs sub-partition fallback will be unavailable\n", err)
	} else {
		subpartsDir := filepath.Join(tmpDir, "usr", "lib", "peacock")
		if err := os.MkdirAll(subpartsDir, 0755); err != nil {
			return fmt.Errorf("failed to create subparts-mount dir: %w", err)
		}
		subpartsDst := filepath.Join(subpartsDir, "subparts-mount.sh")
		if err := os.WriteFile(subpartsDst, subpartsContent, 0755); err != nil {
			return fmt.Errorf("failed to write subparts-mount.sh: %w", err)
		}
	}

	if cfg.RefresherPath != "" {
		refresherDest := filepath.Join(binDir, "msm-fb-refresher")
		refresherInput, err := os.ReadFile(cfg.RefresherPath)
		if err != nil {
			return fmt.Errorf("failed to read msm-fb-refresher binary: %w", err)
		}
		if err := os.WriteFile(refresherDest, refresherInput, 0755); err != nil {
			return fmt.Errorf("failed to write msm-fb-refresher binary: %w", err)
		}
	}

	initScriptPath := filepath.Join(tmpDir, "init.sh")
	if err := GenerateInitScript(initScriptPath, cfg); err != nil {
		return err
	}

	initPath := filepath.Join(tmpDir, "init")
	if err := buildInitWrapper(initPath, cfg); err != nil {
		return err
	}

	// Pipe: find . | cpio -o -H newc | gzip -9 > output
	findCmd := exec.Command("find", ".")
	findCmd.Dir = tmpDir
	findCmd.Stderr = cfg.logWriter()

	cpioCmd := exec.Command("cpio", "-o", "-H", "newc")
	cpioCmd.Dir = tmpDir
	cpioCmd.Stderr = cfg.logWriter()

	gzipCmd := exec.Command("gzip", "-9")
	gzipCmd.Stderr = cfg.logWriter()

	cpioCmd.Stdin, _ = findCmd.StdoutPipe()
	gzipCmd.Stdin, _ = cpioCmd.StdoutPipe()

	outFile, err := os.Create(output)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer outFile.Close()
	gzipCmd.Stdout = outFile

	if err := gzipCmd.Start(); err != nil {
		return fmt.Errorf("failed to start gzip: %w", err)
	}
	if err := cpioCmd.Start(); err != nil {
		_ = gzipCmd.Wait()
		return fmt.Errorf("failed to start cpio: %w", err)
	}
	if err := findCmd.Start(); err != nil {
		_ = cpioCmd.Wait()
		_ = gzipCmd.Wait()
		return fmt.Errorf("failed to start find: %w", err)
	}

	if err := findCmd.Wait(); err != nil {
		_ = cpioCmd.Wait()
		_ = gzipCmd.Wait()
		return fmt.Errorf("find failed: %w", err)
	}
	if err := cpioCmd.Wait(); err != nil {
		_ = gzipCmd.Wait()
		return fmt.Errorf("cpio failed: %w", err)
	}
	if err := gzipCmd.Wait(); err != nil {
		return fmt.Errorf("gzip failed: %w", err)
	}

	return nil
}
