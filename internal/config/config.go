// Package config loads the cache server's runtime configuration via viper,
// layering config.yaml, optional .env files, and environment variables.
//
// The field set mirrors the PHP reference server's config.php one-for-one, and
// so do its defaulting rules — every one of them has a test behind it in
// emule-http-cache-php/tests/StorageTest.php. Where PHP wrote `?? default`,
// applyDefaults() writes the same default here.
package config

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/pkg/errors"
	"github.com/spf13/viper"
)

// AnonymousKeyID is the owner recorded for an upload that arrived without a
// credential, which is only possible when Upload.OpenUpload is on.
//
// Reserved: a configured key may never claim it, so nobody can ever
// authenticate as the owner of an anonymous chunk and delete it.
const AnonymousKeyID = "anonymous"

// EnvConfigFile names the environment variable that points at a config file.
const EnvConfigFile = "EMULE_HTTP_CACHE_CONFIG"

// basePathPattern is what gin will accept as a route prefix. Validated at load
// because gin panics on a malformed pattern, and a panic at boot from a config
// typo is a bad error message.
var basePathPattern = regexp.MustCompile(`^(/[A-Za-z0-9._~-]+)*$`)

// maxPublicBaseURL keeps the generated chunk URL inside the client's
// HTTPCACHE_MAX_URL_LEN of 1024, allowing for "/v1/chunks/" plus 32 hex chars.
const maxPublicBaseURL = 1024 - len("/v1/chunks/") - 32

// Config is the top-level server configuration.
type Config struct {
	Server  ServerConfig  `mapstructure:"server"`
	Storage StorageConfig `mapstructure:"storage"`
	GC      GCConfig      `mapstructure:"gc"`
	Upload  UploadConfig  `mapstructure:"upload"`

	// RawKeys is the api_keys mapping as written. Normalised into APIKeys by
	// applyDefaults; nothing outside this package should read it.
	RawKeys map[string]RawAPIKey `mapstructure:"api_keys"`

	// APIKeys is the normalised, deterministically ordered credential list.
	APIKeys []APIKey `mapstructure:"-"`

	// GCProbability is the PHP reference server's per-upload sweep chance. It
	// is accepted so a config carried over from that server still loads, and
	// ignored because a daemon sweeps on a timer instead. A pointer so that
	// "present" can be told from "zero", which is what Warnings reports on.
	GCProbability *float64 `mapstructure:"gcProbability"`
}

// ServerConfig controls the HTTP listener and the URLs it hands out.
type ServerConfig struct {
	Addr string `mapstructure:"addr"`
	Mode string `mapstructure:"mode"` // gin mode: debug | release | test

	// ReadTimeout and WriteTimeout are absolute deadlines measured from the
	// start of a request, so any value large enough for a 9.7 MB transfer on a
	// slow link is useless as a stall detector. They default to 0 and the
	// download/upload paths roll per-slice deadlines instead.
	ReadTimeout       time.Duration `mapstructure:"read_timeout"`
	WriteTimeout      time.Duration `mapstructure:"write_timeout"`
	ReadHeaderTimeout time.Duration `mapstructure:"read_header_timeout"`
	IdleTimeout       time.Duration `mapstructure:"idle_timeout"`

	// ShutdownTimeout must outlast an upload in flight: http.Server.Shutdown
	// does not interrupt active handlers, and 9,728,016 bytes at a real
	// 100 KB/s is 97 seconds.
	ShutdownTimeout time.Duration `mapstructure:"shutdown_timeout"`

	// PublicBaseURL is the absolute base written into the 201 "url" field.
	// Empty derives it from the request, which is wrong behind a proxy that
	// rewrites Host — pin it there.
	PublicBaseURL string `mapstructure:"public_base_url"`

	// BasePath mounts every route under a prefix, for a reverse proxy that does
	// not strip it. Empty mounts at the root.
	BasePath string `mapstructure:"base_path"`

	// StaticFilePath loads HTML templates from disk instead of the embedded
	// copies, so a template can be edited without a rebuild.
	StaticFilePath string `mapstructure:"static_file_path"`
}

