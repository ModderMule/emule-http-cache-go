package cmd

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/ModderMule/emule-http-cache-go/internal/config"
	"github.com/ModderMule/emule-http-cache-go/internal/install"
	"github.com/ModderMule/emule-http-cache-go/pkg/ed2k"
)

// initFlags mirrors the browser install form, so the two paths cannot drift.
var initFlags = struct {
	keyID             string
	openUpload        bool
	openUploadQuotaGb string
	quotaGb           string
	minFreeGb         string
	defaultTTLHours   int
	maxTTLHours       int
	publicBaseURL     string
}{}

func init() {
	defaults := install.FormDefaults()

	initCmd.Flags().StringVar(&initFlags.keyID, "key-id", defaults["keyId"], "names this uploader in chunk metadata and in its quota counter")
	initCmd.Flags().BoolVar(&initFlags.openUpload, "open-upload", false, "accept uploads with no API key at all")
	initCmd.Flags().StringVar(&initFlags.openUploadQuotaGb, "open-upload-quota-gb", defaults["openUploadQuotaGb"], "daily limit for anonymous uploads, in GB; 0 is unlimited")
	initCmd.Flags().StringVar(&initFlags.quotaGb, "quota-gb", defaults["quotaGb"], "daily limit for this key, in GB; 0 is unlimited")
	initCmd.Flags().StringVar(&initFlags.minFreeGb, "min-free-gb", defaults["minFreeGb"], "refuse uploads once free disk would drop below this, in GB")
	initCmd.Flags().IntVar(&initFlags.defaultTTLHours, "default-ttl-hours", atoi(defaults["defaultTtlHours"]), "lifetime applied when a client asks for nothing specific")
	initCmd.Flags().IntVar(&initFlags.maxTTLHours, "max-ttl-hours", atoi(defaults["maxTtlHours"]), "longest lifetime a client may ask for")
	initCmd.Flags().StringVar(&initFlags.publicBaseURL, "public-base-url", "", "absolute base URL peers should fetch chunks from; empty derives it per request")

	rootCmd.AddCommand(initCmd)
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Write a config file and print the generated API key",
	Long: "Write config.yaml and print the API key, once, alongside the ed2k:// link that\n" +
		"configures eMuleQt in one step. The same thing the /install page does, for an\n" +
		"operator who has a shell rather than a browser.",
	RunE: func(cmd *cobra.Command, args []string) error {
		form := install.FormDefaults()
		form["keyId"] = initFlags.keyID
		form["openUploadQuotaGb"] = initFlags.openUploadQuotaGb
		form["quotaGb"] = initFlags.quotaGb
		form["minFreeGb"] = initFlags.minFreeGb
		form["defaultTtlHours"] = strconv.Itoa(initFlags.defaultTTLHours)
		form["maxTtlHours"] = strconv.Itoa(initFlags.maxTTLHours)
		form["publicBaseUrl"] = initFlags.publicBaseURL
		if initFlags.openUpload {
			form["openUpload"] = "1"
		}

		settings, errs := install.FromForm(form)
		if settings == nil {
			for field, message := range errs {
				cmd.PrintErrf("--%s: %s\n", flagFor(field), message)
			}
			return fmt.Errorf("nothing was written")
		}

		varDir := "data/var"
		if cfg, err := config.Parse(); err == nil {
			varDir = cfg.Storage.VarDir
		}

		installer := install.New(baseDir(), configPath(), varDir)

		secret, _, err := installer.Install(settings)
		if err != nil {
			if failure, ok := err.(*install.Error); ok && len(failure.Hints) > 0 {
				cmd.PrintErrln(failure.Message)
				cmd.PrintErrln("\nRun one of these, then try again:")
				for _, hint := range failure.Hints {
					cmd.PrintErrf("  %s\n", hint)
				}
				return fmt.Errorf("nothing was written")
			}
			return err
		}

		// Claimed here for the same reason the page claims before rendering:
		// "shown once" has to mean once, whichever path showed it.
		installer.Claim()

		base := settings.PublicBaseURL
		if base == "" {
			base = "http://localhost:8080"
		}

		link := ed2k.Link{
			Name:    ed2k.DefaultName,
			BaseURL: base,
			Secret:  secret,
			KeyID:   settings.KeyID,
		}

		cmd.Printf("Wrote %s\n\n", installer.ConfigPath())
		cmd.Printf("  key id  %s\n", settings.KeyID)
		cmd.Printf("  secret  %s\n", secret)
		cmd.Printf("  base    %s\n\n", base)
		cmd.Printf("Configure eMuleQt with this link:\n\n  %s\n\n", link.String())
		cmd.Println("This is the only time the key is printed. It carries an upload credential —")
		cmd.Println("treat the link exactly as you would treat the key itself.")

		return nil
	},
}

// -- internals ---------------------------------------------------------------

// flagFor maps a form field name onto the flag that sets it, so a validation
// message names something the operator actually typed.
func flagFor(field string) string {
	switch field {
	case "keyId":
		return "key-id"
	case "openUploadQuotaGb":
		return "open-upload-quota-gb"
	case "quotaGb":
		return "quota-gb"
	case "minFreeGb":
		return "min-free-gb"
	case "defaultTtlHours":
		return "default-ttl-hours"
	case "maxTtlHours":
		return "max-ttl-hours"
	case "publicBaseUrl":
		return "public-base-url"
	default:
		return field
	}
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)

	return n
}
