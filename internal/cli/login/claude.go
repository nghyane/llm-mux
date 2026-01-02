package login

import (
	"github.com/nghyane/llm-mux/internal/bootstrap"
	"github.com/nghyane/llm-mux/internal/cmd"
	"github.com/spf13/cobra"
)

var claudeCmd = &cobra.Command{
	Use:   "claude",
	Short: "Login to Claude (Anthropic)",
	Long: `Login to Claude Pro/Max using OAuth.

This command initiates the OAuth authentication flow for Anthropic Claude services.
It will open a browser window for you to sign in with your Anthropic account.
Once authenticated, your credentials will be saved locally.

Use --no-browser flag to get a URL to open manually instead.`,
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

		cmd.DoClaudeLogin(result.Config, options)
		return nil
	},
}

func init() {
	LoginCmd.AddCommand(claudeCmd)
}
