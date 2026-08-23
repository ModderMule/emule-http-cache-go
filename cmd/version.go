package cmd

import (
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// Version is the release version, and the single source of truth for it. The
// literal below is what scripts/bump-version.sh rewrites, what the three build
// workflows scrape with sed to name their archives, and what a `go install`
// build reports with no build flags at all. Keeping one literal rather than
// deriving it from `git describe` means the tag, the artifact filename and the
// version the binary reports cannot drift apart.
//
// Both are still stamped at build time, so a build from a working tree can say
// which commit it came from:
//
//	go build -ldflags "-X github.com/ModderMule/emule-http-cache-go/cmd.Version=v1.0.0"
//
// Commit is left empty, with the module's own build info as a fallback so a
// `go install` build still says something useful.
var (
	Version = "v0.1.2"
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
