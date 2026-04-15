// synthzip builds a virtual, read-only ZIP archive from a static set of file
// metadata without materialising the full archive. It loads file contents lazily
// via a user-provided function, so memory consumption is proportional to the
// metadata, not file contents.
//
// Callers must provide file metadata upfront as a list of [File] entries
// (name, size, and optionally CRC-32) to [New]. The returned [Archive]
// implements [io.Reader], [io.Seeker], and [io.ReaderAt].
//
// Archives use the STORE method (no compression). If the CRC-32 isn't provided
// for any non-empty file, the zip file will be constructed with zeroed CRC-32
// fields for that file. Some tools, such as go's archive/zip package, will
// successfully read the resulting zip without error, but other tools may
// produce warnings or errors. For maximum compatibility, provide CRC-32
// checksums for all non-empty files. All files are written with Unix mode 0644
// and a modification time of 1980-01-01 00:00:00 (the ZIP epoch).
//
// The open function passed to [New] is called lazily and only for byte ranges
// that include file data. synthzip will skip past any leading bytes it does
// not need, using [io.ReaderAt.ReadAt] if the returned value implements
// [io.ReaderAt], and read only the required portion, then close the reader.
// The callback may be invoked multiple times in a single [Archive.ReadAt]
// call if the range spans multiple files.
//
// File sizes are limited to 4 GiB (ZIP32 format). ZIP64 extensions are not
// supported.
package synthzip
