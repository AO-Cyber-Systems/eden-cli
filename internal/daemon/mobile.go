package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// simctlDeviceList is the JSON structure returned by `xcrun simctl list devices -j`.
type simctlDeviceList struct {
	Devices map[string][]simctlDevice `json:"devices"`
}

// simctlDevice represents a single iOS simulator device.
type simctlDevice struct {
	Name      string `json:"name"`
	UDID      string `json:"udid"`
	State     string `json:"state"`
	IsAvail   bool   `json:"isAvailable"`
	Runtime   string `json:"-"` // populated during parsing
}

// TrustSimulatorCA installs the Eden Dev CA certificate into booted iOS simulators.
// It tries `xcrun simctl keychain add-root-cert` first (Xcode 14+, silent install).
// If that fails, it falls back to opening the cert in simulator Safari for manual install.
func TrustSimulatorCA(caDir string) error {
	certPath := filepath.Join(caDir, caCertFile)
	if !fileExists(certPath) {
		return fmt.Errorf("CA certificate not found at %s\nRun 'eden dev setup' first", certPath)
	}

	// Check xcrun is available.
	if _, err := exec.LookPath("xcrun"); err != nil {
		return fmt.Errorf("xcrun not found. Install Xcode Command Line Tools:\n  xcode-select --install")
	}

	// Find booted simulators.
	booted, available, err := listSimulators()
	if err != nil {
		return fmt.Errorf("listing simulators: %w", err)
	}

	if len(booted) == 0 {
		msg := "No booted iOS simulators found.\n\n"
		if len(available) > 0 {
			msg += "Available simulators:\n"
			for _, d := range available {
				msg += fmt.Sprintf("  %s (%s)\n", d.Name, d.UDID)
			}
			msg += "\nBoot one with: xcrun simctl boot <UDID>"
		} else {
			msg += "No simulators available. Create one in Xcode > Window > Devices and Simulators."
		}
		return fmt.Errorf("%s", msg)
	}

	// Try keychain approach first (Xcode 14+, silent install).
	for _, device := range booted {
		fmt.Printf("  Installing CA cert in simulator: %s (%s)\n", device.Name, device.UDID)

		keychainCmd := exec.Command("xcrun", "simctl", "keychain", device.UDID, "add-root-cert", certPath)
		keychainCmd.Stdout = os.Stdout
		keychainCmd.Stderr = os.Stderr

		if err := keychainCmd.Run(); err == nil {
			fmt.Printf("  CA certificate installed and trusted in %s (via simctl keychain)\n", device.Name)
			continue
		}

		// Fallback: open cert URL in simulator Safari for manual install.
		fmt.Printf("  simctl keychain not available (requires Xcode 14+), falling back to Safari...\n")

		openURL := "file://" + certPath
		openCmd := exec.Command("xcrun", "simctl", "openurl", device.UDID, openURL)
		openCmd.Stdout = os.Stdout
		openCmd.Stderr = os.Stderr

		if err := openCmd.Run(); err != nil {
			return fmt.Errorf("opening cert in simulator Safari: %w", err)
		}

		fmt.Printf("\n  CA certificate opened in simulator Safari for %s.\n", device.Name)
		fmt.Println("  Complete the installation manually:")
		fmt.Println("    1. Tap 'Allow' when prompted to install the profile")
		fmt.Println("    2. Go to Settings > General > VPN & Device Management")
		fmt.Println("    3. Tap the 'Eden Dev CA' profile and install it")
		fmt.Println("    4. Go to Settings > General > About > Certificate Trust Settings")
		fmt.Println("    5. Enable full trust for 'Eden Dev CA'")
	}

	return nil
}

