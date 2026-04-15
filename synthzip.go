package synthzip

import (
	"encoding/binary"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"path/filepath"
	"sort"
	"sync"
)

// See zip spec at https://pkware.cachefly.net/webdocs/casestudies/APPNOTE.TXT

// File describes a file to include in the synthetic zip archive.
type File struct {
	// Name is the full path in the zip (e.g. "foo/bar/main.go"), passed to the
	// open function when its contents need to be read. It cannot be an absolute
	// path, and is always forward slash-separated, even on Windows.
	Name string

	// Size is the uncompressed file size in bytes. It will remain uncompressed
	// in the zip archive (method STORE).
	Size int64

	// CRC32 is the CRC-32 checksum of the file content using the IEEE polynomial.
	// Required for files with Size > 0.
	CRC32 uint32
}

func (f *File) lazyCRC32() bool {
	return f.Size > 0 && f.CRC32 == 0
}

type file struct {
	File

	mu        sync.Mutex  // Protects the remaining fields.
	bytesRead int64       // The number of bytes read into h so far.
	h         hash.Hash32 // Bytes are summed into h as they are read if CRC-32 is not provided.
}

func (f *file) ensureFullyRead(open func(string) (io.ReadCloser, error)) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.bytesRead >= f.Size {
		return nil
	}

	rc, err := open(f.Name)
	if err != nil {
		return err
	}
	defer rc.Close()

	// Skip over already-read bytes.
	if f.bytesRead > 0 {
		rs, ok := rc.(io.Seeker)
		if ok {
			if _, err := rs.Seek(f.bytesRead, io.SeekStart); err != nil {
				return err
			}
		} else {
			if _, err := io.CopyN(io.Discard, rc, f.bytesRead); err != nil {
				return err
			}
		}
	}

	n, err := io.Copy(f.h, rc)
	if err != nil {
		return err
	}
	f.bytesRead += n

	if f.bytesRead != f.Size {
		return fmt.Errorf("synthzip: expected to read %d bytes for file %q, but got %d", f.Size, f.Name, f.bytesRead)
	}

	return nil
}

func (f *file) maybeSumBytes(offset int64, b []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.bytesRead >= f.Size {
		// Already fully summed.
		return
	}

	if offset > f.bytesRead {
		// We must read bytes in order, but the offset is already beyond what
		// we've read up to.
		return
	}

	start := f.bytesRead - offset
	if start >= int64(len(b)) {
		// All the bytes in b are before our current cursor into the underlying file.
		return
	}

	n, err := f.h.Write(b[start:])
	if err != nil {
		f.h.Reset()
		f.bytesRead = 0
		return
	}
	f.bytesRead += int64(n)
}

type regionKind uint8

const (
	regionLocalHeader regionKind = iota
	regionFileData
	regionFileDataDescriptor
	regionCentralDir
	regionEOCD
)

type region struct {
	offset int64
	length int64
	kind   regionKind
	index  int // index into Archive.files
}

var (
	_ io.Reader   = (*Archive)(nil)
	_ io.ReaderAt = (*Archive)(nil)
	_ io.Seeker   = (*Archive)(nil)
	// TODO: maybe implement more of the interfaces that os.File satisfies?
	// _ io.Closer      = (*Archive)(nil)
	// _ fs.File        = (*Archive)(nil)
	// _ fs.ReadDirFile = (*Archive)(nil)
)

// Archive implements a virtual, read-only ZIP archive. Use [New] to construct
// a functional [Archive]. It implements [io.Reader], [io.Seeker], and
// [io.ReaderAt] for reading the archive contents.
type Archive struct {
	files        []file
	regions      []region
	localOffsets []int64 // local header offset per file, needed by central dir entries
	cdOffset     int64
	cdSize       int64
	size         int64
	open         func(name string) (io.ReadCloser, error)

	readOffset int64
}

