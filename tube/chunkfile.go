package tube

import (
	"bytes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	ChunkSize       = 64 << 10
	chunkTag        = 16
	chunkHeaderSize = 16
	noncePrefixSize = 8
	readAheadChunks = 16
)

var chunkMagic = []byte("SMS\x01")

var ErrBadEncryptedFile = errors.New("not a valid encrypted file")

func chunkCount(plainSize int64) int64 {
	if plainSize <= 0 {
		return 1
	}
	return (plainSize + ChunkSize - 1) / ChunkSize
}

func EncryptedSize(plainSize int64) int64 {
	return chunkHeaderSize + plainSize + chunkCount(plainSize)*chunkTag
}

func PlainSize(encSize int64) int64 {
	body := encSize - chunkHeaderSize
	if body <= chunkTag {
		return 0
	}
	n := (body + ChunkSize + chunkTag - 1) / (ChunkSize + chunkTag)
	return body - n*chunkTag
}

func chunkNonce(prefix []byte, index int64) []byte {
	nonce := make([]byte, noncePrefixSize+4)
	copy(nonce, prefix)
	binary.BigEndian.PutUint32(nonce[noncePrefixSize:], uint32(index))
	return nonce
}

func chunkAD(objectKey string, index int64, final bool) []byte {
	ad := make([]byte, 0, len(objectKey)+9)
	ad = append(ad, objectKey...)
	ad = binary.BigEndian.AppendUint64(ad, uint64(index))
	if final {
		return append(ad, 1)
	}
	return append(ad, 0)
}

func EncryptStream(dst io.Writer, src io.Reader, fileKey []byte, objectKey string) (int64, error) {
	aead := newAEAD(fileKey)
	prefix := randomBytes(noncePrefixSize)
	header := make([]byte, chunkHeaderSize)
	copy(header, chunkMagic)
	binary.BigEndian.PutUint32(header[4:], ChunkSize)
	copy(header[8:], prefix)
	if _, err := dst.Write(header); err != nil {
		return 0, err
	}

	cur, next := make([]byte, ChunkSize), make([]byte, ChunkSize)
	n, err := readChunk(src, cur)
	if err != nil {
		return 0, err
	}
	var total int64
	out := make([]byte, 0, ChunkSize+chunkTag)
	for index := int64(0); ; index++ {
		final := n < ChunkSize
		m := 0
		if !final {
			if m, err = readChunk(src, next); err != nil {
				return total, err
			}
			final = m == 0
		}
		out = aead.Seal(out[:0], chunkNonce(prefix, index), cur[:n], chunkAD(objectKey, index, final))
		if _, err := dst.Write(out); err != nil {
			return total, err
		}
		total += int64(n)
		if final {
			return total, nil
		}
		cur, next, n = next, cur, m
	}
}

func readChunk(r io.Reader, buf []byte) (int, error) {
	n, err := io.ReadFull(r, buf)
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return n, nil
	}
	return n, err
}

func EncryptBytes(plain, fileKey []byte, objectKey string) []byte {
	var buf bytes.Buffer
	buf.Grow(int(EncryptedSize(int64(len(plain)))))
	if _, err := EncryptStream(&buf, bytes.NewReader(plain), fileKey, objectKey); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

type FetchFunc func(off, n int64) ([]byte, error)

type DecryptingReader struct {
	aead      cipher.AEAD
	prefix    []byte
	objectKey string
	plainSize int64
	fetch     FetchFunc
	pos       int64

	windowStart int64
	window      [][]byte
}

func NewDecryptingReader(plainSize int64, fileKey []byte, objectKey string, fetch FetchFunc) (*DecryptingReader, error) {
	header, err := fetch(0, chunkHeaderSize)
	if err != nil {
		return nil, err
	}
	if len(header) != chunkHeaderSize || !bytes.Equal(header[:4], chunkMagic) ||
		binary.BigEndian.Uint32(header[4:8]) != ChunkSize {
		return nil, ErrBadEncryptedFile
	}
	return &DecryptingReader{
		aead:      newAEAD(fileKey),
		prefix:    append([]byte(nil), header[8:]...),
		objectKey: objectKey,
		plainSize: plainSize,
		fetch:     fetch,
	}, nil
}

func (r *DecryptingReader) Size() int64 {
	return r.plainSize
}

func (r *DecryptingReader) plainChunkLen(index int64) int64 {
	return min(ChunkSize, r.plainSize-index*ChunkSize)
}

func (r *DecryptingReader) chunk(index int64) ([]byte, error) {
	if r.window != nil && index >= r.windowStart && index < r.windowStart+int64(len(r.window)) {
		return r.window[index-r.windowStart], nil
	}
	last := chunkCount(r.plainSize) - 1
	if index < 0 || index > last {
		return nil, io.EOF
	}
	end := min(index+readAheadChunks, last+1)
	off := chunkHeaderSize + index*(ChunkSize+chunkTag)
	var span int64
	for i := index; i < end; i++ {
		span += max(r.plainChunkLen(i), 0) + chunkTag
	}
	raw, err := r.fetch(off, span)
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) != span {
		return nil, fmt.Errorf("%w: short read", ErrBadEncryptedFile)
	}
	window := make([][]byte, 0, end-index)
	for i := index; i < end; i++ {
		n := max(r.plainChunkLen(i), 0) + chunkTag
		sealed := raw[:n]
		raw = raw[n:]
		plain, err := r.aead.Open(nil, chunkNonce(r.prefix, i), sealed, chunkAD(r.objectKey, i, i == last))
		if err != nil {
			return nil, ErrDecrypt
		}
		window = append(window, plain)
	}
	r.windowStart, r.window = index, window
	return window[0], nil
}

func (r *DecryptingReader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("negative offset")
	}
	n := 0
	for n < len(p) && off < r.plainSize {
		index := off / ChunkSize
		data, err := r.chunk(index)
		if err != nil {
			return n, err
		}
		c := copy(p[n:], data[off-index*ChunkSize:])
		n += c
		off += int64(c)
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (r *DecryptingReader) Read(p []byte) (int, error) {
	if r.pos >= r.plainSize {
		return 0, io.EOF
	}
	n, err := r.ReadAt(p, r.pos)
	r.pos += int64(n)
	if err == io.EOF && n > 0 {
		err = nil
	}
	return n, err
}

func (r *DecryptingReader) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = r.pos + offset
	case io.SeekEnd:
		abs = r.plainSize + offset
	default:
		return 0, errors.New("invalid whence")
	}
	if abs < 0 {
		return 0, errors.New("negative position")
	}
	r.pos = abs
	return abs, nil
}

func DecryptAll(plainSize int64, fileKey []byte, objectKey string, fetch FetchFunc) ([]byte, error) {
	r, err := NewDecryptingReader(plainSize, fileKey, objectKey, fetch)
	if err != nil {
		return nil, err
	}
	out := make([]byte, plainSize)
	if _, err := r.ReadAt(out, 0); err != nil && err != io.EOF {
		return nil, err
	}
	if plainSize == 0 {
		if _, err := r.chunk(0); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func BytesFetcher(data []byte) FetchFunc {
	return func(off, n int64) ([]byte, error) {
		if off < 0 || off > int64(len(data)) {
			return nil, io.ErrUnexpectedEOF
		}
		end := min(off+n, int64(len(data)))
		return data[off:end], nil
	}
}
