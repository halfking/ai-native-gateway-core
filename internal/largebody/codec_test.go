package largebody

import (
	"bytes"
	"crypto/rand"
	"io"
	"strings"
	"testing"
)

func TestFrameWriterReader_RoundTrip(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"small", []byte("hello world")},
		{"one_frame", bytes.Repeat([]byte("a"), frameSize)},
		{"multi_frame", bytes.Repeat([]byte("b"), frameSize*3+1000)},
		{"compressible", bytes.Repeat([]byte("test pattern "), 10000)},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			
			// Write compressed
			fw, err := NewFrameWriter(&buf)
			if err != nil {
				t.Fatalf("NewFrameWriter: %v", err)
			}
			
			n, err := fw.Write(tt.data)
			if err != nil {
				t.Fatalf("Write: %v", err)
			}
			if n != len(tt.data) {
				t.Errorf("Write returned %d, want %d", n, len(tt.data))
			}
			
			if err := fw.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			
			compressed := buf.Bytes()
			t.Logf("Original: %d bytes, Compressed: %d bytes, Ratio: %.2f%%",
				len(tt.data), len(compressed),
				float64(len(compressed))*100/float64(max(len(tt.data), 1)))
			
			// Read decompressed
			fr, err := NewFrameReader(bytes.NewReader(compressed))
			if err != nil {
				t.Fatalf("NewFrameReader: %v", err)
			}
			defer fr.Close()
			
			decompressed, err := io.ReadAll(fr)
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			
			if !bytes.Equal(decompressed, tt.data) {
				t.Errorf("Decompressed data mismatch: got %d bytes, want %d bytes",
					len(decompressed), len(tt.data))
			}
		})
	}
}

func TestFrameWriter_IncrementalWrites(t *testing.T) {
	var buf bytes.Buffer
	fw, err := NewFrameWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	
	// Write in small increments crossing frame boundaries
	chunk := []byte(strings.Repeat("x", 1000))
	totalWritten := 0
	for i := 0; i < 200; i++ {
		n, err := fw.Write(chunk)
		if err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
		totalWritten += n
	}
	
	if err := fw.Close(); err != nil {
		t.Fatal(err)
	}
	
	// Verify
	fr, err := NewFrameReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer fr.Close()
	
	decompressed, err := io.ReadAll(fr)
	if err != nil {
		t.Fatal(err)
	}
	
	if len(decompressed) != totalWritten {
		t.Errorf("Got %d bytes, want %d", len(decompressed), totalWritten)
	}
}

func TestFrameReader_PartialReads(t *testing.T) {
	// Create large compressible data
	original := bytes.Repeat([]byte("partial read test "), 10000)
	
	var buf bytes.Buffer
	fw, _ := NewFrameWriter(&buf)
	fw.Write(original)
	fw.Close()
	
	// Read in small chunks
	fr, err := NewFrameReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer fr.Close()
	
	var result bytes.Buffer
	chunk := make([]byte, 1024)
	for {
		n, err := fr.Read(chunk)
		if n > 0 {
			result.Write(chunk[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
	}
	
	if !bytes.Equal(result.Bytes(), original) {
		t.Errorf("Partial reads produced different data")
	}
}

func TestIsCompressed(t *testing.T) {
	t.Run("compressed", func(t *testing.T) {
		var buf bytes.Buffer
		fw, _ := NewFrameWriter(&buf)
		fw.Write([]byte("test"))
		fw.Close()
		
		compressed, err := IsCompressed(bytes.NewReader(buf.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		if !compressed {
			t.Error("IsCompressed returned false for compressed data")
		}
	})
	
	t.Run("uncompressed", func(t *testing.T) {
		data := []byte("plain text")
		compressed, err := IsCompressed(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if compressed {
			t.Error("IsCompressed returned true for plain data")
		}
	})
	
	t.Run("too_short", func(t *testing.T) {
		compressed, err := IsCompressed(bytes.NewReader([]byte{1, 2}))
		if err != nil {
			t.Fatal(err)
		}
		if compressed {
			t.Error("IsCompressed returned true for too-short data")
		}
	})
}

func TestFrameReader_CorruptedData(t *testing.T) {
	// Create valid compressed data
	var buf bytes.Buffer
	fw, _ := NewFrameWriter(&buf)
	fw.Write(bytes.Repeat([]byte("test"), 20000))
	fw.Close()
	
	compressed := buf.Bytes()
	
	t.Run("corrupted_crc", func(t *testing.T) {
		corrupt := append([]byte(nil), compressed...)
		// Flip a bit in the middle of compressed frame data
		if len(corrupt) > headerSize+frameHeaderSize+50 {
			corrupt[headerSize+frameHeaderSize+50] ^= 0xFF
		}
		
		fr, err := NewFrameReader(bytes.NewReader(corrupt))
		if err != nil {
			// Corruption may be detected at header validation
			return
		}
		defer fr.Close()
		
		_, err = io.ReadAll(fr)
		// Either CRC check fails or decompression fails - both are acceptable
		if err == nil {
			t.Error("Expected error reading corrupted data, got nil")
		} else {
			t.Logf("Corruption detected (expected): %v", err)
		}
	})
	
	t.Run("invalid_magic", func(t *testing.T) {
		corrupt := append([]byte(nil), compressed...)
		corrupt[0] = 0xFF
		
		_, err := NewFrameReader(bytes.NewReader(corrupt))
		if err == nil {
			t.Error("Expected error for invalid magic")
		}
	})
}

func TestFrameWriter_LargeRandomData(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping large data test in short mode")
	}
	
	// 10MB of random data
	original := make([]byte, 10<<20)
	if _, err := rand.Read(original); err != nil {
		t.Fatal(err)
	}
	
	var buf bytes.Buffer
	fw, _ := NewFrameWriter(&buf)
	
	n, err := fw.Write(original)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(original) {
		t.Errorf("Wrote %d bytes, want %d", n, len(original))
	}
	
	if err := fw.Close(); err != nil {
		t.Fatal(err)
	}
	
	t.Logf("10MB random: compressed to %d bytes (%.1f%% of original)",
		buf.Len(), float64(buf.Len())*100/float64(len(original)))
	
	// Verify decompression
	fr, err := NewFrameReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer fr.Close()
	
	decompressed, err := io.ReadAll(fr)
	if err != nil {
		t.Fatal(err)
	}
	
	if !bytes.Equal(decompressed, original) {
		t.Error("Large random data mismatch after round-trip")
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
