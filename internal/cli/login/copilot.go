package login

import (
	"github.com/nghyane/llm-mux/internal/bootstrap"
	"github.com/nghyane/llm-mux/internal/cmd"
	"github.com/spf13/cobra"
)

var copilotCmd = &cobra.Command{
	Use:   "copilot",
	Short: "Login to GitHub Copilot",
	Long: `Login to GitHub Copilot using OAuth device flow.

This command initiates the GitHub OAuth device flow authentication.
You will be prompted to visit a URL and enter a code to authorize access.
Once authenticated, your credentials will be saved for future use.`,
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

		cmd.DoCopilotLogin(result.Config, options)
		return nil
	},
}

func init() {
	LoginCmd.AddCommand(copilotCmd)
}
