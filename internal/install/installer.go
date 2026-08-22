package install

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ModderMule/emule-http-cache-go/internal/config"
)

// secretBytes is the entropy behind a generated API key: 24 bytes, rendered as
// 48 hex characters.
const secretBytes = 24

// Error is a failure the install page can show, with the concrete commands that
// would fix it.
type Error struct {
	Message string
	Hints   []string
}

func (e *Error) Error() string { return e.Message }

// Installer writes and inspects the server's config file.
type Installer struct {
	baseDir    string
	configPath string
	varDir     string
}

// New builds an installer rooted at a directory.
//
// configPath and varDir are passed explicitly rather than derived, because the
// running server may have been pointed at either with a flag or an environment
// variable, and the install page must agree with it.
func New(baseDir, configPath, varDir string) *Installer {
	if configPath == "" {
		configPath = filepath.Join(baseDir, "config.yaml")
	}
	if varDir == "" {
		varDir = filepath.Join(baseDir, "data", "var")
	}

	return &Installer{baseDir: baseDir, configPath: configPath, varDir: varDir}
}

// ConfigPath is the file this installer writes.
func (i *Installer) ConfigPath() string { return i.configPath }

// IsInstalled reports whether a config file already exists.
func (i *Installer) IsInstalled() bool {
	_, err := os.Stat(i.configPath)

	return err == nil
}

// State returns the install marker, or false when this config was not
// machine-generated. A hand-written config has no marker, which is what stops
// /install reading somebody's own key back to whoever asks.
func (i *Installer) State() (*State, bool) {
	return readState(i.markerPath())
}

// Claim records that the key has now been shown.
//
// Called *before* the key reaches the page, never after: if this cannot be
// written there is no way to stop the key being shown again, and the caller has
// to say so rather than quietly break the promise.
func (i *Installer) Claim() bool {
	state, ok := i.State()
	if !ok {
		return false
	}

	now := time.Now().Unix()
	state.ClaimedAt = &now

	return writeState(i.markerPath(), state) == nil
}

// SecretFor reads the configured secret for a key id, for the one page allowed
// to show it.
func (i *Installer) SecretFor(keyID string) (string, bool) {
	cfg, err := config.ParseFile(i.configPath)
	if err != nil {
		return "", false
	}

	return cfg.SecretFor(keyID)
}

// Install writes the config file and returns the generated secret.
//
// The secret is returned rather than stored anywhere else: it lives in the
// config file and in the one page allowed to show it, and nowhere in between.
func (i *Installer) Install(s *Settings) (string, *State, error) {
	if i.IsInstalled() {
		return "", nil, &Error{Message: "a config file already exists, so there is nothing to install."}
	}

	if err := i.preflight(); err != nil {
		return "", nil, err
	}

	source, err := i.template()
	if err != nil {
		return "", nil, err
	}

	secret, err := newSecret()
	if err != nil {
		return "", nil, &Error{Message: "the system random number generator is unavailable, so no key could be generated."}
	}

	generated, err := render(source, s, secret)
	if err != nil {
		return "", nil, &Error{
			Message: "config.example.yaml is not shaped the way the installer expects, so nothing was written (" + err.Error() + ").",
			Hints:   []string{"cp config.example.yaml " + i.configPath},
		}
	}

	if err := i.commit(generated); err != nil {
		return "", nil, err
	}

	// Trust nothing about the substitutions: load the file back and compare.
	if mismatch := i.verify(s, secret); mismatch != "" {
		_ = os.Remove(i.configPath)
		return "", nil, &Error{Message: "the generated config did not load correctly (" + mismatch + "), so it was removed."}
	}

	// Unclaimed: the page that shows the key is what claims it.
	state := &State{GeneratedAt: time.Now().Unix(), KeyID: s.KeyID}
	if err := ensureDir(i.varDir); err != nil || writeState(i.markerPath(), state) != nil {
		_ = os.Remove(i.configPath)
		return "", nil, &Error{
			Message: "the install marker could not be written, so the config was removed rather than leave a key nobody can see.",
			Hints:   chmodHints(i.varDir),
		}
	}

	return secret, state, nil
}

// -- internals ---------------------------------------------------------------

// preflight reports every problem at once, so one page can fix all of them.
func (i *Installer) preflight() error {
	var unwritable []string

	if !writable(filepath.Dir(i.configPath)) {
		unwritable = append(unwritable, filepath.Dir(i.configPath))
	}
	if err := ensureDir(i.varDir); err != nil || !writable(i.varDir) {
		unwritable = append(unwritable, i.varDir)
	}

	if len(unwritable) == 0 {
		return nil
	}

	return &Error{
		Message: fmt.Sprintf("the server (running as %q) cannot write to %s.", currentUser(), strings.Join(unwritable, ", ")),
		Hints:   chmodHints(unwritable...),
	}
}

