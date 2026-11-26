package web

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"io"
	"io/fs"
	"path"
	"strings"
	"unicode"

	"github.com/openhoo/shoowcase/internal/sites"
)

// Allow the small container overhead for an uncompressed 8 MiB site.
const maxSiteArchive = sites.MaxRevisionBytes + 512<<10
const maxArchiveEntries = 512 // File count is independently limited to 100.

type consoleFile struct {
	Path   string `json:"path"`
	Base64 string `json:"base64"`
}

func badFile(message string) error { return problem("invalid_argument", message) }

func boundedBytes(r io.Reader, limit int64) ([]byte, error) {
	b, e := io.ReadAll(io.LimitReader(r, limit+1))
	if e != nil {
		return nil, badFile("The file could not be read. Choose it again.")
	}
	if int64(len(b)) > limit {
		return nil, badFile("The file exceeds the size limit.")
	}
	return b, nil
}

// Verify standard single-disk ZIP directory bounds before archive/zip allocates
// an entry object for every central directory header. Do not trust the declared
// entry count alone; it can be smaller than the actual number of headers.
func boundedDirectory(data []byte) error {
	malformed := badFile("Choose a standard ZIP archive. Split, encrypted and ZIP64 archives are not supported.")
	le := binary.LittleEndian
	for i := len(data) - 22; i >= 0 && i >= len(data)-22-65535; i-- {
		if data[i] != 0x50 || le.Uint32(data[i:]) != 0x06054b50 {
			continue
		}
		if int(le.Uint16(data[i+20:])) != len(data)-i-22 {
			return malformed
		}
		count := int(le.Uint16(data[i+10:]))
		size, start := uint64(le.Uint32(data[i+12:])), uint64(le.Uint32(data[i+16:]))
		if le.Uint32(data[i+4:]) != 0 || int(le.Uint16(data[i+8:])) != count || count > maxArchiveEntries || size > 512<<10 || start+size != uint64(i) {
			return malformed
		}
		central := data[int(start):i]
		for n := 0; n < count; n++ {
			if len(central) < 46 || le.Uint32(central) != 0x02014b50 || le.Uint16(central[34:]) != 0 {
				return malformed
			}
			length := 46 + int(le.Uint16(central[28:])) + int(le.Uint16(central[30:])) + int(le.Uint16(central[32:]))
			if length > len(central) {
				return malformed
			}
			central = central[length:]
		}
		if len(central) != 0 {
			return malformed
		}
		return nil
	}
	return malformed
}

func consoleZIP(data []byte) ([]consoleFile, error) {
	if len(data) > maxSiteArchive {
		return nil, badFile("The ZIP exceeds the upload limit.")
	}
	if e := boundedDirectory(data); e != nil {
		return nil, e
	}
	z, e := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if e != nil {
		return nil, badFile("The ZIP could not be opened.")
	}
	var entries []*zip.File
	var dirs []string
	var total uint64
	seen := make(map[string]bool)
	for _, f := range z.File {
		name := f.Name
		mode := f.Mode()
		if f.Flags&1 != 0 || (mode.Type() != 0 && mode.Type() != fs.ModeDir) || (f.Method != zip.Store && f.Method != zip.Deflate) {
			return nil, badFile("Use ordinary files and folders in the ZIP. Links, encryption and this compression method are not supported.")
		}
		entry := strings.TrimSuffix(name, "/")
		if entry == "" || entry == "." || len(entry) > 456 || strings.HasPrefix(entry, "/") || strings.ContainsAny(entry, "\\:") || strings.ContainsFunc(entry, unicode.IsControl) || path.Clean(entry) != entry || strings.HasPrefix(entry, "../") || entry == ".." || seen[entry] {
			return nil, badFile("Each ZIP entry needs a unique relative path without dot segments.")
		}
		seen[entry] = true
		if f.UncompressedSize64 > sites.MaxRevisionBytes || total+f.UncompressedSize64 > sites.MaxRevisionBytes {
			return nil, badFile("The unpacked ZIP exceeds 8 MiB.")
		}
		total += f.UncompressedSize64
		// Finder adds these entries on macOS. They are never read or published.
		if entry == "__MACOSX" || strings.HasPrefix(entry, "__MACOSX/") || path.Base(entry) == ".DS_Store" {
			continue
		}
		if mode.IsDir() {
			if !strings.HasSuffix(name, "/") || f.UncompressedSize64 != 0 {
				return nil, badFile("A ZIP folder contains file data.")
			}
			dirs = append(dirs, entry)
			continue
		}
		if len(entries) >= sites.MaxFiles {
			return nil, badFile("A ZIP may contain up to 100 files.")
		}
		entries = append(entries, f)
	}
	if len(entries) == 0 {
		return nil, badFile("The ZIP has no site files.")
	}
	// Accept the common “Compress folder” output without publishing a nested
	// homepage. A partial upload or an archive with multiple roots keeps its paths.
	root, _, nested := strings.Cut(entries[0].Name, "/")
	strip, homepage := nested, false
	for _, f := range entries {
		homepage = homepage || f.Name == root+"/index.html"
		if !strings.HasPrefix(f.Name, root+"/") {
			strip = false
			break
		}
	}
	strip = strip && homepage
	for _, dir := range dirs {
		if strip && dir == root {
			continue
		}
		if strip {
			dir = strings.TrimPrefix(dir, root+"/")
		}
		if _, e := sites.UploadTarget(dir+"/a.css", 0); e != nil {
			return nil, e
		}
	}
	result := make([]consoleFile, 0, len(entries))
	for _, f := range entries {
		name := f.Name
		if strip {
			name = strings.TrimPrefix(name, root+"/")
		}
		if _, e := sites.UploadTarget(name, int64(f.UncompressedSize64)); e != nil {
			return nil, badFile("Cannot import " + name + ": " + e.Error())
		}
		reader, e := f.Open()
		if e != nil {
			return nil, badFile("The ZIP entry could not be opened: " + name)
		}
		b, e := boundedBytes(reader, int64(f.UncompressedSize64))
		closeErr := reader.Close()
		if e != nil || closeErr != nil || uint64(len(b)) != f.UncompressedSize64 {
			return nil, badFile("The ZIP entry failed its size or integrity check: " + name)
		}
		result = append(result, consoleFile{name, base64.StdEncoding.EncodeToString(b)})
	}
	return result, nil
}
