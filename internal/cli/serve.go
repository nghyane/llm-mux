package cli

import (
	"os"
	"time"

	"github.com/nghyane/llm-mux/internal/bootstrap"
	"github.com/nghyane/llm-mux/internal/cmd"
	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/logging"
	log "github.com/nghyane/llm-mux/internal/logging"
	"github.com/nghyane/llm-mux/internal/usage"
	"github.com/spf13/cobra"
)

var servePort int

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the llm-mux server",
	Long: `Start the llm-mux API gateway server.

This is the main command to run the proxy server. It loads the configuration,
initializes the token stores, and starts the HTTP server.`,
	Run: func(c *cobra.Command, args []string) {
		logging.SetupBaseLogger()

		configPath := cfgFile
		if configPath == "" {
			configPath = "$XDG_CONFIG_HOME/llm-mux/config.yaml"
		}

		result, err := bootstrap.Bootstrap(configPath)
		if err != nil {
			log.Fatalf("Failed to bootstrap: %v", err)
			os.Exit(1)
		}

		cfg := result.Config

		if servePort != 0 && servePort != 8317 {
			cfg.Port = servePort
		}

		usage.SetStatisticsEnabled(cfg.Usage.DSN != "")

		if cfg.Usage.DSN != "" {
			initUsageBackend(cfg)
		}

		if err := logging.ConfigureLogOutput(cfg.LoggingToFile); err != nil {
			log.Fatalf("Failed to configure log output: %v", err)
		}

		cmd.StartService(cfg, result.ConfigFilePath, "")
	},
}

func initUsageBackend(cfg *config.Config) {
	var flushInterval time.Duration
	if cfg.Usage.FlushInterval != "" {
		if d, parseErr := time.ParseDuration(cfg.Usage.FlushInterval); parseErr == nil {
			flushInterval = d
		}
	}
	if flushInterval == 0 {
		flushInterval = 5 * time.Second
	}
	batchSize := cfg.Usage.BatchSize
	if batchSize == 0 {
		batchSize = 100
	}
	retentionDays := cfg.Usage.RetentionDays
	if retentionDays == 0 {
		retentionDays = 30
	}
	backendCfg := usage.BackendConfig{
		DSN:           cfg.Usage.DSN,
		BatchSize:     batchSize,
		FlushInterval: flushInterval,
		RetentionDays: retentionDays,
	}
	if initErr := usage.Initialize(backendCfg); initErr != nil {
		log.Warnf("Failed to initialize usage backend: %v", initErr)
	} else {
		log.Infof("Usage backend initialized: %s", cfg.Usage.DSN)
	}
}

func init() {
	serveCmd.Flags().IntVarP(&servePort, "port", "p", 8317, "server port")
	rootCmd.AddCommand(serveCmd)
}
