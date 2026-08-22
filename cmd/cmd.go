// Package cmd wires the emule-http-cache CLI: a cobra root command, config
// initialisation, and the signal handler that drives graceful shutdown.
package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/ModderMule/emule-http-cache-go/internal/config"
	"github.com/ModderMule/emule-http-cache-go/log"
)

var rootCmd = &cobra.Command{
	Use:   "emule-http-cache",
	Short: "Encrypted chunk cache for eMuleQt's HTTP Cache feature",
	Long: "emule-http-cache stores the AES-256-CBC chunks an eMuleQt uploader publishes so\n" +
		"that several downloaders can fetch one part over HTTP instead of costing the\n" +
		"uploader an upload slot each. It never receives a key, a file hash or a part\n" +
		"number — only opaque blobs of a uniform size.",
	Run: func(cmd *cobra.Command, args []string) {
		_ = cmd.Usage()
	},
}

// Execute runs the CLI.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

var configFile string

func init() {
	cobra.OnInitialize(initConfig)
	rootCmd.PersistentFlags().StringVar(&configFile, "config", "", "config file (default is ./config.yaml)")
}

// initConfig loads the config before any subcommand runs.
//
// A missing config file is not an error: `serve` then answers /install and 503s
// every /v1 route until one exists, which is the whole point of having an
// install page.
func initConfig() {
	if err := config.LoadConfig(".", configFile); err != nil {
		fmt.Printf("config: %v\n", err)
		os.Exit(1)
	}
}

// -- internals ---------------------------------------------------------------

// getLogger builds the application logger from the loaded config.
func getLogger() (log.Logger, error) {
	return log.NewLogger(log.NewConfig(viper.GetViper()), log.DefaultLogger)
}

// baseDir is the directory the config file lives in, which is where the
// installer writes and where a config.example.yaml is looked for.
func baseDir() string {
	if used := viper.ConfigFileUsed(); used != "" {
		return filepath.Dir(used)
	}
	if configFile != "" {
		return filepath.Dir(configFile)
	}

	return "."
}

// configPath is the file the installer will write.
func configPath() string {
	if used := viper.ConfigFileUsed(); used != "" {
		return used
	}
	if configFile != "" {
		return configFile
	}

	return filepath.Join(baseDir(), "config.yaml")
}

// listenExitCommand cancels the application context when a termination signal
// is received. A second signal forcefully exits the process.
func listenExitCommand(logger log.Logger, cancel context.CancelFunc) {
	c := make(chan os.Signal, 2)
	signal.Notify(c, syscall.SIGTERM, os.Interrupt)

	var count int
	for range c {
		count++
		if count >= 2 {
			logger.Infof("Forcefully exiting...")
			os.Exit(1)
		}

		logger.Infof("Signal caught, shutting down... (signal again to force exit)")
		cancel()
	}
}
