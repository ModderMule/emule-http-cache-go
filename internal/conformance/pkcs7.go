package conformance

import "bytes"

// pkcs7Pad appends PKCS#7 padding to a block boundary.
//
// Go has no PKCS#7 in the standard library, and the suite needs it for the one
// number the whole contract is shaped around: a 9,728,000-byte eMule part is an
// exact multiple of the 16-byte AES block size, so the padding adds a whole
// extra block and the ciphertext of a full part is always 9,728,016 bytes.
func pkcs7Pad(data []byte, blockSize int) []byte {
	padding := blockSize - len(data)%blockSize

	return append(data, bytes.Repeat([]byte{byte(padding)}, padding)...)
}

// pkcs7Unpad removes PKCS#7 padding, reporting false when the tail is not a
// valid padding block.
func pkcs7Unpad(data []byte, blockSize int) ([]byte, bool) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, false
	}

	padding := int(data[len(data)-1])
	if padding == 0 || padding > blockSize || padding > len(data) {
		return nil, false
	}

	for _, b := range data[len(data)-padding:] {
		if int(b) != padding {
			return nil, false
		}
	}

	return data[:len(data)-padding], true
}
