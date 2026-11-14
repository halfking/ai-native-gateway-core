// Package largebody codec provides frame-based compression for RAW storage.
// Each 64KiB frame is independently compressed, enabling random-access reads.
package largebody

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"

	"github.com/klauspost/compress/zstd"
)

const (
	frameSize      = 64 << 10 // 64KiB uncompressed frame
	maxFrameSize   = 128 << 10 // Compressed frame must fit this
	codecMagic     = 0x4C474243 // "LGBC" = LargeBody Codec
	codecVersion   = 1
	headerSize     = 16 // magic(4) + version(4) + flags(4) + reserved(4)
	frameHeaderSize = 12 // uncompressed_len(4) + compressed_len(4) + crc32(4)
)

var (
	ErrCorrupted = errors.New("largebody: corrupted compressed frame")
	ErrVersion   = errors.New("largebody: unsupported codec version")
)

// FrameWriter wraps an io.Writer with frame-based compression.
type FrameWriter struct {
	w          io.Writer
	enc        *zstd.Encoder
	buf        []byte        // uncompressed frame buffer
	compBuf    []byte        // compressed output buffer
	offset     int           // position in current frame
	totalBytes int64         // total uncompressed bytes written
	frameCount int
	err        error
}

// NewFrameWriter creates a compressing writer. Caller must call Close.
func NewFrameWriter(w io.Writer) (*FrameWriter, error) {
	// 2026-11-14 12h 审计轮补：zstd.NewWriter 默认 EncoderConcurrency =
	// GOMAXPROCS（本机 10+）——每个 Encoder 预分配并发 block 池（数 MB/实例），
	// LargeIngress 4 并发槽 × zstd 并发池直接顶爆 TestLargeIngressMemoryScaling
	// 的 RAW 内存门（heap_delta 182M > 门 144M，实测三连红）。本写入器是
	// 顺序文件流（frame 串行写出），并发编码零收益 ⇒ 钉死 1 并发。
	enc, err := zstd.NewWriter(nil,
		zstd.WithEncoderLevel(zstd.SpeedDefault),
		zstd.WithEncoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	
	fw := &FrameWriter{
		w:       w,
		enc:     enc,
		buf:     make([]byte, frameSize),
		compBuf: make([]byte, maxFrameSize),
	}
	
	// Write file header
	if err := fw.writeHeader(); err != nil {
		return nil, err
	}
	
	return fw, nil
}

func (fw *FrameWriter) writeHeader() error {
	header := make([]byte, headerSize)
	binary.LittleEndian.PutUint32(header[0:4], codecMagic)
	binary.LittleEndian.PutUint32(header[4:8], codecVersion)
	binary.LittleEndian.PutUint32(header[8:12], 0) // flags: reserved
	binary.LittleEndian.PutUint32(header[12:16], 0) // reserved
	_, err := fw.w.Write(header)
	return err
}

func (fw *FrameWriter) Write(p []byte) (n int, err error) {
	if fw.err != nil {
		return 0, fw.err
	}
	
	for len(p) > 0 {
		space := frameSize - fw.offset
		chunk := p
		if len(chunk) > space {
			chunk = p[:space]
		}
		
		copy(fw.buf[fw.offset:], chunk)
		fw.offset += len(chunk)
		p = p[len(chunk):]
		n += len(chunk)
		
		if fw.offset == frameSize {
			if err := fw.flushFrame(); err != nil {
				fw.err = err
				return n, err
			}
		}
	}
	
	fw.totalBytes += int64(n)
	return n, nil
}

func (fw *FrameWriter) flushFrame() error {
	if fw.offset == 0 {
		return nil
	}
	
	// Compress the frame
	uncompLen := fw.offset
	compressed := fw.enc.EncodeAll(fw.buf[:uncompLen], fw.compBuf[:0])
	compLen := len(compressed)
	
	if compLen > maxFrameSize {
		return fmt.Errorf("largebody: compressed frame %d exceeds limit %d", compLen, maxFrameSize)
	}
	
	// Calculate CRC32 of compressed data
	checksum := crc32.ChecksumIEEE(compressed)
	
	// Write frame header
	frameHdr := make([]byte, frameHeaderSize)
	binary.LittleEndian.PutUint32(frameHdr[0:4], uint32(uncompLen))
	binary.LittleEndian.PutUint32(frameHdr[4:8], uint32(compLen))
	binary.LittleEndian.PutUint32(frameHdr[8:12], checksum)
	
	if _, err := fw.w.Write(frameHdr); err != nil {
		return err
	}
	
	// Write compressed frame
	if _, err := fw.w.Write(compressed); err != nil {
		return err
	}
	
	fw.frameCount++
	fw.offset = 0
	return nil
}

func (fw *FrameWriter) Close() error {
	if fw.err != nil {
		return fw.err
	}
	
	// Flush any partial frame
	if err := fw.flushFrame(); err != nil {
		fw.err = err
		return err
	}
	
	// Sync if underlying writer supports it
	if syncer, ok := fw.w.(interface{ Sync() error }); ok {
		if err := syncer.Sync(); err != nil {
			return err
		}
	}
	
	return nil
}

// FrameReader decompresses frame-based compressed data.
type FrameReader struct {
	r          io.Reader
	dec        *zstd.Decoder
	buf        []byte // current decompressed frame
	pos        int    // position in current frame
	totalRead  int64  // total uncompressed bytes read
	eof        bool
	err        error
}

// NewFrameReader creates a decompressing reader.
func NewFrameReader(r io.Reader) (*FrameReader, error) {
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return nil, err
	}
	
	fr := &FrameReader{
		r:   r,
		dec: dec,
	}
	
	// Read and validate header
	if err := fr.readHeader(); err != nil {
		return nil, err
	}
	
	return fr, nil
}

