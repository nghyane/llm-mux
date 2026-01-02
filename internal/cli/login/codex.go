package login

import (
	"github.com/nghyane/llm-mux/internal/bootstrap"
	"github.com/nghyane/llm-mux/internal/cmd"
	"github.com/spf13/cobra"
)

var codexCmd = &cobra.Command{
	Use:   "codex",
	Short: "Login to OpenAI Codex",
	Long: `Login to OpenAI Codex using OAuth.

This command initiates the OAuth authentication flow for OpenAI Codex services.
A browser window will open to complete the authentication process.
Use --no-browser flag to get the URL instead of opening the browser automatically.`,
	RunE: func(c *cobra.Command, args []string) error {
		cfgPath, _ := c.Flags().GetString("config")
		noBrowser, _ := c.Flags().GetBool("no-browser")

		result, err := bootstrap.Bootstrap(cfgPath)
		if err != nil {
			return err
		}

		options := &cmd.LoginOptions{
			NoBrowser: noBrowser,
		}

		cmd.DoCodexLogin(result.Config, options)
		return nil
	},
}

func init() {
	LoginCmd.AddCommand(codexCmd)
}
