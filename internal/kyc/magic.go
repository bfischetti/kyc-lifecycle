package kyc

import "bytes"

// MagicBytesLen is how many leading bytes MatchesMagic needs.
const MagicBytesLen = 8

var magic = map[string][]byte{
	"application/pdf": []byte("%PDF-"),
	"image/png":       {0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'},
	"image/jpeg":      {0xFF, 0xD8, 0xFF},
}

// MatchesMagic reports whether head (the first bytes of a file) starts with
// the signature of contentType. It catches, say, an executable renamed to
// .pdf, which a declared content type alone would not.
func MatchesMagic(contentType string, head []byte) bool {
	sig, ok := magic[contentType]
	return ok && bytes.HasPrefix(head, sig)
}
