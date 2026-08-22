package cmd

import (
	"log"

	"github.com/spf13/cobra"

	"github.com/ModderMule/emule-http-cache-go/pkg/bundle"
)

var bundleParams = &bundle.BundleRequestParams{}

func init() {
	createBundleCmd.Flags().StringVar(&bundleParams.Directory, "dir", "./", "The root directory with all the files to include in the bundle.")
	createBundleCmd.Flags().StringVar(&bundleParams.Output, "out", "./bin/emule-http-cache.tar.gz", "The name of the bundle to be created.")

	createBundleCmd.Flags().StringArrayVar(&bundleParams.ExcludeDirs, "exclude", nil, "A list of directory names to be excluded.")
	createBundleCmd.Flags().StringArrayVar(&bundleParams.ExcludeFiles, "files", nil, "A list of file names to be excluded.")
	createBundleCmd.Flags().StringArrayVar(&bundleParams.ExcludeExt, "ext", []string{".go"}, "A list of file extensions to be excluded.")
	createBundleCmd.Flags().BoolVar(&bundleParams.ExcludeDotfiles, "skipdot", true, "Exclude dotfiles (starting with .) in the bundle.")

	createBundleCmd.Flags().StringArrayVar(&bundleParams.Files, "add", nil, "A list of files to add to the root in the bundle (from other paths or excluded files).")
	rootCmd.AddCommand(createBundleCmd)
}

var createBundleCmd = &cobra.Command{
	Use:   "bundle",
	Short: "Create a tar.gz bundle of the binary with all static files.",
	RunE: func(cmd *cobra.Command, args []string) error {
		log.Printf("Creating bundle of %s to %s", bundleParams.Directory, bundleParams.Output)
		res, err := bundle.BundleDirectory(bundleParams)
		if err != nil {
			return err
		}

		log.Printf("Created app bundle at %s", res.Output)
		return nil
	},
}
