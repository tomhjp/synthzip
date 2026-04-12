package synthzip

import (
	"archive/zip"
	"bytes"
	"fmt"
	"hash/crc32"
	"io"
	"path/filepath"
	"testing"
)

func TestByteForByteAgainstStdLib(t *testing.T) {
	cases := []struct {
		name  string
		files map[string][]byte
	}{
		{"empty_archive", nil},
		{"single_empty_file", map[string][]byte{
			"empty.txt": nil,
		}},
		{"single_file", map[string][]byte{
			"hello.txt": []byte("hello world"),
		}},
		{"empty_directory", map[string][]byte{
			"dir/": nil,
		}},
		{"mixed_empty_and_nonempty", map[string][]byte{
			"emptyfile":           nil,
			"empty/dir/structure": nil,
			"file1":               fmt.Appendf(nil, "file1 contents"),
			"dir/file2":           fmt.Appendf(nil, "file2 contents"),
		}},
		{"nested_paths", map[string][]byte{
			"a/b/c/d.txt": []byte("deep"),
			"a/b/e.txt":   []byte("mid"),
			"f.txt":       []byte("top"),
		}},
		{"large_file", map[string][]byte{
			"big.bin": bytes.Repeat([]byte("abcdefghij"), 10000),
		}},
		{"many_small_files", map[string][]byte{
			"01": []byte("1"),
			"02": []byte("22"),
			"03": []byte("333"),
			"04": []byte("4444"),
			"05": []byte("55555"),
			"06": []byte("666666"),
			"07": []byte("7777777"),
			"08": []byte("88888888"),
		}},
		{"long_filename", map[string][]byte{
			"example.com/very/deeply/nested/module@v1.0.0/internal/pkg/subpkg/file.go": []byte("package subpkg\n"),
		}},
		{"binary_content", map[string][]byte{
			"bin": {0x00, 0x01, 0xff, 0xfe, 0x80, 0x7f},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files, open := testFiles(tc.files)
			a, err := New(files, open)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			verifyWithStdlib(t, a, tc.files)
		})
	}
}

