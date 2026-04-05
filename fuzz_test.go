package synthzip

import (
	"bytes"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"strings"
	"testing"
)

// FuzzByteForByte generates random file lists and verifies the synthetic zip
// matches a stdlib-produced zip byte-for-byte.
func FuzzByteForByte(f *testing.F) {
	// Seed corpus: each entry is a blob encoding a list of files.
	f.Add(encodeFileList(nil))
	f.Add(encodeFileList([]fileEntry{{name: "a.txt", data: []byte("hello")}}))
	f.Add(encodeFileList([]fileEntry{
		{name: "empty", data: nil},
		{name: "x", data: []byte{0xff}},
		{name: "a/b/c.txt", data: []byte("nested")},
	}))

	f.Fuzz(func(t *testing.T, blob []byte) {
		entries := decodeFileList(blob)
		if len(entries) == 0 {
			return
		}

		contents, files := filesFromEntries(entries)
		open := openerFromMap(contents)
		a, err := New(files, open)
		if err != nil {
			return // invalid input, e.g. empty name
		}
		verifyWithStdlib(t, a, contents)
	})
}

// FuzzReadAt generates random file lists and verifies that arbitrary ReadAt
// calls produce the same bytes as a single full read.
func FuzzReadAt(f *testing.F) {
	f.Add(
		encodeFileList([]fileEntry{{name: "a.txt", data: []byte("hello")}}),
		int64(0), uint16(5),
	)
	f.Add(
		encodeFileList([]fileEntry{
			{name: "x", data: []byte("abc")},
			{name: "y", data: []byte("defgh")},
		}),
		int64(3), uint16(20),
	)

	f.Fuzz(func(t *testing.T, blob []byte, offset int64, length uint16) {
		entries := decodeFileList(blob)
		if len(entries) == 0 {
			return
		}

		contents, files := filesFromEntries(entries)
		open := openerFromMap(contents)
		a, err := New(files, open)
		if err != nil {
			return
		}

		// Read the full archive as reference.
		full := make([]byte, a.Size())
		if _, err := a.ReadAt(full, 0); err != nil && err != io.EOF {
			t.Fatalf("full ReadAt: %v", err)
		}

		// Clamp offset and length to archive bounds.
		if offset < 0 {
			offset = -offset
		}
		if offset > a.Size() {
			return
		}
		n := int64(length)
		if offset+n > a.Size() {
			n = a.Size() - offset
		}
		if n == 0 {
			return
		}

		buf := make([]byte, n)
		got, err := a.ReadAt(buf, offset)
		if err != nil && err != io.EOF {
			t.Fatalf("ReadAt(off=%d, len=%d): %v", offset, n, err)
		}

		want := full[offset : offset+int64(got)]
		if !bytes.Equal(buf[:got], want) {
			t.Errorf("ReadAt(off=%d, len=%d) differs from full read", offset, n)
		}
	})
}

// fileEntry is used by the fuzz helpers to represent a file before constructing
// the []File and content map.
type fileEntry struct {
	name string
	data []byte
}

// encodeFileList serializes a list of file entries into a flat byte slice for
// use as fuzz corpus seeds. Format: [count] then for each file:
// [nameLen uint8] [name] [dataLen uint16-LE] [data].
func encodeFileList(entries []fileEntry) []byte {
	var buf []byte
	buf = append(buf, byte(len(entries)))
	for _, e := range entries {
		buf = append(buf, byte(len(e.name)))
		buf = append(buf, e.name...)
		buf = append(buf, byte(len(e.data)), byte(len(e.data)>>8))
		buf = append(buf, e.data...)
	}
	return buf
}

// decodeFileList parses the format produced by encodeFileList. Returns nil if
// the blob is too short or encodes zero files. File names are sanitized to
// avoid duplicates and empty names.
func decodeFileList(blob []byte) []fileEntry {
	if len(blob) < 1 {
		return nil
	}
	count := int(blob[0])
	if count == 0 {
		return nil
	}
	// Cap to avoid pathologically slow tests.
	if count > 16 {
		count = 16
	}
	blob = blob[1:]

	seen := map[string]bool{}
	var entries []fileEntry
	for i := 0; i < count; i++ {
		if len(blob) < 1 {
			break
		}
		nameLen := int(blob[0])
		blob = blob[1:]
		if nameLen == 0 || nameLen > len(blob) {
			break
		}
		name := string(blob[:nameLen])
		blob = blob[nameLen:]

		if len(blob) < 2 {
			break
		}
		dataLen := int(blob[0]) | int(blob[1])<<8
		blob = blob[2:]
		if dataLen > len(blob) {
			break
		}
		data := blob[:dataLen]
		blob = blob[dataLen:]

		// Sanitize name to valid path characters for the opener.
		name = sanitizeFuzzName(name)
		if name == "" {
			continue
		}

		// Skip duplicates.
		if seen[name] {
			continue
		}
		seen[name] = true

		entries = append(entries, fileEntry{name: name, data: data})
	}
	return entries
}

// sanitizeFuzzName maps raw fuzz bytes to a valid relative file path.
// Returns "" if nothing usable remains.
func sanitizeFuzzName(raw string) string {
	var b []byte
	for _, c := range []byte(raw) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '_', c == '-', c == '@':
			b = append(b, c)
		case c == '/':
			// Avoid leading, trailing, or double slashes.
			if len(b) > 0 && b[len(b)-1] != '/' {
				b = append(b, '/')
			}
		}
	}
	s := strings.TrimRight(string(b), "/")
	if !fs.ValidPath(s) || s == "." {
		return ""
	}
	return s
}

func filesFromEntries(entries []fileEntry) (map[string][]byte, []File) {
	contents := make(map[string][]byte, len(entries))
	var files []File
	for _, e := range entries {
		contents[e.name] = e.data
		files = append(files, File{
			Name:  e.name,
			Size:  int64(len(e.data)),
			CRC32: crc32.ChecksumIEEE(e.data),
		})
	}
	return contents, files
}

func openerFromMap(contents map[string][]byte) func(string) (io.ReadCloser, error) {
	return func(name string) (io.ReadCloser, error) {
		data, ok := contents[name]
		if !ok {
			return nil, fmt.Errorf("file not found: %s", name)
		}
		return io.NopCloser(bytes.NewReader(data)), nil
	}
}