// template prefers a config.example.yaml beside the binary, so an operator can
// customise the comments, and falls back to the embedded copy.
func (i *Installer) template() (string, error) {
	raw, err := os.ReadFile(filepath.Join(i.baseDir, "config.example.yaml"))
	if err == nil {
		return string(raw), nil
	}

	if embeddedExample == "" {
		return "", &Error{Message: "config.example.yaml is missing and no copy is compiled in, so there is no template to install from."}
	}

	return embeddedExample, nil
}

// commit writes the config atomically, so no half-written file is ever read.
func (i *Installer) commit(contents string) error {
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return &Error{Message: "the system random number generator is unavailable."}
	}

	tmp := i.configPath + ".tmp-" + hex.EncodeToString(suffix)

	// 0600, not the 0644 the PHP server needs: there, a CLI tool and the web
	// server are different users. One daemon has one identity, and the file
	// holds an upload credential.
	if err := os.WriteFile(tmp, []byte(contents), 0o600); err != nil {
		return &Error{
			Message: "the config file could not be written to " + filepath.Dir(i.configPath) + ".",
			Hints:   chmodHints(filepath.Dir(i.configPath)),
		}
	}

	if err := os.Rename(tmp, i.configPath); err != nil {
		_ = os.Remove(tmp)
		return &Error{
			Message: "the config file could not be created in " + filepath.Dir(i.configPath) + ".",
			Hints:   chmodHints(filepath.Dir(i.configPath)),
		}
	}

	return nil
}

// verify loads the generated file back and compares every field against the
// form. It returns what differed, or "" when nothing did.
//
// This is what makes a substitution-based generator safe: a needle that stopped
// matching, or a value that did not survive YAML, is caught here rather than
// discovered months later by an operator wondering why a limit does nothing.
func (i *Installer) verify(s *Settings, secret string) string {
	cfg, err := config.ParseFile(i.configPath)
	if err != nil {
		return err.Error()
	}

	stored, ok := cfg.SecretFor(s.KeyID)
	if !ok {
		return "the key " + strconv.Quote(s.KeyID) + " is not in it"
	}
	if subtle.ConstantTimeCompare([]byte(stored), []byte(secret)) != 1 {
		return "the generated key did not survive the write"
	}

	checks := []struct {
		field string
		got   any
		want  any
	}{
		{"quota_bytes_per_day", cfg.QuotaFor(s.KeyID), s.QuotaBytesPerDay},
		{"open_upload", cfg.Upload.OpenUpload, s.OpenUpload},
		{"open_upload_quota_bytes_per_day", cfg.Upload.OpenUploadQuotaBytesPerDay, s.OpenUploadQuotaBytesPerDay},
		{"min_free_bytes", cfg.Storage.MinFreeBytes, s.MinFreeBytes},
		{"default_ttl", cfg.Storage.DefaultTTL, s.DefaultTTL},
		{"max_ttl", cfg.Storage.MaxTTL, s.MaxTTL},
		{"public_base_url", cfg.Server.PublicBaseURL, s.PublicBaseURL},
	}

	for _, c := range checks {
		if c.got != c.want {
			return fmt.Sprintf("%s is %v, expected %v", c.field, c.got, c.want)
		}
	}

	for _, key := range cfg.APIKeys {
		if key.ID == s.KeyID && !key.Enabled {
			return "the generated key is disabled"
		}
	}

	return ""
}

func (i *Installer) markerPath() string {
	return filepath.Join(i.varDir, markerFile)
}

// newSecret is 24 bytes from a CSPRNG as 48 hex characters.
func newSecret() (string, error) {
	raw := make([]byte, secretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}

	return hex.EncodeToString(raw), nil
}

func ensureDir(path string) error {
	if err := os.MkdirAll(path, 0o775); err != nil {
		if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
			return nil
		}
		return err
	}

	return nil
}

// writable probes a directory by creating and removing a file in it, which is
// the only portable way to know: the mode bits alone do not account for the
// effective user, ACLs or a read-only mount.
func writable(dir string) bool {
	probe := filepath.Join(dir, ".emule-http-cache-probe")

	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return false
	}
	_ = f.Close()
	_ = os.Remove(probe)

	return true
}

func chmodHints(paths ...string) []string {
	quoted := strings.Join(paths, " ")

	return []string{
		"chmod 770 " + quoted,
		"chown " + currentUser() + " " + quoted,
	}
}

func currentUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}

	return "the server's user"
}
