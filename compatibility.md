# Zip tool compatibility

synthzip supports omitting the CRC-32 checksum from file metadata. When CRC-32
is omitted, the archive writes `0x00000000` in the CRC-32 field of both the
local file header and central directory entry. This avoids needing to read file
contents upfront, but many zip tools will at least warn or maybe error.

## Test setup

A test archive was created with four files (text, nested path, empty, binary),
all with CRC-32 omitted. Each tool was tested for its ability to list and
extract the archive contents.

## Results

| Tool              | Version tested       | Extract | Notes                                               |
|-------------------|----------------------|---------|-----------------------------------------------------|
| Go `archive/zip`  | go1.24.2             | OK      | Skips CRC check when central dir CRC-32 is 0        |
| busybox `unzip`   | 1.37.0               | OK      | No CRC verification                                 |
| Info-ZIP `unzip`  | 6.0                  | WARN    | `bad CRC` warnings but extracts files (exit code 2) |
| `bsdtar`          | libarchive 3.7.7     | WARN    | `ZIP bad CRC` warnings but extracts files (exit 1)  |
| 7-Zip (`7z`)      | 25.01                | FAIL    | `CRC Failed` error, does not extract                |
| Python `zipfile`  | 3.13                 | FAIL    | `BadZipFile: Bad CRC-32` exception                  |

An alternative using ZIP data descriptors (bit 3 flag with the real CRC-32
after the file data, zero in the central directory) was also tested. It was
strictly worse: Go's `archive/zip` rejected it too, since it compares the data
descriptor CRC against the central directory value.

## Recommendation

Only omit CRC-32 when the archive consumer is known to be Go's `archive/zip`
or another reader that tolerates a zero CRC-32. When broad tool compatibility
is required, provide the CRC-32 for all non-empty files.
