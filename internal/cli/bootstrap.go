package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nghyane/llm-mux/internal/buildinfo"
	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/json"
	"github.com/nghyane/llm-mux/internal/util"
)

func DoInitConfig(configPath string, force bool) error {
	configPath, _ = util.ResolveAuthDir(configPath)
	dir := filepath.Dir(configPath)
	credPath := config.CredentialsFilePath()

	configExists := fileExists(configPath)
	credExists := fileExists(credPath)

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	_ = os.MkdirAll(filepath.Join(dir, "auth"), 0o700)

	if !configExists {
		if err := os.WriteFile(configPath, config.GenerateDefaultConfigYAML(), 0o600); err != nil {
			return fmt.Errorf("failed to write config: %w", err)
		}
		fmt.Printf("Created: %s\n", configPath)
	}

	if credExists && !force {
		key := config.GetManagementKey()
		if key != "" {
			fmt.Printf("Management key: %s\n", key)
			fmt.Printf("Location: %s\n", credPath)
			fmt.Println("Use --init --force to regenerate")
			return nil
		}
	}

	key, err := config.CreateCredentials()
	if err != nil {
		return fmt.Errorf("failed to create credentials: %w", err)
	}

	if credExists && force {
		fmt.Println("Regenerated management key:")
	} else {
		fmt.Println("Generated management key:")
	}
	fmt.Printf("  %s\n", key)
	fmt.Printf("Location: %s\n", credPath)
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func DoUpdate(checkOnly bool) error {
	fmt.Println("Checking for updates...")

	latestVersion, err := fetchLatestVersion()
	if err != nil {
		return fmt.Errorf("failed to check for updates: %w", err)
	}

	currentVersion := strings.TrimPrefix(buildinfo.Version, "v")
	latestVersion = strings.TrimPrefix(latestVersion, "v")

	if currentVersion == "dev" || currentVersion == "" {
		fmt.Println("Running development version, updating to latest release...")
	} else if compareVersions(currentVersion, latestVersion) >= 0 {
		fmt.Printf("Already up to date (current: v%s, latest: v%s)\n", currentVersion, latestVersion)
		return nil
	} else {
		fmt.Printf("Update available: v%s -> v%s\n", currentVersion, latestVersion)
	}

	if checkOnly {
		return nil
	}

	fmt.Println("Downloading and installing update...")
	if err := runInstallScript(); err != nil {
		return fmt.Errorf("failed to install update: %w", err)
	}
	fmt.Println("Update complete! Please restart llm-mux.")
	return nil
}

func fetchLatestVersion() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/nghyane/llm-mux/releases/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", err
	}
	return release.TagName, nil
}

func compareVersions(v1, v2 string) int {
	parts1 := strings.Split(v1, ".")
	parts2 := strings.Split(v2, ".")

	maxLen := len(parts1)
	if len(parts2) > maxLen {
		maxLen = len(parts2)
	}

	for i := 0; i < maxLen; i++ {
		var n1, n2 int
		if i < len(parts1) {
			fmt.Sscanf(parts1[i], "%d", &n1)
		}
		if i < len(parts2) {
			fmt.Sscanf(parts2[i], "%d", &n2)
		}
		if n1 < n2 {
			return -1
		}
		if n1 > n2 {
			return 1
		}
	}
	return 0
}

func runInstallScript() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", "https://raw.githubusercontent.com/nghyane/llm-mux/main/install.sh", nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("failed to download install script: status %d", resp.StatusCode)
	}

	scriptContent, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	tmpFile, err := os.CreateTemp("", "llm-mux-install-*.sh")
	if err != nil {
		return err
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write(scriptContent); err != nil {
		tmpFile.Close()
		return err
	}
	tmpFile.Close()

	if err := os.Chmod(tmpFile.Name(), 0755); err != nil {
		return err
	}

	cmd := exec.Command("bash", tmpFile.Name(), "--no-service", "--force")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