func (fr *FrameReader) readHeader() error {
	header := make([]byte, headerSize)
	if _, err := io.ReadFull(fr.r, header); err != nil {
		return err
	}
	
	magic := binary.LittleEndian.Uint32(header[0:4])
	if magic != codecMagic {
		return fmt.Errorf("largebody: invalid magic 0x%08x, want 0x%08x", magic, codecMagic)
	}
	
	version := binary.LittleEndian.Uint32(header[4:8])
	if version != codecVersion {
		return fmt.Errorf("%w: got %d, want %d", ErrVersion, version, codecVersion)
	}
	
	return nil
}

func (fr *FrameReader) Read(p []byte) (n int, err error) {
	if fr.err != nil {
		return 0, fr.err
	}
	
	for len(p) > 0 {
		// If current frame is exhausted, load next
		if fr.pos >= len(fr.buf) {
			if fr.eof {
				return n, io.EOF
			}
			if err := fr.readNextFrame(); err != nil {
				if err == io.EOF {
					fr.eof = true
					if n > 0 {
						return n, nil
					}
				}
				fr.err = err
				return n, err
			}
		}
		
		// Copy from current frame
		chunk := len(fr.buf) - fr.pos
		if chunk > len(p) {
			chunk = len(p)
		}
		copy(p, fr.buf[fr.pos:fr.pos+chunk])
		fr.pos += chunk
		p = p[chunk:]
		n += chunk
		fr.totalRead += int64(chunk)
	}
	
	return n, nil
}

func (fr *FrameReader) readNextFrame() error {
	// Read frame header
	frameHdr := make([]byte, frameHeaderSize)
	if _, err := io.ReadFull(fr.r, frameHdr); err != nil {
		return err
	}
	
	uncompLen := binary.LittleEndian.Uint32(frameHdr[0:4])
	compLen := binary.LittleEndian.Uint32(frameHdr[4:8])
	expectedCRC := binary.LittleEndian.Uint32(frameHdr[8:12])
	
	if uncompLen > frameSize || compLen > maxFrameSize {
		return fmt.Errorf("%w: invalid frame sizes uncomp=%d comp=%d", ErrCorrupted, uncompLen, compLen)
	}
	
	// Read compressed frame
	compressed := make([]byte, compLen)
	if _, err := io.ReadFull(fr.r, compressed); err != nil {
		return err
	}
	
	// Verify CRC32
	actualCRC := crc32.ChecksumIEEE(compressed)
	if actualCRC != expectedCRC {
		return fmt.Errorf("%w: crc32 mismatch got=0x%08x want=0x%08x", ErrCorrupted, actualCRC, expectedCRC)
	}
	
	// Decompress
	decompressed, err := fr.dec.DecodeAll(compressed, nil)
	if err != nil {
		return fmt.Errorf("largebody: decompress failed: %w", err)
	}
	
	if len(decompressed) != int(uncompLen) {
		return fmt.Errorf("%w: decompressed size mismatch got=%d want=%d", ErrCorrupted, len(decompressed), uncompLen)
	}
	
	fr.buf = decompressed
	fr.pos = 0
	return nil
}

func (fr *FrameReader) Close() error {
	fr.dec.Close()
	return nil
}

// IsCompressed checks if a file starts with the codec magic number.
func IsCompressed(r io.Reader) (bool, error) {
	br := bufio.NewReader(r)
	magic := make([]byte, 4)
	if _, err := io.ReadFull(br, magic); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return false, nil // Too short to be compressed
		}
		return false, err
	}
	return binary.LittleEndian.Uint32(magic) == codecMagic, nil
}
