package login

import (
	"github.com/nghyane/llm-mux/internal/bootstrap"
	"github.com/nghyane/llm-mux/internal/cmd"
	"github.com/spf13/cobra"
)

var kiroCmd = &cobra.Command{
	Use:   "kiro",
	Short: "Login to Kiro",
	Long: `Login to Kiro using AWS authentication.

This command initiates the authentication flow for Kiro (Amazon Q/CodeWhisperer services).
It will open a browser window for you to sign in with your AWS Builder ID.
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

		cmd.DoKiroLogin(result.Config, options)
		return nil
	},
}

func init() {
	LoginCmd.AddCommand(kiroCmd)
}
