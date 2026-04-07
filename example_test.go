package synthzip_test

import (
	"archive/zip"
	"bytes"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"

	"github.com/tomhjp/synthzip"
)

func Example() {
	dir, _ := os.MkdirTemp("", "synthzip-example")
	defer os.RemoveAll(dir)
	os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello world\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# My Project\n"), 0o644)

	// Compute the metadata needed to construct the archive.
	var files []synthzip.File
	for _, name := range []string{"hello.txt", "README.md"} {
		data, _ := os.ReadFile(filepath.Join(dir, name))
		files = append(files, synthzip.File{
			Name:  name,
			Size:  int64(len(data)),
			CRC32: crc32.ChecksumIEEE(data),
		})
	}

	// The open function is called lazily by the archive when it needs to read.
	open := func(name string) (io.ReadCloser, error) {
		return os.Open(filepath.Join(dir, name))
	}

	// Construct the archive. This pre-computes the layout but does not read
	// any file contents yet.
	a, _ := synthzip.New(files, open)

	// Size is known immediately, no I/O needed.
	fmt.Printf("archive size: %d bytes\n", a.Size())

	// The archive is readable by Go's archive/zip.
	zr, _ := zip.NewReader(a, a.Size())
	for _, zf := range zr.File {
		rc, _ := zf.Open()
		data, _ := io.ReadAll(rc)
		rc.Close()
		fmt.Printf("%s: %s", zf.Name, data)
	}

	// Output:
	// archive size: 235 bytes
	// hello.txt: hello world
	// README.md: # My Project
}

func ExampleNew() {
	data := []byte("hello world\n")

	files := []synthzip.File{
		{Name: "hello.txt", Size: int64(len(data)), CRC32: crc32.ChecksumIEEE(data)},
	}
	open := func(name string) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	}

	a, _ := synthzip.New(files, open)
	fmt.Printf("size: %d bytes\n", a.Size())

	// Output:
	// size: 128 bytes
}
