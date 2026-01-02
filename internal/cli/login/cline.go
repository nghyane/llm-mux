package login

import (
	"github.com/nghyane/llm-mux/internal/bootstrap"
	"github.com/nghyane/llm-mux/internal/cmd"
	"github.com/spf13/cobra"
)

var clineCmd = &cobra.Command{
	Use:   "cline",
	Short: "Login to Cline",
	Long: `Login to Cline using a refresh token.

Unlike traditional OAuth flows, Cline uses a simpler approach where you
export a refresh token from the VSCode extension and provide it to llm-mux.
Once authenticated, your credentials will be saved locally.`,
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

		cmd.DoClineLogin(result.Config, options)
		return nil
	},
}

func init() {
	LoginCmd.AddCommand(clineCmd)
}
