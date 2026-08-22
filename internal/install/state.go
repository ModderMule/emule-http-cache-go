// Package install writes the server's config file on behalf of an operator who
// has a browser, or a shell, and nothing else.
//
// config.yaml is built out of the *text* of config.example.yaml rather than
// emitted from scratch, so the sample's comments — the only documentation those
// tuning knobs have — end up in the operator's real config. That makes the
// generator a set of substitutions, which is only safe because the last step
// loads the result back and compares every field: a substitution that silently
// missed cannot survive it.
package install

import (
	"encoding/json"
	"os"
	"time"
)

// markerFile records that this config was machine-generated.
const markerFile = "install.json"

// State is the install marker, var/install.json.
//
// It records that *this* config.yaml was machine-generated and whether its key
// has been shown yet. It deliberately holds no secret: the key lives in
// config.yaml and nowhere else.
//
// Disclosure is gated on this file rather than on config.yaml, so a config
// written by hand — which has no marker — can never be read back over HTTP.
type State struct {
	GeneratedAt int64  `json:"generatedAt"`
	KeyID       string `json:"keyId"`
	ClaimedAt   *int64 `json:"claimedAt"`
}

// IsClaimed reports whether the key has already been shown once.
func (s State) IsClaimed() bool {
	return s.ClaimedAt != nil
}

// ClaimedAtTime is when the key was shown, for the page that says so.
func (s State) ClaimedAtTime() time.Time {
	if s.ClaimedAt == nil {
		return time.Time{}
	}

	return time.Unix(*s.ClaimedAt, 0).UTC()
}

// readState loads the marker, reporting false when there is none or it is
// unusable.
func readState(path string) (*State, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}

	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, false
	}
	if state.GeneratedAt <= 0 || state.KeyID == "" {
		return nil, false
	}

	return &state, true
}

// writeState persists the marker.
func writeState(path string, state *State) error {
	encoded, err := json.MarshalIndent(state, "", "    ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, append(encoded, '\n'), 0o600)
}
