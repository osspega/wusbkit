package flash

import (
	"archive/zip"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/lazaroagomez/wusbkit/internal/encoding"
	"github.com/ulikunitz/xz"
)

// Source represents an image source that can be read sequentially.
// Implementations handle different formats (raw, zip) transparently.
type Source interface {
	// Size returns the total uncompressed size in bytes
	Size() int64
	// Read reads up to len(p) bytes into p
	Read(p []byte) (n int, err error)
	// Close releases any resources
	Close() error
	// Name returns the source filename for display
	Name() string
}

// OpenSource opens an image file and returns the appropriate Source implementation.
// Supports: .img, .iso, .bin, .raw (raw), .zip (streaming extraction),
// and compressed formats: .gz, .xz, .zst/.zstd (streaming decompression).
// Also supports HTTP/HTTPS URLs for remote image streaming.
func OpenSource(path string) (Source, error) {
	// Check if path is a URL and handle remote sources
	if IsURL(path) {
		return newURLSource(path)
	}

	// Handle local files based on extension
	ext := strings.ToLower(filepath.Ext(path))

	switch ext {
	case ".zip":
		return newZipSource(path)
	case ".gz", ".gzip":
		return newGzipSource(path)
	case ".xz":
		return newXzSource(path)
	case ".zst", ".zstd":
		return newZstdSource(path)
	case ".img", ".iso", ".bin", ".raw":
		return newRawSource(path)
	default:
		// Try as raw file for unknown extensions
		return newRawSource(path)
	}
}

// rawSource reads directly from an uncompressed image file.
// For ImageUSB .bin files with a 512-byte header, the header is skipped
// and the reported size reflects only the image data.
type rawSource struct {
	file *os.File
	size int64
	name string
	// ImageUSB .bin header fields (populated if header detected)
	hasBinHeader bool
	binMD5       string
	binSHA1      string
}

// imageUSBHeaderSize is the fixed size of the ImageUSB .bin file header.
const imageUSBHeaderSize = 512

func newRawSource(path string) (*rawSource, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open image: %w", err)
	}

	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("failed to stat image: %w", err)
	}

	if info.Size() == 0 {
		file.Close()
		return nil, fmt.Errorf("image file is empty")
	}

	src := &rawSource{
		file: file,
		size: info.Size(),
		name: filepath.Base(path),
	}

	// For .bin files, check for ImageUSB header
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".bin" && info.Size() > imageUSBHeaderSize {
		if err := src.detectBinHeader(); err != nil {
			file.Close()
			return nil, err
		}
	}

	return src, nil
}

// detectBinHeader checks if the file starts with an ImageUSB header.
// If found, it seeks past the header and adjusts the reported size.
func (r *rawSource) detectBinHeader() error {
	// Read first 32 bytes to check the signature
	var sigBuf [32]byte
	if _, err := io.ReadFull(r.file, sigBuf[:]); err != nil {
		// Can't read enough bytes, not a valid header — rewind and treat as raw
		r.file.Seek(0, io.SeekStart)
		return nil
	}

	if !hasImageUSBSignature(sigBuf[:]) {
		// No valid signature — rewind to start
		r.file.Seek(0, io.SeekStart)
		return nil
	}

	// Valid header detected — read the fields we need
	// Seek to offset 48 for ImageLength (uint64 LE, 8 bytes)
	if _, err := r.file.Seek(48, io.SeekStart); err != nil {
		return fmt.Errorf("failed to read bin header: %w", err)
	}

	var imageLength uint64
	if err := binary.Read(r.file, binary.LittleEndian, &imageLength); err != nil {
		return fmt.Errorf("failed to read image length from bin header: %w", err)
	}

	// Read MD5 at offset 64, 66 bytes (UTF-16LE)
	if _, err := r.file.Seek(64, io.SeekStart); err != nil {
		return fmt.Errorf("failed to read bin header: %w", err)
	}
	md5Buf := make([]byte, 66)
	if _, err := io.ReadFull(r.file, md5Buf); err != nil {
		return fmt.Errorf("failed to read MD5 from bin header: %w", err)
	}

	// Read SHA1 at offset 130, 82 bytes (UTF-16LE)
	sha1Buf := make([]byte, 82)
	if _, err := io.ReadFull(r.file, sha1Buf); err != nil {
		return fmt.Errorf("failed to read SHA1 from bin header: %w", err)
	}

	r.hasBinHeader = true
	r.size = int64(imageLength)
	r.binMD5 = encoding.DecodeUTF16LE(md5Buf)
	r.binSHA1 = encoding.DecodeUTF16LE(sha1Buf)

	// Seek to start of actual image data (past the 512-byte header)
	if _, err := r.file.Seek(imageUSBHeaderSize, io.SeekStart); err != nil {
		return fmt.Errorf("failed to seek past bin header: %w", err)
	}

	return nil
}

