package cagemobile

import (
	"encoding/base64"
	"errors"
	"mime"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxSharedFileBytes bounds a file the phone workbench may hand to the system
// share sheet with {action: "share-file", name, mime, data}.
const MaxSharedFileBytes = 8 << 20

// SharedFile is a validated share request from the phone workbench. The native
// shell writes Data as Name into a private cache directory and opens the
// system share sheet for it.
type SharedFile struct {
	Name string
	Mime string
	Data []byte
}

var errSharedFileTooLarge = errors.New("文件超过 8 MB，无法分享。")

// DecodeSharedFile accepts a single file name without path separators or
// control characters, a type/subtype media type (empty means binary data) and
// standard base64 data of at most MaxSharedFileBytes. Errors can be shown to
// the user.
func DecodeSharedFile(name, mediaType, data string) (*SharedFile, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || len(name) > 255 || !utf8.ValidString(name) ||
		strings.ContainsAny(name, `/\`) || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return nil, errors.New("文件名无效，无法分享。")
	}
	kind := "application/octet-stream"
	if strings.TrimSpace(mediaType) != "" {
		parsed, _, err := mime.ParseMediaType(mediaType)
		if err != nil || len(parsed) > 127 || strings.Count(parsed, "/") != 1 {
			return nil, errors.New("文件类型无效，无法分享。")
		}
		kind = parsed
	}
	if len(data) > base64.StdEncoding.EncodedLen(MaxSharedFileBytes) {
		return nil, errSharedFileTooLarge
	}
	content, err := base64.StdEncoding.Strict().DecodeString(data)
	if err != nil {
		return nil, errors.New("文件内容无效，无法分享。")
	}
	if len(content) > MaxSharedFileBytes {
		return nil, errSharedFileTooLarge
	}
	return &SharedFile{Name: name, Mime: kind, Data: content}, nil
}