func verifyWithStdlib(t *testing.T, a *Archive, contents map[string][]byte) {
	t.Helper()

	// Build a reference zip using archive/zip with the same files in the
	// same order as the synthetic archive.
	var ref bytes.Buffer
	zw := zip.NewWriter(&ref)
	for i := range a.files {
		f := &a.files[i]
		hdr := &zip.FileHeader{
			Name:               f.Name,
			Method:             zip.Store,
			CompressedSize64:   uint64(f.Size),
			UncompressedSize64: uint64(f.Size),
			CRC32:              f.CRC32,
			CreatorVersion:     0x0314, // Unix, 2.0
			ReaderVersion:      20,     // 2.0
			ExternalAttrs:      0o644 << 16,
			ModifiedTime:       0,      // 00:00:00
			ModifiedDate:       0x0021, // 1980-01-01
		}

		w, err := zw.CreateRaw(hdr)
		if err != nil {
			t.Fatalf("CreateRaw(%q): %v", f.Name, err)
		}
		if f.Size > 0 {
			if _, err := w.Write(contents[f.Name]); err != nil {
				t.Fatalf("Write(%q): %v", f.Name, err)
			}
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip.Writer.Close: %v", err)
	}

	// Read the full synthetic archive.
	got := make([]byte, a.Size())
	if _, err := a.ReadAt(got, 0); err != nil && err != io.EOF {
		t.Fatalf("ReadAt: %v", err)
	}

	want := ref.Bytes()
	if !bytes.Equal(got, want) {
		t.Errorf("synthetic zip (%d bytes) differs from stdlib zip (%d bytes)", len(got), len(want))
		// Show first differing byte for debugging.
		n := min(len(want), len(got))
		for i := range n {
			if got[i] != want[i] {
				t.Errorf("first difference at byte %d: got %#02x, want %#02x", i, got[i], want[i])
				break
			}
		}
	}
}

// TestPartialReads verifies that reading the zip in small chunks via
// repeated ReadAt calls produces the same result as a single full read.
func TestPartialReads(t *testing.T) {
	files, open := testFiles(map[string][]byte{
		"a.txt": []byte("hello world"),
		"b.txt": []byte("goodbye world, this is a longer file to ensure we span regions"),
		"c.txt": bytes.Repeat([]byte("x"), 1000),
	})
	a, err := New(files, open)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	fullData, err := io.ReadAll(a)
	if err != nil {
		t.Fatal(err)
	}

	// Read in various chunk sizes
	for _, chunkSize := range []int{1, 7, 13, 64, 256} {
		t.Run(fmt.Sprintf("chunk_%d", chunkSize), func(t *testing.T) {
			var assembled []byte
			off := int64(0)
			size := a.Size()
			for off < size {
				n := int64(chunkSize)
				if off+n > size {
					n = size - off
				}
				buf := make([]byte, n)
				got, err := a.ReadAt(buf, off)
				if err != nil && err != io.EOF {
					t.Fatalf("ReadAt(off=%d, len=%d): %v", off, n, err)
				}
				assembled = append(assembled, buf[:got]...)
				off += int64(got)
			}

			if !bytes.Equal(assembled, fullData) {
				t.Errorf("chunk size %d: assembled %d bytes differs from full read %d bytes",
					chunkSize, len(assembled), len(fullData))
			}
		})
	}
}

// TestHeaderOnlyReads verifies that reading byte ranges that fall entirely
// within header/metadata regions does not invoke the opener.
func TestHeaderOnlyReads(t *testing.T) {
	files, _ := testFiles(map[string][]byte{
		"test.txt": []byte("content"),
	})

	opened := false
	noOpen := func(name string) (io.ReadCloser, error) {
		opened = true
		return nil, fmt.Errorf("opener should not be called, but called for %s", name)
	}

	a, err := New(files, noOpen)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Read just the first 30 bytes (the local file header fixed portion).
	// The local header is 30 + len("test.txt") = 38 bytes.
	// File data starts at offset 38.
	buf := make([]byte, 30)
	_, err = a.ReadAt(buf, 0)
	if err != nil && err != io.EOF {
		t.Fatalf("ReadAt header: %v", err)
	}
	if opened {
		t.Error("opener was called for header-only read")
	}

	// Read from the central directory region (after all local headers + file data).
	// The central directory starts after: 38 (local header) + 7 (file data) = 45.
	// Read a small chunk from the central directory.
	cdOffset := int64(30 + len("test.txt") + len("content"))
	buf2 := make([]byte, 20)
	_, err = a.ReadAt(buf2, cdOffset)
	if err != nil && err != io.EOF {
		t.Fatalf("ReadAt central dir: %v", err)
	}
	if opened {
		t.Error("opener was called for central directory read")
	}
}

// TestEmptyArchive verifies that an archive with zero files produces a
// valid zip that archive/zip can open.
func TestEmptyArchive(t *testing.T) {
	a, err := New(nil, func(name string) (io.ReadCloser, error) {
		return nil, fmt.Errorf("should not be called, but called for %s", name)
	})
	if err != nil {
		t.Fatal(err)
	}

	// An empty zip is just the EOCD record: 22 bytes.
	if a.Size() != 22 {
		t.Errorf("empty archive size = %d, want 22", a.Size())
	}

	zipData, err := io.ReadAll(a)
	if err != nil {
		t.Fatal(err)
	}

	zr, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != 0 {
		t.Errorf("empty zip has %d files, want 0", len(zr.File))
	}
}

func TestSize(t *testing.T) {
	cases := []struct {
		name  string
		files map[string][]byte
	}{
		{"empty", nil},
		{"one_empty_file", map[string][]byte{"e.txt": nil}},
		{"one_empty_dir", map[string][]byte{"e/": nil}},
		{"one_file", map[string][]byte{"a.txt": []byte("hi")}},
		{"several_files", map[string][]byte{
			"a.txt":   []byte("aaa"),
			"bb.txt":  []byte("bbbbb"),
			"ccc.txt": []byte("ccccccc"),
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files, open := testFiles(tc.files)
			a, err := New(files, open)
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			// Read one byte past the end to check for exact size.
			buf := make([]byte, a.Size()+1)
			n, err := a.ReadAt(buf, 0)
			if err != io.EOF {
				t.Fatalf("ReadAt: %v", err)
			}
			if int64(n) != a.Size() {
				t.Errorf("ReadAt returned %d bytes, Size() = %d", n, a.Size())
			}
		})
	}
}

func TestNewValidation(t *testing.T) {
	t.Run("empty_name", func(t *testing.T) {
		if _, err := New([]File{{Name: "", Size: 5, CRC32: 123}}, nil); err == nil {
			t.Error("expected error for empty name")
		}
	})

	t.Run("rooted_path", func(t *testing.T) {
		absPath, err := filepath.Abs("a.txt")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := New([]File{{Name: absPath, Size: 0, CRC32: 0}}, nil); err == nil {
			t.Error("expected error for absolute path")
		}
	})

	t.Run("negative_size", func(t *testing.T) {
		if _, err := New([]File{{Name: "a.txt", Size: -1, CRC32: 123}}, nil); err == nil {
			t.Error("expected error for negative size")
		}
	})

	t.Run("zero_size_zero_crc_ok", func(t *testing.T) {
		if _, err := New([]File{{Name: "a.txt", Size: 0, CRC32: 0}}, nil); err != nil {
			t.Errorf("unexpected error for zero-size file with zero CRC32: %v", err)
		}
	})
}

func TestReadAtBeyondEnd(t *testing.T) {
	files, open := testFiles(map[string][]byte{
		"a.txt": []byte("hello"),
	})

	a, err := New(files, open)
	if err != nil {
		t.Fatal(err)
	}

	// Read starting at the very end
	buf := make([]byte, 10)
	n, err := a.ReadAt(buf, a.Size())
	if err != io.EOF {
		t.Errorf("ReadAt at end: err = %v, want io.EOF", err)
	}
	if n != 0 {
		t.Errorf("ReadAt at end: n = %d, want 0", n)
	}

	// Read starting past the end
	n, err = a.ReadAt(buf, a.Size()+100)
	if err != io.EOF {
		t.Errorf("ReadAt past end: err = %v, want io.EOF", err)
	}
	if n != 0 {
		t.Errorf("ReadAt past end: n = %d, want 0", n)
	}
}

func testFiles(contents map[string][]byte) ([]File, func(string) (io.ReadCloser, error)) {
	var files []File
	for name, data := range contents {
		files = append(files, File{
			Name:  name,
			Size:  int64(len(data)),
			CRC32: crc32.ChecksumIEEE(data),
		})
	}
	open := func(name string) (io.ReadCloser, error) {
		data, ok := contents[name]
		if !ok {
			return nil, fmt.Errorf("file not found: %s", name)
		}
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	return files, open
}