// StorageConfig describes where chunks live and how large they may be.
type StorageConfig struct {
	DataDir string `mapstructure:"data_dir"`
	VarDir  string `mapstructure:"var_dir"`

	MaxChunkSize int64 `mapstructure:"max_chunk_size"`

	// MinFreeBytes refuses uploads once free space on the storage volume would
	// drop below it. 0 disables the check. Complements the per-key daily quota:
	// that limits one key for one day, this protects the host from every key.
	MinFreeBytes int64 `mapstructure:"min_free_bytes"`

	DefaultTTL time.Duration `mapstructure:"default_ttl"`
	MaxTTL     time.Duration `mapstructure:"max_ttl"`

	// Fsync flushes a chunk to the platter before it is renamed into place.
	// Costly on darwin, where File.Sync issues F_FULLFSYNC.
	Fsync bool `mapstructure:"fsync"`
}

// GCConfig controls the background expiry sweep.
type GCConfig struct {
	// Interval is the sweep period. 0 disables the in-process sweeper, leaving
	// the `gc` subcommand on cron as the only reclaim path.
	Interval   time.Duration `mapstructure:"interval"`
	MaxDeletes int           `mapstructure:"max_deletes"`
}

// UploadConfig controls who may store a chunk.
type UploadConfig struct {
	// OpenUpload accepts POST /v1/chunks with no credential at all. Anonymous
	// chunks are owned by AnonymousKeyID, which nobody can authenticate as, so
	// they cannot be deleted through the API and only lapse at their TTL.
	OpenUpload bool `mapstructure:"open_upload"`

	OpenUploadQuotaBytesPerDay int64 `mapstructure:"open_upload_quota_bytes_per_day"`
}

// RawAPIKey is one api_keys entry exactly as written in the config file.
type RawAPIKey struct {
	Secret           string `mapstructure:"secret"`
	QuotaBytesPerDay int64  `mapstructure:"quota_bytes_per_day"`

	// Enabled is a pointer so an absent key can be told from an explicit false:
	// a config predating this field means enabled, not disabled.
	Enabled *bool `mapstructure:"enabled"`
}

// APIKey is one normalised uploader credential.
//
// A disabled key stays loaded rather than being dropped: its quota counter and
// the chunks it already owns still have to resolve, it just stops matching.
type APIKey struct {
	ID               string
	Secret           string
	QuotaBytesPerDay int64 // 0 = unlimited
	Enabled          bool
}

// LoadConfig reads config.yaml, any .env files and the environment into the
// global viper instance.
//
// The file is resolved as: configFile argument, then $EMULE_HTTP_CACHE_CONFIG,
// then ./config.yaml. A missing file is not an error — the server answers
// /install and 503s every /v1/ route until one exists — so callers check
// Installed() rather than the error.
func LoadConfig(configPath string, configFile string, envFiles ...string) error {
	if configPath == "" {
		configPath = "."
	}
	if configFile == "" {
		configFile = os.Getenv(EnvConfigFile)
	}
	if configFile != "" {
		viper.SetConfigFile(configFile)
	} else {
		viper.SetConfigName("config")
		viper.AddConfigPath(configPath)
	}

	// Match env vars to nested config keys: "." and "-" become "_".
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))

	// AutomaticEnv alone only resolves keys viper already knows about, which
	// on a server with no config file yet is none of them — SERVER_ADDR would
	// be silently ignored and the listener would come up on the default port.
	// Binding every key from the struct registers them all, and deriving the
	// list by reflection means it cannot drift from the fields.
	bindEnvs(viper.GetViper(), "", reflect.TypeOf(Config{}))
	if err := readEnvFile(configPath, envFiles...); err != nil {
		return errors.Wrap(err, "unable to read env config")
	}
	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if errors.As(err, &notFound) || os.IsNotExist(errors.Cause(err)) {
			return nil // uninstalled; the install page is the whole UI until then
		}
		return errors.Wrap(err, "unable to read config")
	}
	return nil
}

