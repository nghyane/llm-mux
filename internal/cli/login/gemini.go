package login

import (
	"github.com/nghyane/llm-mux/internal/bootstrap"
	clicmd "github.com/nghyane/llm-mux/internal/cmd"
	"github.com/spf13/cobra"
)

var projectID string

var geminiCmd = &cobra.Command{
	Use:   "gemini",
	Short: "Login to Google Gemini",
	Long: `Login to Google Gemini using OAuth authentication.

This command initiates the OAuth flow for Google Gemini services,
allowing you to authenticate and use Gemini models through llm-mux.

The --project flag allows you to specify a Google Cloud project ID.
If not provided, you will be prompted to select from available projects.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfgPath, _ := cmd.Flags().GetString("config")
		noBrowser, _ := cmd.Flags().GetBool("no-browser")

		result, err := bootstrap.Bootstrap(cfgPath)
		if err != nil {
			return err
		}

		options := &clicmd.LoginOptions{
			NoBrowser: noBrowser,
		}

		clicmd.DoLogin(result.Config, projectID, options)
		return nil
	},
}

func init() {
	geminiCmd.Flags().StringVar(&projectID, "project", "", "Google Cloud project ID")
	LoginCmd.AddCommand(geminiCmd)
}
