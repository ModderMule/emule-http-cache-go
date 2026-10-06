package cmd

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ModderMule/emule-http-cache-go/internal/config"
	"github.com/ModderMule/emule-http-cache-go/internal/install"
	"github.com/ModderMule/emule-http-cache-go/pkg/baseurl"
	"github.com/ModderMule/emule-http-cache-go/pkg/ed2k"
	"github.com/ModderMule/emule-http-cache-go/pkg/publicaddr"
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
	initCmd.Flags().StringVar(&initFlags.publicBaseURL, "public-base-url", "", "absolute base URL peers should fetch chunks from; empty detects this machine's address, asking a public echo service")

	rootCmd.AddCommand(initCmd)
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Write a config file and print the generated API key",
	Long: "Write config.yaml and print the API key, once, alongside the ed2k:// link that\n" +
		"configures eMuleQt in one step. The same thing the /install page does, for an\n" +
		"operator who has a shell rather than a browser.\n\n" +
		"The link needs an address other machines can reach. Without --public-base-url,\n" +
		"init asks a public echo service for this machine's address, falls back to its\n" +
		"network interfaces, and pins the result as server.public_base_url.",
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

		varDir, listen, basePath := "data/var", ":8080", ""
		if cfg, err := config.Parse(); err == nil {
			varDir, listen, basePath = cfg.Storage.VarDir, cfg.Server.Addr, cfg.Server.BasePath
		}

		// There is no request to read a host from here, and the link is for a
		// client on another machine, so the address has to be found. It goes
		// through the form like a typed one, which is what pins it.
		var notes []string
		if strings.TrimSpace(initFlags.publicBaseURL) == "" {
			found, by := detectAddress(listen)
			form["publicBaseUrl"] = found.BaseURL()
			notes = describeChoice(found, by)
		}

		settings, errs := install.FromForm(form)
		if settings == nil {
			for field, message := range errs {
				cmd.PrintErrf("--%s: %s\n", flagFor(field), message)
			}
			return fmt.Errorf("nothing was written")
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

		// Nothing pinned means nothing was found: the link can then only name
		// this machine, and the notes say so.
		base := settings.PublicBaseURL
		if base == "" {
			base = localBaseURL(listen)
		}
		if baseurl.IsLoopback(base) {
			notes = append(notes,
				"This address only works on this machine. A client anywhere else cannot use the link.")
		}
		base += basePath

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
		for _, note := range notes {
			cmd.Printf("%s\n", note)
		}
		if len(notes) > 0 {
			cmd.Printf("To change the address, edit server.public_base_url in %s.\n\n", installer.ConfigPath())
		}
		cmd.Printf("Configure eMuleQt with this link:\n\n  %s\n\n", link.String())
		cmd.Println("This is the only time the key is printed. It carries an upload credential —")
		cmd.Println("treat the link exactly as you would treat the key itself.")

		return nil
	},
}

// -- internals ---------------------------------------------------------------

// detectAddress finds the address to hand out, and the echo service that
// confirmed it, if one did.
func detectAddress(listen string) (publicaddr.Choice, string) {
	probed, by, err := publicaddr.Probe(context.Background(), publicaddr.DefaultURLs, publicaddr.DefaultTimeout)
	if err != nil {
		by = ""
	}

	return publicaddr.Choose(probed, publicaddr.Local(), listen), by
}

// describeChoice tells the operator where the address came from and what, if
// anything, they still have to do for it to work.
func describeChoice(c publicaddr.Choice, by string) []string {
	var notes []string

	switch c.Kind {
	case publicaddr.Verified:
		notes = append(notes, fmt.Sprintf("%s is this machine's public address, confirmed by %s.", c.Host, by))
	case publicaddr.BehindNAT:
		port := c.Port
		if port == 0 {
			port = 80
		}

		target := "this machine"
		if c.LAN.IsValid() {
			target = c.LAN.String()
		}

		notes = append(notes,
			fmt.Sprintf("%s is the public address according to %s,", c.Host, by),
			"but it is not on this machine: a router sits in between.",
			fmt.Sprintf("Forward TCP port %d on it to %s, or clients outside this network cannot connect.", port, target))
		if c.LAN.IsValid() {
			notes = append(notes, fmt.Sprintf("For use inside this network only, the address is http://%s.",
				publicaddr.HostPort(c.LAN, c.Port)))
		}
	case publicaddr.Unverified:
		notes = append(notes,
			fmt.Sprintf("No echo service could be reached, so %s was taken from a network interface", c.Host),
			"and is unconfirmed.")
	case publicaddr.LANOnly:
		notes = append(notes,
			fmt.Sprintf("No echo service could be reached. %s is a private address: the link works", c.Host),
			"on this network and nowhere else.")
	case publicaddr.None:
		notes = append(notes, "No address other machines can reach was found, so none was pinned.")
	}

	if c.Proxied {
		notes = append(notes,
			"server.addr listens on loopback only, so a reverse proxy on port 80 is assumed.")
	}

	return notes
}

// localBaseURL is the address of last resort: this machine, on the port it
// listens on.
func localBaseURL(listen string) string {
	_, port, err := net.SplitHostPort(listen)
	if err != nil || port == "" || port == "80" {
		return "http://localhost"
	}

	return "http://localhost:" + port
}

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