// New creates a new [Archive] from the given file list. Files appear in the zip
// in the order provided. Returns an error if any file has an empty name,
// negative size, or zero CRC32 with non-zero size. The open function will not
// be called during construction. It will be passed the provided file name from
// files when a read on the returned [Archive] requires its contents.
//
// As an optimisation, the open function may return a type that implements
// [io.ReaderAt], which will be used to skip any leading bytes not needed for
// reads that start within a file's data region.
func New(filesIn []File, open func(name string) (io.ReadCloser, error)) (*Archive, error) {
	var (
		off          int64
		regions      []region
		archiveFiles = make([]file, len(filesIn))
		localOffsets = make([]int64, len(filesIn))
	)
	for i, f := range filesIn {
		if f.Name == "" {
			return nil, fmt.Errorf("synthzip: file %d has empty name", i)
		}
		if f.Name[0] == '/' || filepath.IsAbs(f.Name) {
			return nil, fmt.Errorf("synthzip: file %d (%q) has absolute path", i, f.Name)
		}
		if f.Size < 0 {
			return nil, fmt.Errorf("synthzip: file %d (%q) has negative size", i, f.Name)
		}

		archiveFiles[i] = file{
			File: f,
			h:    crc32.NewIEEE(),
		}

		// Local file header.
		localOffsets[i] = off
		headerLen := 30 + int64(len(f.Name))
		regions = append(regions, region{
			offset: off,
			length: headerLen,
			kind:   regionLocalHeader,
			index:  i,
		})
		off += headerLen

		// File data.
		if f.Size > 0 {
			regions = append(regions, region{
				offset: off,
				length: f.Size,
				kind:   regionFileData,
				index:  i,
			})
			off += f.Size
		}

		// Optional data descriptor.
		if f.lazyCRC32() {
			regions = append(regions, region{
				offset: off,
				length: 16,
				kind:   regionFileDataDescriptor,
				index:  i,
			})
			off += 16
		}
	}

	// Central directory.
	cdOffset := off
	for i, f := range filesIn {
		nameLen := int64(len(f.Name))
		entryLen := 46 + nameLen
		regions = append(regions, region{
			offset: off,
			length: entryLen,
			kind:   regionCentralDir,
			index:  i,
		})
		off += entryLen
	}
	cdSize := off - cdOffset

	// EOCD
	regions = append(regions, region{
		offset: off,
		length: 22,
		kind:   regionEOCD,
	})
	off += 22

	a := &Archive{
		regions:      regions,
		files:        archiveFiles,
		localOffsets: localOffsets,
		open:         open,
		size:         off,
		cdOffset:     cdOffset,
		cdSize:       cdSize,
	}
	return a, nil
}

// Size returns the total size of the zip archive in bytes. This is available
// immediately after construction with no I/O.
func (a *Archive) Size() int64 {
	return a.size
}

// ReadAt reads len(p) bytes starting at byte offset off from the synthetic zip.
// It will lazily read file contents if the range overlaps file data regions. If
// the returned reader implements io.ReaderAt, it will be used to read only the
// required portion of the file instead of reading from the start.
func (a *Archive) ReadAt(p []byte, off int64) (int, error) {
	if off >= a.Size() {
		return 0, io.EOF
	}

	end := min(off+int64(len(p)), a.Size())

	// Find first region overlapping [off, end).
	ri := sort.Search(len(a.regions), func(i int) bool {
		r := &a.regions[i]
		return r.offset+r.length > off
	})

	cur := off
	total := 0
	for ; ri < len(a.regions) && cur < end; ri++ {
		r := &a.regions[ri]
		if r.offset >= end {
			break
		}

		// Overlap: [regionStart, regionEnd)
		regionStart := max(cur, r.offset)
		regionEnd := min(end, r.offset+r.length)

		// Offset within the region's data
		intraOffset := regionStart - r.offset
		n := int(regionEnd - regionStart)
		dst := p[regionStart-off : regionEnd-off]

		switch r.kind {
		case regionLocalHeader:
			hdr := makeLocalHeader(&a.files[r.index])
			copy(dst, hdr[intraOffset:intraOffset+int64(n)])

		case regionCentralDir:
			entry := makeCentralDirEntry(&a.files[r.index], a.localOffsets[r.index])
			copy(dst, entry[intraOffset:intraOffset+int64(n)])

		case regionEOCD:
			eocd := makeEOCD(len(a.files), a.cdOffset, a.cdSize)
			copy(dst, eocd[intraOffset:intraOffset+int64(n)])

		case regionFileDataDescriptor:
			f := &a.files[r.index]
			if f.lazyCRC32() {
				if err := f.ensureFullyRead(a.open); err != nil {
					return total, err
				}
			}
			dd := makeDataDescriptor(f)
			copy(dst, dd[intraOffset:intraOffset+int64(n)])

		case regionFileData:
			f := &a.files[r.index]
			rc, err := a.open(f.Name)
			if err != nil {
				return total, err
			}
			defer rc.Close()

			if ra, ok := rc.(io.ReaderAt); ok && intraOffset > 0 {
				if _, err := ra.ReadAt(dst, intraOffset); err != nil {
					return total, err
				}
			} else {
				if intraOffset > 0 {
					if _, err := io.CopyN(io.Discard, rc, intraOffset); err != nil {
						return total, err
					}
				}
				if _, err := io.ReadFull(rc, dst); err != nil {
					return total, err
				}
			}

			// If dst has bytes we can use for a lazily calculated CRC32, sum them now.
			if f.lazyCRC32() {
				f.maybeSumBytes(intraOffset, dst)
			}
		default:
			return 0, fmt.Errorf("synthzip: unknown region kind %d", r.kind)
		}

		total += n
		cur = regionEnd
	}

	if cur >= a.Size() {
		return total, io.EOF
	}
	return total, nil
}

// Read implements io.Reader, reading sequentially from the current read offset.
func (a *Archive) Read(p []byte) (int, error) {
	n, err := a.ReadAt(p, a.readOffset)
	a.readOffset += int64(n)
	return n, err
}

