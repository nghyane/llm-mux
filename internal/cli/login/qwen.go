package login

import (
	"github.com/nghyane/llm-mux/internal/bootstrap"
	cmdpkg "github.com/nghyane/llm-mux/internal/cmd"
	"github.com/spf13/cobra"
)

var qwenCmd = &cobra.Command{
	Use:   "qwen",
	Short: "Login to Alibaba Qwen",
	Long: `Login to Alibaba Qwen using device-based authentication.

This command initiates the device flow authentication for Qwen services.
It will provide you with a URL and code to enter in your browser to authenticate.
Once authenticated, your credentials will be saved locally.

Use --no-browser flag to prevent automatic browser opening.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfgPath, _ := cmd.Flags().GetString("config")
		noBrowser, _ := cmd.Flags().GetBool("no-browser")

		result, err := bootstrap.Bootstrap(cfgPath)
		if err != nil {
			return err
		}

		options := &cmdpkg.LoginOptions{
			NoBrowser: noBrowser,
		}

		cmdpkg.DoQwenLogin(result.Config, options)
		return nil
	},
}

func init() {
	LoginCmd.AddCommand(qwenCmd)
}