// hasImageUSBSignature checks if a 32-byte buffer starts with "imageUSB" when decoded as UTF-16LE.
func hasImageUSBSignature(buf []byte) bool {
	decoded := encoding.DecodeUTF16LE(buf)
	return strings.HasPrefix(decoded, "imageUSB")
}

// BinHeaderChecksums returns the stored checksums if the source is an ImageUSB .bin file.
// Returns empty strings and false if no header was detected or the source is not a raw source.
func BinHeaderChecksums(s Source) (md5, sha1 string, ok bool) {
	rs, isRaw := s.(*rawSource)
	if !isRaw || !rs.hasBinHeader {
		return "", "", false
	}
	return rs.binMD5, rs.binSHA1, true
}

func (r *rawSource) Size() int64 {
	return r.size
}

func (r *rawSource) Read(p []byte) (n int, err error) {
	return r.file.Read(p)
}

func (r *rawSource) Close() error {
	return r.file.Close()
}

func (r *rawSource) Name() string {
	return r.name
}

// zipSource extracts and streams the first image file from a zip archive
type zipSource struct {
	zipReader  *zip.ReadCloser
	fileReader io.ReadCloser
	size       int64
	name       string
}

// Supported image extensions inside zip files
var imageExtensions = map[string]bool{
	".img": true,
	".iso": true,
	".bin": true,
	".raw": true,
}

func newZipSource(path string) (*zipSource, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open zip: %w", err)
	}

	// Find the first image file in the archive
	var imageFile *zip.File
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(f.Name))
		if imageExtensions[ext] {
			imageFile = f
			break
		}
	}

	if imageFile == nil {
		zr.Close()
		return nil, fmt.Errorf("no image file found in zip (supported: .img, .iso, .bin, .raw)")
	}

	// Open the image file for streaming
	fr, err := imageFile.Open()
	if err != nil {
		zr.Close()
		return nil, fmt.Errorf("failed to open image in zip: %w", err)
	}

	return &zipSource{
		zipReader:  zr,
		fileReader: fr,
		size:       int64(imageFile.UncompressedSize64),
		name:       filepath.Base(imageFile.Name),
	}, nil
}

func (z *zipSource) Size() int64 {
	return z.size
}

func (z *zipSource) Read(p []byte) (n int, err error) {
	return z.fileReader.Read(p)
}

func (z *zipSource) Close() error {
	z.fileReader.Close()
	return z.zipReader.Close()
}

func (z *zipSource) Name() string {
	return z.name
}

// gzipSource decompresses gzip files on-the-fly
type gzipSource struct {
	file   *os.File
	reader *gzip.Reader
	size   int64
	name   string
}

func newGzipSource(path string) (*gzipSource, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open gzip file: %w", err)
	}

	gzr, err := gzip.NewReader(file)
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("failed to read gzip header: %w", err)
	}

	// Try to get uncompressed size from gzip footer (last 4 bytes = ISIZE)
	size := getGzipUncompressedSize(file)

	// Remove .gz extension for display name
	name := filepath.Base(path)
	name = strings.TrimSuffix(name, filepath.Ext(name))

	return &gzipSource{
		file:   file,
		reader: gzr,
		size:   size,
		name:   name,
	}, nil
}

