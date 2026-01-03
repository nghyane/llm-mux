package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"
)

var installCmd = &cobra.Command{
	Use:   "install",
	Short: "Install background service",
	RunE: func(cmd *cobra.Command, args []string) error {
		exe, err := os.Executable()
		if err != nil {
			return err
		}

		exePath, err := filepath.EvalSymlinks(exe)
		if err != nil {
			exePath = exe
		}

		switch runtime.GOOS {
		case "darwin":
			return installMacOS(exePath)
		case "linux":
			return installLinux(exePath)
		default:
			return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
		}
	},
}

var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Uninstall background service",
	RunE: func(cmd *cobra.Command, args []string) error {
		switch runtime.GOOS {
		case "darwin":
			return uninstallMacOS()
		case "linux":
			return uninstallLinux()
		default:
			return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
		}
	},
}

func init() {
	ServiceCmd.AddCommand(installCmd)
	ServiceCmd.AddCommand(uninstallCmd)
}

// macOS implementation
func installMacOS(exePath string) error {
	home, _ := os.UserHomeDir()
	plistPath := filepath.Join(home, "Library/LaunchAgents/com.llm-mux.plist")
	logDir := filepath.Join(home, ".local/var/log")

	if err := os.MkdirAll(logDir, 0755); err != nil {
		return err
	}

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.llm-mux</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <dict>
        <key>SuccessfulExit</key>
        <false/>
    </dict>
    <key>ThrottleInterval</key>
    <integer>5</integer>
    <key>StandardOutPath</key>
    <string>%s/llm-mux.log</string>
    <key>StandardErrorPath</key>
    <string>%s/llm-mux.log</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
        <key>HOME</key>
        <string>%s</string>
    </dict>
    <key>WorkingDirectory</key>
    <string>%s</string>
</dict>
</plist>`, exePath, logDir, logDir, home, home)

	if err := os.WriteFile(plistPath, []byte(plist), 0644); err != nil {
		return fmt.Errorf("failed to write plist: %w", err)
	}

	fmt.Printf("Service installed to %s\n", plistPath)

	// Load service
	exec.Command("launchctl", "unload", plistPath).Run() // Ignore error
	if err := exec.Command("launchctl", "load", plistPath).Run(); err != nil {
		fmt.Printf("Warning: failed to load service: %v\n", err)
		fmt.Printf("Try running: launchctl load %s\n", plistPath)
	} else {
		fmt.Println("Service started")
	}

	return nil
}

func uninstallMacOS() error {
	home, _ := os.UserHomeDir()
	plistPath := filepath.Join(home, "Library/LaunchAgents/com.llm-mux.plist")

	exec.Command("launchctl", "unload", plistPath).Run()

	if err := os.Remove(plistPath); err != nil {
		return fmt.Errorf("failed to remove plist: %w", err)
	}

	fmt.Println("Service uninstalled")
	return nil
}

// Linux implementation
func installLinux(exePath string) error {
	home, _ := os.UserHomeDir()
	serviceDir := filepath.Join(home, ".config/systemd/user")
	servicePath := filepath.Join(serviceDir, "llm-mux.service")

	if err := os.MkdirAll(serviceDir, 0755); err != nil {
		return err
	}

	unit := fmt.Sprintf(`[Unit]
Description=llm-mux - Multi-provider LLM gateway
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s
WorkingDirectory=%s
Restart=on-failure
RestartSec=5
StartLimitBurst=3
StartLimitIntervalSec=60
Environment=HOME=%s
Environment=PATH=/usr/local/bin:/usr/bin:/bin

[Install]
WantedBy=default.target
`, exePath, home, home)

	if err := os.WriteFile(servicePath, []byte(unit), 0644); err != nil {
		return fmt.Errorf("failed to write unit file: %w", err)
	}

	fmt.Printf("Service installed to %s\n", servicePath)

	// Reload and enable
	exec.Command("systemctl", "--user", "daemon-reload").Run()
	exec.Command("systemctl", "--user", "enable", "llm-mux").Run()
	if err := exec.Command("systemctl", "--user", "start", "llm-mux").Run(); err != nil {
		fmt.Printf("Warning: failed to start service: %v\n", err)
	} else {
		fmt.Println("Service started")
	}

	// Enable lingering
	exec.Command("loginctl", "enable-linger", os.Getenv("USER")).Run()

	return nil
}

func uninstallLinux() error {
	exec.Command("systemctl", "--user", "stop", "llm-mux").Run()
	exec.Command("systemctl", "--user", "disable", "llm-mux").Run()

	home, _ := os.UserHomeDir()
	servicePath := filepath.Join(home, ".config/systemd/user/llm-mux.service")

	if err := os.Remove(servicePath); err != nil {
		return fmt.Errorf("failed to remove unit file: %w", err)
	}

	exec.Command("systemctl", "--user", "daemon-reload").Run()
	fmt.Println("Service uninstalled")
	return nil
}