// listSimulators returns booted and available (non-booted) iOS simulators.
func listSimulators() (booted, available []simctlDevice, err error) {
	out, err := exec.Command("xcrun", "simctl", "list", "devices", "-j").Output()
	if err != nil {
		return nil, nil, fmt.Errorf("running simctl list: %w", err)
	}

	var deviceList simctlDeviceList
	if err := json.Unmarshal(out, &deviceList); err != nil {
		return nil, nil, fmt.Errorf("parsing simctl output: %w", err)
	}

	for runtime, devices := range deviceList.Devices {
		for _, d := range devices {
			if !d.IsAvail {
				continue
			}
			d.Runtime = runtime
			if d.State == "Booted" {
				booted = append(booted, d)
			} else {
				available = append(available, d)
			}
		}
	}

	return booted, available, nil
}

// SetupAndroidReverse sets up adb reverse port forwarding for the given ports,
// allowing an Android emulator to reach host services on localhost:<port>.
func SetupAndroidReverse(ports []int) error {
	adbPath, err := exec.LookPath("adb")
	if err != nil {
		return fmt.Errorf("adb not found in PATH.\n" +
			"  Install Android Studio or set ANDROID_HOME:\n" +
			"    https://developer.android.com/studio\n" +
			"  Or install platform-tools:\n" +
			"    brew install --cask android-platform-tools")
	}

	// Verify a device/emulator is connected.
	devicesOut, err := exec.Command(adbPath, "devices").Output()
	if err != nil {
		return fmt.Errorf("running adb devices: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(devicesOut)), "\n")
	hasDevice := false
	for _, line := range lines[1:] { // skip header
		if strings.Contains(line, "device") || strings.Contains(line, "emulator") {
			hasDevice = true
			break
		}
	}
	if !hasDevice {
		return fmt.Errorf("no Android device/emulator connected.\n" +
			"  Start an emulator from Android Studio or connect a device via USB.\n" +
			"  Check with: adb devices")
	}

	for _, port := range ports {
		portStr := strconv.Itoa(port)
		spec := "tcp:" + portStr
		cmd := exec.Command(adbPath, "reverse", spec, spec)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("adb reverse %s %s failed: %w", spec, spec, err)
		}
		fmt.Printf("  Forwarding emulator localhost:%s -> host localhost:%s\n", portStr, portStr)
	}

	fmt.Println("\n  Port forwarding active. Android emulator can reach host services on the forwarded ports.")
	fmt.Println("  For HTTPS with the Eden Dev CA, ensure your app trusts user CAs (see: eden dev android-reverse --help)")

	return nil
}

// ListAndroidForwards returns the currently active adb reverse port forwarding rules.
func ListAndroidForwards() ([]string, error) {
	adbPath, err := exec.LookPath("adb")
	if err != nil {
		return nil, fmt.Errorf("adb not found in PATH")
	}

	out, err := exec.Command(adbPath, "reverse", "--list").Output()
	if err != nil {
		return nil, fmt.Errorf("adb reverse --list failed: %w", err)
	}

	text := strings.TrimSpace(string(out))
	if text == "" {
		return nil, nil
	}

	return strings.Split(text, "\n"), nil
}

// PrintAndroidSecurityConfig prints the XML snippet needed to trust user-installed
// CA certificates in an Android app (required for API 24+).
func PrintAndroidSecurityConfig() {
	fmt.Println(`
  To trust the Eden Dev CA in your Android app, add these files:

  res/xml/network_security_config.xml:
  ┌──────────────────────────────────────────────────────────────┐
  │ <?xml version="1.0" encoding="utf-8"?>                      │
  │ <network-security-config>                                    │
  │     <debug-overrides>                                        │
  │         <trust-anchors>                                      │
  │             <certificates src="user" />                      │
  │             <certificates src="system" />                    │
  │         </trust-anchors>                                     │
  │     </debug-overrides>                                       │
  │ </network-security-config>                                   │
  └──────────────────────────────────────────────────────────────┘

  AndroidManifest.xml (in the <application> tag):
  ┌──────────────────────────────────────────────────────────────┐
  │ android:networkSecurityConfig="@xml/network_security_config" │
  └──────────────────────────────────────────────────────────────┘`)
}