// getGzipUncompressedSize reads the ISIZE field from gzip footer and validates it.
// The gzip ISIZE field is a 32-bit value that stores size modulo 2^32, so it wraps
// for files > 4GB. When this happens, we estimate based on compressed file size.
func getGzipUncompressedSize(file *os.File) int64 {
	// Get compressed file size for validation/estimation
	info, err := file.Stat()
	if err != nil {
		return 0
	}
	compressedSize := info.Size()

	// Save current position
	currentPos, err := file.Seek(0, io.SeekCurrent)
	if err != nil {
		return compressedSize * 3 // Fallback estimate
	}

	// Seek to last 4 bytes (ISIZE field)
	_, err = file.Seek(-4, io.SeekEnd)
	if err != nil {
		file.Seek(currentPos, io.SeekStart)
		return compressedSize * 3 // Fallback estimate
	}

	// Read ISIZE (little-endian uint32)
	var isize uint32
	err = binary.Read(file, binary.LittleEndian, &isize)
	if err != nil {
		file.Seek(currentPos, io.SeekStart)
		return compressedSize * 3 // Fallback estimate
	}

	// Restore position
	file.Seek(currentPos, io.SeekStart)

	uncompressedSize := int64(isize)

	// Validate ISIZE: if it's smaller than compressed size or seems wrapped (> 4GB file),
	// the ISIZE field has wrapped around. Use estimation instead.
	// A valid uncompressed size should always be >= compressed size.
	if uncompressedSize < compressedSize {
		// ISIZE wrapped around - estimate based on compressed size
		// Use a conservative estimate (3x compression ratio typical for disk images)
		return compressedSize * 3
	}

	// Additional sanity check: if compressed file is > 1GB and ISIZE < 1GB,
	// it's very likely wrapped (disk images rarely compress better than 4:1)
	const oneGB = 1 << 30
	if compressedSize > oneGB && uncompressedSize < oneGB {
		return compressedSize * 3
	}

	return uncompressedSize
}

func (g *gzipSource) Size() int64  { return g.size }
func (g *gzipSource) Name() string { return g.name }

func (g *gzipSource) Read(p []byte) (n int, err error) {
	return g.reader.Read(p)
}

func (g *gzipSource) Close() error {
	g.reader.Close()
	return g.file.Close()
}

// xzSource decompresses xz files on-the-fly
type xzSource struct {
	file   *os.File
	reader io.Reader
	size   int64
	name   string
}

func newXzSource(path string) (*xzSource, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open xz file: %w", err)
	}

	xzr, err := xz.NewReader(file)
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("failed to read xz header: %w", err)
	}

	// XZ doesn't store uncompressed size in header, estimate from file size
	info, _ := file.Stat()
	estimatedSize := info.Size() * 5 // Typical compression ratio

	// Remove .xz extension for display name
	name := filepath.Base(path)
	name = strings.TrimSuffix(name, filepath.Ext(name))

	return &xzSource{
		file:   file,
		reader: xzr,
		size:   estimatedSize,
		name:   name,
	}, nil
}

func (x *xzSource) Size() int64  { return x.size }
func (x *xzSource) Name() string { return x.name }

func (x *xzSource) Read(p []byte) (n int, err error) {
	return x.reader.Read(p)
}

func (x *xzSource) Close() error {
	return x.file.Close()
}

// zstdSource decompresses zstd files on-the-fly
type zstdSource struct {
	file   *os.File
	reader *zstd.Decoder
	size   int64
	name   string
}

func newZstdSource(path string) (*zstdSource, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open zstd file: %w", err)
	}

	zr, err := zstd.NewReader(file)
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("failed to read zstd header: %w", err)
	}

	// ZSTD doesn't always store uncompressed size, estimate from file size
	info, _ := file.Stat()
	estimatedSize := info.Size() * 4 // Typical compression ratio

	// Remove .zst/.zstd extension for display name
	name := filepath.Base(path)
	name = strings.TrimSuffix(name, filepath.Ext(name))

	return &zstdSource{
		file:   file,
		reader: zr,
		size:   estimatedSize,
		name:   name,
	}, nil
}

func (z *zstdSource) Size() int64  { return z.size }
func (z *zstdSource) Name() string { return z.name }

func (z *zstdSource) Read(p []byte) (n int, err error) {
	return z.reader.Read(p)
}

