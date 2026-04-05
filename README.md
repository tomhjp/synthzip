# synthzip

[![Go Reference](https://pkg.go.dev/badge/github.com/tomhjp/synthzip.svg)](https://pkg.go.dev/github.com/tomhjp/synthzip)

synthzip generates virtual ZIP archives on the fly from pre-known file metadata,
without ever materialising the full archive in memory or on disk.

## Key properties

- **Zero dependencies** beyond the Go standard library.
- **Arbitrary byte-range reads.** `ReadAt` can serve any `[offset, offset+len)`
  slice of the archive independently. File content is fetched on demand only
  when the requested range overlaps a file data region.
- **Standard-compatible output.** Archives use the STORE method (no compression)
  and are readable by Go's `archive/zip`, `unzip`, and other compliant readers.

## Usage

```go
// Build the archive layout from known metadata.
files := []synthzip.File{
    {Name: "hello.txt", Size: 12, CRC32: 0xaf083b2d},
    {Name: "world.txt", Size: 6,  CRC32: 0x6d86b7f6},
}
archive, _ := synthzip.New(files, os.Open)

// Total size is available immediately, no I/O needed.
fmt.Println(archive.Size())

// Serve a byte-range read. The open callback is only called when the
// requested range overlaps actual file content.
buf := make([]byte, 64)
n, _ := archive.ReadAt(buf, 0)
```

## Limitations

- File sizes are limited to 4 GiB (ZIP32). ZIP64 extensions are not supported.
- CRC-32 must be provided upfront for all non-empty files.

## License

BSD 3-Clause. See [LICENSE](LICENSE).
