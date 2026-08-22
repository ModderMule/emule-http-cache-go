// Command emule-http-cache is the reference chunk cache for eMuleQt's HTTP
// Cache feature. An uploader that sees several peers wanting the same 9,728,000
// byte part encrypts it once, POSTs the ciphertext here, and hands the URL and
// key to each peer over the eD2K link, so one upload serves N downloaders.
//
// The server never sees anything useful: no key, no IV, no eD2K file hash, no
// part number, no filename — only opaque blobs of a uniform size.
package main

import "github.com/ModderMule/emule-http-cache-go/cmd"

//go:generate swag init -g main.go -o docs_api --parseInternal

// @title       emule-http-cache API
// @version     1.0
// @description Encrypted chunk cache for eMuleQt's HTTP Cache feature.
// @description An uploader stores one AES-256-CBC blob here and hands its URL to several peers, so a part is uploaded once and fetched many times. The server holds ciphertext it has no key for, and is never told the file hash or the part number. Chunk downloads are unauthenticated by design: the 128-bit chunk id is the capability.
// @BasePath    /
//
// No @schemes is declared on purpose: Swagger UI then issues "Try it out"
// requests over the same protocol the spec was loaded with (https behind a TLS
// terminator, http locally), avoiding mixed-content blocking.
func main() {
	cmd.Execute()
}
