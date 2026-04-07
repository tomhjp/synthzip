# synthzip

[![Go Reference](https://pkg.go.dev/badge/github.com/tomhjp/synthzip.svg)](https://pkg.go.dev/github.com/tomhjp/synthzip)

synthzip builds a virtual, read-only ZIP archive from a static set of file
metadata without materialising the full archive. It loads file contents lazily
via a user-provided function, so memory consumption is proportional to the
metadata, not file contents.

## Key properties

- **Zero dependencies** beyond the Go standard library.
- **Arbitrary byte-range reads.** `ReadAt` can serve any `[offset, offset+len)`
  slice of the archive independently. File content is fetched on demand only
  when the requested range overlaps a file data region.
- **Standard-compatible output.** Archives use the STORE method (no compression)
  and are readable by Go's `archive/zip`, `unzip`, and other compliant readers.

## Usage

See [`example_test.go`](example_test.go) for a working example.

## Limitations

- File sizes are limited to 4 GiB (ZIP32). ZIP64 extensions are not supported.
- CRC-32 must be provided upfront for all non-empty files.
