// Package synthzip generates virtual ZIP archives on the fly from pre-known
// file metadata, without ever materialising the full archive in memory or on
// disk.
//
// The caller provides a list of [File] entries (name, size, CRC-32) to [New],
// which pre-computes the archive layout. The returned [Archive] implements
// [io.ReaderAt], [io.Reader], and [io.Seeker].
//
// Archives use the STORE method (no compression) and produce output compatible
// with any conformant ZIP reader. All files are written with Unix mode 0644
// and a modification time of 1980-01-01 00:00:00 (the ZIP epoch).
//
// The open function passed to [New] is called lazily and only for byte ranges
// that include file data. synthzip will skip past any leading bytes it does
// not need, using ReadAt if the returned value implements io.ReaderAt, and read
// only the required portion, then close the reader. The callback may be invoked
// multiple times in a single [Archive.ReadAt] call if the range spans multiple
// files.
//
// File sizes are limited to 4 GiB (ZIP32 format). ZIP64 extensions are not
// supported. CRC-32 checksums must be provided for all non-empty files at
// construction time.
package synthzip