// Seek implements io.Seeker, adjusting the current read offset.
func (a *Archive) Seek(offset int64, whence int) (int64, error) {
	var base int64
	switch whence {
	case io.SeekCurrent:
		base = a.readOffset
	case io.SeekStart:
		base = 0
	case io.SeekEnd:
		base = a.Size()
	}

	newOffset := base + offset
	if newOffset < 0 {
		return a.readOffset, fmt.Errorf("Seek %d went from %d to %d (invalid)", offset, a.readOffset, newOffset)
	}

	a.readOffset = newOffset

	return a.readOffset, nil
}

func makeLocalHeader(f *file) []byte {
	nameBytes := []byte(f.Name)
	buf := make([]byte, 30+len(nameBytes))
	binary.LittleEndian.PutUint32(buf[0:], 0x04034b50) // signature
	binary.LittleEndian.PutUint16(buf[4:], 20)         // version needed
	makeHeaderCommon(f, buf[6:])
	copy(buf[30:], nameBytes)
	return buf
}

func makeDataDescriptor(f *file) []byte {
	buf := make([]byte, 16)
	binary.LittleEndian.PutUint32(buf[0:], 0x08074b50)      // signature
	binary.LittleEndian.PutUint32(buf[4:], f.h.Sum32())     // crc32
	binary.LittleEndian.PutUint32(buf[8:], uint32(f.Size))  // compressed size
	binary.LittleEndian.PutUint32(buf[12:], uint32(f.Size)) // uncompressed size
	return buf
}

func makeCentralDirEntry(f *file, localOffset int64) []byte {
	nameBytes := []byte(f.Name)
	buf := make([]byte, 46+len(nameBytes))
	binary.LittleEndian.PutUint32(buf[0:], 0x02014b50) // signature
	binary.LittleEndian.PutUint16(buf[4:], 0x0314)     // version made by: Unix, 2.0
	binary.LittleEndian.PutUint16(buf[6:], 20)         // version needed
	makeHeaderCommon(f, buf[8:])
	binary.LittleEndian.PutUint16(buf[32:], 0)                   // file comment length
	binary.LittleEndian.PutUint16(buf[34:], 0)                   // disk number start
	binary.LittleEndian.PutUint16(buf[36:], 0)                   // internal attrs
	binary.LittleEndian.PutUint32(buf[38:], 0o644<<16)           // external attrs: Unix 0644
	binary.LittleEndian.PutUint32(buf[42:], uint32(localOffset)) // local header offset
	copy(buf[46:], nameBytes)
	return buf
}

func makeHeaderCommon(f *file, buf []byte) {
	var flags uint16
	// Does the file use a trailing data descriptor for checksum and size?
	dd := f.lazyCRC32()
	if dd {
		// Spec section 4.4.4; bit 3 indicates CRC-32 and sizes are in a data
		// descriptor after file data.
		flags |= 0x08
	}
	binary.LittleEndian.PutUint16(buf[0:], flags)  // flags
	binary.LittleEndian.PutUint16(buf[2:], 0)      // compression: store
	binary.LittleEndian.PutUint16(buf[4:], 0)      // mod time
	binary.LittleEndian.PutUint16(buf[6:], 0x0021) // mod date: 1980-01-01
	if dd {
		binary.LittleEndian.PutUint32(buf[8:], 0)               // crc32
		binary.LittleEndian.PutUint32(buf[12:], uint32(f.Size)) // compressed size
		binary.LittleEndian.PutUint32(buf[16:], uint32(f.Size)) // uncompressed size
	} else {
		binary.LittleEndian.PutUint32(buf[8:], f.CRC32)         // crc32
		binary.LittleEndian.PutUint32(buf[12:], uint32(f.Size)) // compressed size
		binary.LittleEndian.PutUint32(buf[16:], uint32(f.Size)) // uncompressed size
	}
	binary.LittleEndian.PutUint16(buf[20:], uint16(len(f.Name))) // name length
	binary.LittleEndian.PutUint16(buf[22:], 0)                   // extra field length
}

func makeEOCD(numFiles int, cdOffset, cdSize int64) []byte {
	buf := make([]byte, 22)
	binary.LittleEndian.PutUint32(buf[0:], 0x06054b50)        // signature
	binary.LittleEndian.PutUint16(buf[4:], 0)                 // disk number
	binary.LittleEndian.PutUint16(buf[6:], 0)                 // disk with CD
	binary.LittleEndian.PutUint16(buf[8:], uint16(numFiles))  // entries on disk
	binary.LittleEndian.PutUint16(buf[10:], uint16(numFiles)) // total entries
	binary.LittleEndian.PutUint32(buf[12:], uint32(cdSize))   // CD size
	binary.LittleEndian.PutUint32(buf[16:], uint32(cdOffset)) // CD offset
	binary.LittleEndian.PutUint16(buf[20:], 0)                // comment length
	return buf
}