func (z *zstdSource) Close() error {
	z.reader.Close()
	return z.file.Close()
}

// httpClient is a shared HTTP client with appropriate timeouts for streaming.
// Uses longer timeouts since we're streaming large files.
var httpClient = &http.Client{
	Timeout: 0, // No overall timeout - we handle this via context
	Transport: &http.Transport{
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		DisableCompression:    true, // We want the raw bytes
	},
}

// urlSource streams image data from a remote HTTP/HTTPS URL.
// Supports both direct image files and compressed archives.
type urlSource struct {
	resp     *http.Response
	body     io.ReadCloser
	size     int64
	name     string
	isZip    bool
	zipInner io.ReadCloser // Inner reader when streaming from zip
}

// IsURL returns true if the path looks like an HTTP/HTTPS URL.
func IsURL(path string) bool {
	return strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://")
}

// newURLSource creates a new source that streams from a remote URL.
// Uses a single GET request (no HEAD) for better performance.
func newURLSource(rawURL string) (*urlSource, error) {
	// Validate URL format
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return nil, fmt.Errorf("unsupported URL scheme: %s (use http or https)", parsedURL.Scheme)
	}

	// Open GET request directly (skip HEAD for better performance)
	getResp, err := httpClient.Get(rawURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to URL: %w", err)
	}

	if getResp.StatusCode != http.StatusOK {
		getResp.Body.Close()
		return nil, fmt.Errorf("server returned error: %s", getResp.Status)
	}

	// Get content size from Content-Length header
	contentLength := getResp.ContentLength
	if contentLength <= 0 {
		getResp.Body.Close()
		return nil, fmt.Errorf("server did not provide content size (Content-Length header missing or invalid)")
	}

	// Detect filename and format from URL and headers
	filename, isZip := detectURLType(rawURL, getResp)

	// If it's a zip file, we cannot stream-extract from HTTP without downloading first
	// because zip format requires random access to read the central directory.
	if isZip {
		getResp.Body.Close()
		return nil, fmt.Errorf("zip files from URLs are not supported (zip format requires random access); download the file first or use a direct image URL")
	}

	return &urlSource{
		resp:  getResp,
		body:  getResp.Body,
		size:  contentLength,
		name:  filename,
		isZip: false,
	}, nil
}

// detectURLType determines the filename and format from a URL and HTTP response.
// It checks the URL path, Content-Disposition header, and Content-Type header.
func detectURLType(rawURL string, resp *http.Response) (filename string, isZip bool) {
	// Try Content-Disposition header first (most reliable for downloads)
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		_, params, err := mime.ParseMediaType(cd)
		if err == nil {
			if name, ok := params["filename"]; ok && name != "" {
				filename = name
				ext := strings.ToLower(filepath.Ext(filename))
				return filename, ext == ".zip"
			}
		}
	}

	// Try to extract filename from URL path
	parsedURL, err := url.Parse(rawURL)
	if err == nil {
		path := parsedURL.Path
		if path != "" && path != "/" {
			filename = filepath.Base(path)
			// Clean up URL-encoded characters
			if decoded, err := url.PathUnescape(filename); err == nil {
				filename = decoded
			}
			ext := strings.ToLower(filepath.Ext(filename))
			if ext != "" {
				return filename, ext == ".zip"
			}
		}
	}

	// Fall back to Content-Type header
	contentType := resp.Header.Get("Content-Type")
	switch {
	case strings.Contains(contentType, "application/zip"):
		return "download.zip", true
	case strings.Contains(contentType, "application/x-iso9660-image"):
		return "download.iso", false
	case strings.Contains(contentType, "application/octet-stream"):
		return "download.img", false
	default:
		// Default to .img for unknown types
		return "download.img", false
	}
}

func (u *urlSource) Size() int64 {
	return u.size
}

func (u *urlSource) Read(p []byte) (n int, err error) {
	if u.zipInner != nil {
		return u.zipInner.Read(p)
	}
	return u.body.Read(p)
}

func (u *urlSource) Close() error {
	if u.zipInner != nil {
		u.zipInner.Close()
	}
	return u.body.Close()
}

func (u *urlSource) Name() string {
	return u.name
}
