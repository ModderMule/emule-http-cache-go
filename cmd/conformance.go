package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ModderMule/emule-http-cache-go/internal/config"
	"github.com/ModderMule/emule-http-cache-go/internal/conformance"
)

var conformanceVerbose bool

func init() {
	conformanceCmd.Flags().BoolVarP(&conformanceVerbose, "verbose", "v", false, "trace every request and response")
	rootCmd.AddCommand(conformanceCmd)
}

var conformanceCmd = &cobra.Command{
	Use:   "conformance [baseUrl] [apiKey]",
	Short: "Check that a backend implements the contract",
	Long: "Run the contract test against any eMule HTTP Cache backend, Go or otherwise.\n" +
		"It speaks nothing but HTTP, so pointing it at another implementation is a valid\n" +
		"way to check that one. Defaults to http://localhost:8080 and the first key in\n" +
		"the local config.",
	Args:          cobra.MaximumNArgs(2),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		base := "http://localhost:8080"
		if len(args) >= 1 {
			base = args[0]
		}

		key := ""
		if len(args) == 2 {
			key = args[1]
		} else if cfg, err := config.Parse(); err == nil && len(cfg.APIKeys) > 0 {
			key = cfg.APIKeys[0].Secret
		}

		if key == "" {
			return fmt.Errorf("no API key: pass one as the second argument, or run this where a config file with one lives")
		}

		reporter := conformance.NewConsole(conformanceVerbose)
		result := (&conformance.Suite{BaseURL: base, APIKey: key}).Run(reporter)
		reporter.Summary(result)

		if !result.OK() {
			return fmt.Errorf("%d assertion(s) failed", result.Failed)
		}

		return nil
	},
}
