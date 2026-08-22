package cmd

import (
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// Version and Commit are stamped at build time:
//
//	go build -ldflags "-X github.com/ModderMule/emule-http-cache-go/cmd.Version=1.0.0"
//
// Left as placeholders otherwise, with the module's own build info as a
// fallback so a `go install` build still says something useful.
var (
	Version = "dev"
	Commit  = ""
)

func init() {
	rootCmd.AddCommand(versionCmd)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version and the protocol it speaks",
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Printf("emule-http-cache %s\n", Version)
		if revision := commit(); revision != "" {
			cmd.Printf("commit           %s\n", revision)
		}
		cmd.Printf("protocol         emule-http-cache v1\n")
		cmd.Printf("go               %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	},
}

// -- internals ---------------------------------------------------------------

func commit() string {
	if Commit != "" {
		return Commit
	}

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}

	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			return setting.Value
		}
	}

	return ""
}
