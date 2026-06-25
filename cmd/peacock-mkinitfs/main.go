// peacock-mkinitfs is the standalone CLI that builds the Peacock initramfs
// cpio.gz. It was extracted from the Peacock CLI's internal/mkinitfs package
// so the binary can be shipped via the peacock-ports tree and invoked
// out-of-process by the Peacock build command.
//
// Usage:
//
//	peacock-mkinitfs build \
//	  --device oppo-a16 \
//	  --arch armv7h \
//	  --busybox /path/to/busybox \
//	  --output /tmp/initramfs.cpio.gz
//
// See `peacock-mkinitfs build --help` for the full flag list.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/PeacockProject/peacock-mkinitfs/internal/mkinitfs"
)

// version is overridable at link time via -ldflags "-X main.version=...".
var version = "0.1.0"

func main() {
	root := &cobra.Command{
		Use:   "peacock-mkinitfs",
		Short: "Build the Peacock initramfs cpio.gz",
		Long: `peacock-mkinitfs assembles the cpio.gz initramfs image consumed by
the Peacock distribution. It bundles busybox, the init script template, the
binary /init wrapper, optional splash/refresher binaries, util-linux + lvm2
runtime tooling, and the subparts-mount shell helper.`,
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(newBuildCmd())
	root.AddCommand(newVersionCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "peacock-mkinitfs:", err)
		os.Exit(1)
	}
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the peacock-mkinitfs version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(version)
		},
	}
}

func newBuildCmd() *cobra.Command {
	var (
		output                 string
		busyboxPath            string
		splashPath             string
		refresherPath          string
		resize2fsPath          string
		utilLinuxBuildDir      string
		lvm2BuildDir           string
		initramfsToolsBuildDir string
		assetsDir              string
		initWrapperPath        string
		deviceName             string
		rootLabel              string
		initSystem             string
		architecture           string
		enableS4CameraLED      bool
	)

	cmd := &cobra.Command{
		Use:   "build",
		Short: "Build the initramfs cpio.gz",
		Long: `Build the initramfs cpio.gz at --output for the given device.

The minimum required flags are --busybox, --arch, and --output. All other
sources (util-linux, lvm2, splash, refresher) are optional and degrade
gracefully when absent — matching the in-tree Peacock CLI behavior.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if output == "" {
				return fmt.Errorf("--output is required")
			}
			if busyboxPath == "" {
				return fmt.Errorf("--busybox is required")
			}
			if architecture == "" {
				return fmt.Errorf("--arch is required")
			}

			cfg := mkinitfs.InitConfig{
				InitSystem:             initSystem,
				RootLabel:              rootLabel,
				BusyboxPath:            busyboxPath,
				Resize2fsPath:          resize2fsPath,
				SplashPath:             splashPath,
				RefresherPath:          refresherPath,
				Architecture:           architecture,
				DeviceName:             deviceName,
				EnableS4CameraLED:      enableS4CameraLED,
				UtilLinuxBuildDir:      utilLinuxBuildDir,
				Lvm2BuildDir:           lvm2BuildDir,
				InitramfsToolsBuildDir: initramfsToolsBuildDir,
				AssetsDir:              assetsDir,
				InitWrapperPath:        initWrapperPath,
				LogWriter:              cmd.ErrOrStderr(),
			}
			return mkinitfs.Build(output, cfg)
		},
	}

	cmd.Flags().StringVar(&output, "output", "", "Path to write the cpio.gz initramfs (required)")
	cmd.Flags().StringVar(&busyboxPath, "busybox", "", "Path to the static busybox binary to bundle (required)")
	cmd.Flags().StringVar(&splashPath, "splash", "", "Path to peacock-splash binary (optional)")
	cmd.Flags().StringVar(&refresherPath, "refresher", "", "Path to msm-fb-refresher binary (optional)")
	cmd.Flags().StringVar(&resize2fsPath, "resize2fs", "", "Path to resize2fs binary (optional; autodetected when empty)")
	cmd.Flags().StringVar(&utilLinuxBuildDir, "util-linux", "", "Staged util-linux port build dir (sbin/, bin/, usr/bin/, lib/, usr/lib/)")
	cmd.Flags().StringVar(&lvm2BuildDir, "lvm2", "", "Staged lvm2 port build dir (sbin/dmsetup + libs)")
	cmd.Flags().StringVar(&initramfsToolsBuildDir, "initramfs-tools", "", "Legacy peacock-initramfs-tools port build dir (optional)")
	cmd.Flags().StringVar(&assetsDir, "assets-dir", "", "Override directory for init.sh.in / init-wrapper.go.in / subparts-mount.sh (optional)")
	cmd.Flags().StringVar(&initWrapperPath, "init-wrapper", "", "Prebuilt /init wrapper binary to use instead of compiling with go (optional; arch-checked)")
	cmd.Flags().StringVar(&deviceName, "device", "", "Device codename (e.g. oppo-a16)")
	cmd.Flags().StringVar(&rootLabel, "root-label", "ROOT", "Filesystem label for the root partition")
	cmd.Flags().StringVar(&initSystem, "init", "systemd", "Init system: systemd or openrc")
	cmd.Flags().StringVar(&architecture, "arch", "", "Target architecture (armv7h, aarch64, x86_64) (required)")
	cmd.Flags().BoolVar(&enableS4CameraLED, "s4-camera-led", false, "Enable Samsung S4 camera-LED debug flashes (debug only)")

	_ = cmd.MarkFlagRequired("output")
	_ = cmd.MarkFlagRequired("busybox")
	_ = cmd.MarkFlagRequired("arch")

	return cmd
}