// bindEnvs registers every leaf config key with viper so an environment
// variable can supply it even when no config file exists.
//
// Maps are skipped: api_keys has no fixed key set, so there is nothing to bind,
// and credentials belong in a file rather than in a process environment anyway.
func bindEnvs(v *viper.Viper, prefix string, t reflect.Type) {
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)

		tag := field.Tag.Get("mapstructure")
		if tag == "" || tag == "-" {
			continue
		}

		key := tag
		if prefix != "" {
			key = prefix + "." + tag
		}

		ft := field.Type
		for ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}

		if ft.Kind() == reflect.Struct && ft != reflect.TypeOf(time.Duration(0)) {
			bindEnvs(v, key, ft)
			continue
		}
		if ft.Kind() == reflect.Map {
			continue
		}

		_ = v.BindEnv(key)
	}
}

// Installed reports whether a config file was actually found and read.
func Installed() bool {
	return viper.ConfigFileUsed() != ""
}

// Parse unmarshals the loaded viper config into a typed Config, applying
// defaults and validating the result.
func Parse() (*Config, error) {
	cfg := &Config{}

	// Composed rather than the bare duration hook: supplying any DecodeHook
	// replaces viper's default pair, and dropping the slice hook would silently
	// break comma-separated env overrides of a list key.
	hook := mapstructure.ComposeDecodeHookFunc(
		mapstructure.StringToTimeDurationHookFunc(),
		mapstructure.StringToSliceHookFunc(","),
	)
	if err := viper.Unmarshal(cfg, viper.DecodeHook(hook)); err != nil {
		return nil, errors.Wrap(err, "unable to unmarshal config")
	}

	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// ClampTTL folds a client-requested TTL into the configured window. A request
// of zero or less means "no preference" and gets the default.
func (c *Config) ClampTTL(requested time.Duration) time.Duration {
	if requested <= 0 {
		return c.Storage.DefaultTTL
	}
	if requested > c.Storage.MaxTTL {
		return c.Storage.MaxTTL
	}
	return requested
}

// QuotaFor returns the daily byte allowance for a key id. 0 means unlimited.
func (c *Config) QuotaFor(keyID string) int64 {
	if keyID == AnonymousKeyID {
		return c.Upload.OpenUploadQuotaBytesPerDay
	}
	for _, k := range c.APIKeys {
		if k.ID == keyID {
			return k.QuotaBytesPerDay
		}
	}
	return 0
}

// SecretFor returns the configured secret for a key id, enabled or not.
func (c *Config) SecretFor(keyID string) (string, bool) {
	for _, k := range c.APIKeys {
		if k.ID == keyID {
			return k.Secret, true
		}
	}
	return "", false
}

// ChunkPath is where a chunk's blob or sidecar lives, sharded by the first two
// hex characters of its id. Callers must have validated the id first.
func (c *Config) ChunkPath(id, extension string) string {
	return filepath.Join(c.Storage.DataDir, id[:2], id+"."+extension)
}

// VarPath is a file under the runtime state directory.
func (c *Config) VarPath(name string) string {
	return filepath.Join(c.Storage.VarDir, name)
}

// -- internals ---------------------------------------------------------------

// applyDefaults fills every field whose zero value is not a meaningful setting,
// then normalises the api_keys mapping into the ordered APIKeys slice.
//
// Defaults live here rather than in viper.SetDefault so that the whole set is
// readable in one place and so a value can depend on another.
func (c *Config) applyDefaults() {
	c.applyDefaultsWith(viper.IsSet)
}

// applyDefaultsWith is applyDefaults against a specific "is this key set?"
// predicate. Two settings — storage.fsync and gc.interval — have a meaningful
// zero value, so for those "absent" and "explicitly zero" must be told apart.
func (c *Config) applyDefaultsWith(isSet func(string) bool) {
	if c.Server.Addr == "" {
		c.Server.Addr = ":8080"
	}
	if c.Server.Mode == "" {
		c.Server.Mode = "release"
	}
	if c.Server.ReadHeaderTimeout == 0 {
		c.Server.ReadHeaderTimeout = 10 * time.Second
	}
	if c.Server.IdleTimeout == 0 {
		c.Server.IdleTimeout = 120 * time.Second
	}
	if c.Server.ShutdownTimeout == 0 {
		c.Server.ShutdownTimeout = 120 * time.Second
	}
	// Trailing slashes would double up when the route path is appended.
	c.Server.PublicBaseURL = strings.TrimRight(strings.TrimSpace(c.Server.PublicBaseURL), "/")
	c.Server.BasePath = strings.TrimRight(strings.TrimSpace(c.Server.BasePath), "/")

	if c.Storage.DataDir == "" {
		c.Storage.DataDir = filepath.Join("data", "storage")
	}
	if c.Storage.VarDir == "" {
		c.Storage.VarDir = filepath.Join("data", "var")
	}
	if c.Storage.MaxChunkSize <= 0 {
		c.Storage.MaxChunkSize = 10 * 1024 * 1024
	}
	// A negative floor means no floor, not a floor of zero bytes.
	if c.Storage.MinFreeBytes < 0 {
		c.Storage.MinFreeBytes = 0
	}
	if c.Storage.DefaultTTL <= 0 {
		c.Storage.DefaultTTL = 48 * time.Hour
	}
	if c.Storage.MaxTTL <= 0 {
		c.Storage.MaxTTL = 7 * 24 * time.Hour
	}
	if !isSet("storage.fsync") {
		c.Storage.Fsync = true
	}

	if !isSet("gc.interval") {
		c.GC.Interval = time.Hour
	}
	if c.GC.MaxDeletes <= 0 {
		c.GC.MaxDeletes = 200
	}
	if c.Upload.OpenUploadQuotaBytesPerDay < 0 {
		c.Upload.OpenUploadQuotaBytesPerDay = 0
	}

	c.APIKeys = normaliseKeys(c.RawKeys)
}

// normaliseKeys turns the api_keys mapping into a deterministically ordered
// slice.
//
// Sorted by id rather than left in map order: Go randomises map iteration, and
// the auth loop must not depend on it. Two entries sharing a secret then
// resolve to the same id on every request instead of a different one each time.
func normaliseKeys(raw map[string]RawAPIKey) []APIKey {
	keys := make([]APIKey, 0, len(raw))

	for id, spec := range raw {
		// A key called "anonymous" would be able to delete every chunk an open
		// server accepted without a credential. Never load one.
		if id == AnonymousKeyID {
			continue
		}
		if spec.Secret == "" {
			continue
		}

		// A config predating the enabled field means enabled, not disabled.
		enabled := true
		if spec.Enabled != nil {
			enabled = *spec.Enabled
		}

		quota := spec.QuotaBytesPerDay
		if quota < 0 {
			quota = 0
		}

		keys = append(keys, APIKey{
			ID:               id,
			Secret:           spec.Secret,
			QuotaBytesPerDay: quota,
			Enabled:          enabled,
		})
	}

	sort.Slice(keys, func(i, j int) bool { return keys[i].ID < keys[j].ID })

	return keys
}

// validate rejects what the server cannot run with. Messages name the dotted
// config key, not the Go field, because that is what the operator has to edit.
func (c *Config) validate() error {
	switch c.Server.Mode {
	case "debug", "release", "test":
	default:
		return fmt.Errorf("server.mode must be one of debug, release, test (got %q)", c.Server.Mode)
	}

	if !basePathPattern.MatchString(c.Server.BasePath) {
		return fmt.Errorf("server.base_path must be empty or a rooted path like /cache (got %q)", c.Server.BasePath)
	}

	if c.Server.PublicBaseURL != "" {
		if len(c.Server.PublicBaseURL) > maxPublicBaseURL {
			return fmt.Errorf("server.public_base_url must be at most %d characters, so the chunk URLs built from it stay under the client's 1024-character limit", maxPublicBaseURL)
		}
		if !strings.HasPrefix(c.Server.PublicBaseURL, "http://") && !strings.HasPrefix(c.Server.PublicBaseURL, "https://") {
			return fmt.Errorf("server.public_base_url must be an absolute http:// or https:// URL (got %q)", c.Server.PublicBaseURL)
		}
	}

	if c.Storage.MaxTTL < c.Storage.DefaultTTL {
		return fmt.Errorf("storage.max_ttl (%s) cannot be lower than storage.default_ttl (%s)", c.Storage.MaxTTL, c.Storage.DefaultTTL)
	}

	return nil
}

// Warnings are conditions worth telling the operator about that are not fatal.
// serve logs each one once at startup.
func (c *Config) Warnings() []string {
	var out []string

	if c.GCProbability != nil {
		out = append(out, "gcProbability is ignored: this build sweeps on a timer (gc.interval, default 1h). Remove the key, or set gc.interval: 0 to keep using cron.")
	}

	// The client's chunk download is a hand-built request on a bare socket with
	// no TLS, dialling parsed.port(80). An https chunk URL therefore never
	// completes, and the symptom is "publishing works, every peer fails".
	if strings.HasPrefix(c.Server.PublicBaseURL, "https://") {
		out = append(out, "server.public_base_url is https, but eMuleQt downloads chunks over a plain socket with no TLS. Peers will fail to fetch. Terminate TLS elsewhere and pin an http:// URL here.")
	}

	if c.Server.PublicBaseURL == "" {
		out = append(out, "server.public_base_url is not set, so chunk URLs are derived from each request's Host header. Pin it when anything sits in front of this server.")
	}

	if len(c.APIKeys) == 0 && !c.Upload.OpenUpload {
		out = append(out, "no api_keys are configured and upload.open_upload is off, so no chunk can ever be stored.")
	}

	return out
}

// readEnvFile loads KEY=VALUE lines into the process environment so viper's
// AutomaticEnv picks them up. Missing files are skipped.
func readEnvFile(configPath string, fromFiles ...string) error {
	if len(fromFiles) == 0 {
		fromFiles = []string{".env"}
	}

	for _, name := range fromFiles {
		raw, err := os.ReadFile(path.Join(configPath, name))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}

		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}

			parts := strings.SplitN(line, "=", 2)
			if len(parts) != 2 {
				continue
			}
			if err := os.Setenv(strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])); err != nil {
				return err
			}
		}
	}

	return nil
}

// ParseFile reads one config file through a private viper instance, with no
// environment layering.
//
// The installer uses it to load back what it just wrote and compare every field
// against the form. That check is the reason a substitution-based generator is
// safe at all: a needle that silently stopped matching cannot survive it.
func ParseFile(path string) (*Config, error) {
	v := viper.New()
	v.SetConfigFile(path)

	if err := v.ReadInConfig(); err != nil {
		return nil, errors.Wrap(err, "unable to read config")
	}

	cfg := &Config{}
	hook := mapstructure.ComposeDecodeHookFunc(
		mapstructure.StringToTimeDurationHookFunc(),
		mapstructure.StringToSliceHookFunc(","),
	)
	if err := v.Unmarshal(cfg, viper.DecodeHook(hook)); err != nil {
		return nil, errors.Wrap(err, "unable to unmarshal config")
	}

	// applyDefaults consults the global viper for the two keys whose zero value
	// is meaningful, so those are read from this instance instead.
	cfg.applyDefaultsWith(v.IsSet)
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}
