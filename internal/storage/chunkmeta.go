package storage

import "time"

// ChunkMeta is the metadata sidecar of one stored chunk.
//
// The field order is load-bearing: encoding/json marshals in declaration order,
// and this is the order the PHP server writes, so a sidecar written here is
// byte-identical to one written there. Marshal it with json.Marshal, never with
// json.Encoder.Encode, which appends a newline PHP does not write.
type ChunkMeta struct {
	ID         string `json:"id"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	OwnerKeyID string `json:"ownerKeyId"`
	CreatedAt  int64  `json:"createdAt"`
	ExpiresAt  int64  `json:"expiresAt"`
}

// IsExpired reports whether the chunk's lifetime has elapsed. The comparison is
// inclusive, matching the PHP server's expiresAt <= now.
func (m ChunkMeta) IsExpired(now time.Time) bool {
	return m.ExpiresAt <= now.Unix()
}

// ETag is the entity tag served for this chunk: the first 32 hex characters of
// the ciphertext digest, double-quoted.
func (m ChunkMeta) ETag() string {
	digest := m.SHA256
	if len(digest) > 32 {
		digest = digest[:32]
	}

	return `"` + digest + `"`
}

// MaxAge is how long a cache may still hold this chunk, never negative.
func (m ChunkMeta) MaxAge(now time.Time) int64 {
	remaining := m.ExpiresAt - now.Unix()
	if remaining < 0 {
		return 0
	}

	return remaining
}
