package service

import (
	"bufio"
	"compress/lzw"
	"compress/zlib"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image/jpeg"
	"image/png"
	"io"

	"IGoNotes/internal/model"
	"golang.org/x/image/webp"
)

func clipInvalidImage() error { return clipperManifestError(model.ClipperInvalidRequest) }
func clipImagePixels(w, h uint64) error {
	if w == 0 || h == 0 {
		return clipInvalidImage()
	}
	if w > model.ClipperMaxImagePixels || h > model.ClipperMaxImagePixels/w {
		return clipperManifestError(model.ClipperTooLarge)
	}
	return nil
}
func clipGIFBudget(frames int, pixels uint64) error {
	if frames > model.ClipperMaxGIFFrames || pixels > model.ClipperMaxGIFTotalPixels {
		return clipperManifestError(model.ClipperTooLarge)
	}
	return nil
}

// A cursor never seeks outside the actual asset, never allocates from an input
// length and checks context even when skipping blocks. Codec parsers below only
// keep fixed headers. Preflight runs on the same pinned descriptor as decoding.
type clipImageCursor struct {
	ctx       context.Context
	r         io.ReadSeeker
	buffer    *bufio.Reader
	size, pos int64
}

func (c *clipImageCursor) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	if c.pos >= c.size {
		return 0, io.EOF
	}
	p = p[:min(int64(len(p)), c.size-c.pos)]
	if c.buffer == nil {
		c.buffer = bufio.NewReader(c.r)
	}
	n, err := c.buffer.Read(p)
	c.pos += int64(n)
	return n, err
}
func (c *clipImageCursor) read(p []byte) error {
	if _, err := io.ReadFull(c, p); err != nil {
		if c.ctx.Err() != nil {
			return c.ctx.Err()
		}
		return clipInvalidImage()
	}
	return nil
}
func (c *clipImageCursor) skip(n int64) error {
	if err := c.ctx.Err(); err != nil {
		return err
	}
	if n < 0 || n > c.size-c.pos {
		return clipInvalidImage()
	}
	if c.buffer != nil && n <= int64(c.buffer.Buffered()) {
		_, err := c.buffer.Discard(int(n))
		if err == nil {
			c.pos += n
		}
		return err
	}
	return c.seek(c.pos + n)
}
func (c *clipImageCursor) seek(pos int64) error {
	_, err := c.r.Seek(pos, io.SeekStart)
	if err == nil {
		c.pos = pos
		if c.buffer != nil {
			c.buffer.Reset(c.r)
		}
	}
	return err
}
func (c *clipImageCursor) byte() (byte, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	if c.pos >= c.size {
		return 0, clipInvalidImage()
	}
	if c.buffer == nil {
		c.buffer = bufio.NewReader(c.r)
	}
	b, err := c.buffer.ReadByte()
	if err != nil {
		return 0, clipInvalidImage()
	}
	c.pos++
	return b, nil
}

func preflightClipImage(ctx context.Context, r io.ReadSeeker, size int64, media string) error {
	c := &clipImageCursor{ctx: ctx, r: r, size: size}
	var err error
	switch media {
	case model.ClipperImagePNG:
		err = preflightClipPNG(c)
	case model.ClipperImageJPEG:
		err = preflightClipJPEG(c)
	case model.ClipperImageGIF:
		err = preflightClipGIF(c)
		if err == nil {
			if err = c.seek(0); err == nil {
				err = scanClipGIF(c, true)
			}
		}
	case model.ClipperImageWebP:
		err = preflightClipWebP(c)
	default:
		return clipperManifestError(model.ClipperUnsupportedImage)
	}
	if err != nil {
		return err
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return err
	}
	// Config is not integrity validation. It additionally checks codec-specific
	// headers after the full structural scan, before any pixel allocation.
	var w, h int
	switch media {
	case model.ClipperImagePNG:
		cfg, e := png.DecodeConfig(clipContextReader{ctx, r})
		err = e
		w, h = cfg.Width, cfg.Height
	case model.ClipperImageJPEG:
		cfg, e := jpeg.DecodeConfig(clipContextReader{ctx, r})
		err = e
		w, h = cfg.Width, cfg.Height
	case model.ClipperImageWebP:
		cfg, e := webp.DecodeConfig(clipContextReader{ctx, r})
		err = e
		w, h = cfg.Width, cfg.Height
	case model.ClipperImageGIF:
		return nil // every canvas/frame already checked
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return clipInvalidImage()
	}
	return clipImagePixels(uint64(w), uint64(h))
}

func preflightClipPNG(c *clipImageCursor) error {
	var sig [8]byte
	if err := c.read(sig[:]); err != nil {
		return err
	}
	if string(sig[:]) != "\x89PNG\r\n\x1a\n" {
		return clipInvalidImage()
	}
	seenHeader, seenData, dataEnded := false, false, false
	var header [13]byte
	var firstData int64
	for {
		var b [8]byte
		if err := c.read(b[:]); err != nil {
			return err
		}
		n := int64(binary.BigEndian.Uint32(b[:4]))
		kind := string(b[4:])
		for _, letter := range b[4:] {
			if !(letter >= 'A' && letter <= 'Z') && !(letter >= 'a' && letter <= 'z') {
				return clipInvalidImage()
			}
		}
		// The third byte is reserved and must be uppercase. Unknown critical
		// chunks cannot be validated by this PNG decoder; ancillary ones can.
		if b[6]&0x20 != 0 {
			return clipInvalidImage()
		}
		if b[4]&0x20 == 0 && kind != "IHDR" && kind != "PLTE" && kind != "IDAT" && kind != "IEND" {
			return clipInvalidImage()
		}
		if n > c.size-c.pos-4 {
			return clipInvalidImage()
		}
		if kind == "acTL" || kind == "fcTL" || kind == "fdAT" {
			return clipperManifestError(model.ClipperUnsupportedImage)
		}
		if !seenHeader && kind != "IHDR" {
			return clipInvalidImage()
		}
		crc := crc32.NewIEEE()
		crc.Write(b[4:])
		switch kind {
		case "IHDR":
			if seenHeader || n != 13 {
				return clipInvalidImage()
			}
			seenHeader = true
			if err := c.read(header[:]); err != nil {
				return err
			}
			crc.Write(header[:])
			if err := clipImagePixels(uint64(binary.BigEndian.Uint32(header[:4])), uint64(binary.BigEndian.Uint32(header[4:8]))); err != nil {
				return err
			}
		case "IDAT":
			if dataEnded {
				return clipInvalidImage()
			}
			if !seenData {
				firstData = c.pos - 8
			}
			seenData = true
			if _, err := io.CopyN(crc, c, n); err != nil {
				if c.ctx.Err() != nil {
					return c.ctx.Err()
				}
				return clipInvalidImage()
			}
		case "IEND":
			if n != 0 || !seenData {
				return clipInvalidImage()
			}
		default:
			if seenData {
				dataEnded = true
			}
			if _, err := io.CopyN(crc, c, n); err != nil {
				if c.ctx.Err() != nil {
					return c.ctx.Err()
				}
				return clipInvalidImage()
			}
		}
		var checksum [4]byte
		if err := c.read(checksum[:]); err != nil {
			return err
		}
		if binary.BigEndian.Uint32(checksum[:]) != crc.Sum32() {
			return clipInvalidImage()
		}
		if kind == "IEND" {
			if c.pos != c.size {
				return clipInvalidImage()
			}
			// The standard PNG decoder tolerates garbage after the zlib stream
			// inside IDAT. Independently check one exact stream without pixels.
			return clipPNGData(c, firstData, header)
		}
	}
}

// Reads only consecutive IDAT data, stripping headers/CRCs already verified by
// preflight. ReadByte prevents zlib read-ahead from hiding trailing compressed
// bytes. Memory is constant, including for Adam7 images.
type clipPNGDataReader struct {
	c    *clipImageCursor
	left int64
	done bool
}

func (r *clipPNGDataReader) Read(p []byte) (int, error) {
	for r.left == 0 && !r.done {
		var b [8]byte
		if err := r.c.read(b[:]); err != nil {
			return 0, err
		}
		if string(b[4:]) != "IDAT" {
			r.done = true
			break
		}
		r.left = int64(binary.BigEndian.Uint32(b[:4]))
		if r.left == 0 {
			if err := r.c.skip(4); err != nil {
				return 0, err
			}
		}
	}
	if r.done {
		return 0, io.EOF
	}
	n, err := r.c.Read(p[:min(int64(len(p)), r.left)])
	r.left -= int64(n)
	if r.left == 0 && err == nil {
		err = r.c.skip(4)
	}
	return n, err
}
func (r *clipPNGDataReader) ReadByte() (byte, error) {
	var b [1]byte
	_, err := io.ReadFull(r, b[:])
	return b[0], err
}
func clipPNGData(c *clipImageCursor, offset int64, h [13]byte) error {
	w, height := uint64(binary.BigEndian.Uint32(h[:4])), uint64(binary.BigEndian.Uint32(h[4:8]))
	channels := uint64(0)
	switch h[9] {
	case 0, 3:
		channels = 1
	case 2:
		channels = 3
	case 4:
		channels = 2
	case 6:
		channels = 4
	default:
		return clipInvalidImage()
	}
	depth := uint64(h[8])
	if depth != 1 && depth != 2 && depth != 4 && depth != 8 && depth != 16 {
		return clipInvalidImage()
	}
	rowBytes := func(w, h uint64) uint64 {
		if w == 0 || h == 0 {
			return 0
		}
		return ((w*channels*depth+7)/8 + 1) * h
	}
	expected := rowBytes(w, height)
	if h[12] == 1 {
		expected = 0
		for _, p := range [][4]uint64{{0, 0, 8, 8}, {4, 0, 8, 8}, {0, 4, 4, 8}, {2, 0, 4, 4}, {0, 2, 2, 4}, {1, 0, 2, 2}, {0, 1, 1, 2}} {
			var pw, ph uint64
			if w > p[0] {
				pw = (w - p[0] + p[2] - 1) / p[2]
			}
			if height > p[1] {
				ph = (height - p[1] + p[3] - 1) / p[3]
			}
			expected += rowBytes(pw, ph)
		}
	} else if h[12] != 0 {
		return clipInvalidImage()
	}
	if err := c.seek(offset); err != nil {
		return err
	}
	idat := &clipPNGDataReader{c: c}
	zr, err := zlib.NewReader(idat)
	if err != nil {
		return clipInvalidImage()
	}
	n, err := io.Copy(io.Discard, io.LimitReader(zr, int64(expected)+1))
	closeErr := zr.Close()
	if c.ctx.Err() != nil {
		return c.ctx.Err()
	}
	if err != nil || closeErr != nil || uint64(n) != expected {
		return clipInvalidImage()
	}
	if _, err := idat.ReadByte(); err != io.EOF {
		return clipInvalidImage()
	}
	return nil
}

func preflightClipGIF(c *clipImageCursor) error {
	return scanClipGIF(c, false)
}

// Strict LZW verification is a second constant-memory scan, after all frame
// budgets are known. Stdlib DecodeAll also validates every pixel/palette, but
// deliberately tolerates a missing LZW end code and some trailing block data.
func scanClipGIF(c *clipImageCursor, verifyLZW bool) error {
	var h [13]byte
	if err := c.read(h[:]); err != nil {
		return err
	}
	if string(h[:6]) != "GIF87a" && string(h[:6]) != "GIF89a" {
		return clipInvalidImage()
	}
	w, height := uint64(binary.LittleEndian.Uint16(h[6:8])), uint64(binary.LittleEndian.Uint16(h[8:10]))
	if err := clipImagePixels(w, height); err != nil {
		return err
	}
	skipTable := func(flags byte) error {
		if flags&0x80 == 0 {
			return nil
		}
		return c.skip(int64(3 * (1 << ((flags & 7) + 1))))
	}
	if err := skipTable(h[10]); err != nil {
		return err
	}
	blocks := func() error {
		for {
			n, err := c.byte()
			if err != nil {
				return err
			}
			if n == 0 {
				return nil
			}
			if err := c.skip(int64(n)); err != nil {
				return err
			}
		}
	}
	frames := 0
	var pixels uint64
	for {
		kind, err := c.byte()
		if err != nil {
			return err
		}
		switch kind {
		case 0x3b:
			if frames == 0 || c.pos != c.size {
				return clipInvalidImage()
			}
			return nil
		case 0x21:
			if _, err := c.byte(); err != nil {
				return err
			}
			if err := blocks(); err != nil {
				return err
			}
		case 0x2c:
			var d [9]byte
			if err := c.read(d[:]); err != nil {
				return err
			}
			fw, fh := uint64(binary.LittleEndian.Uint16(d[4:6])), uint64(binary.LittleEndian.Uint16(d[6:8]))
			if err := clipImagePixels(fw, fh); err != nil {
				return err
			}
			if uint64(binary.LittleEndian.Uint16(d[:2]))+fw > w || uint64(binary.LittleEndian.Uint16(d[2:4]))+fh > height {
				return clipInvalidImage()
			}
			frames++
			pixels += fw * fh // checked per-frame bound, <=101 frames before stop
			if err := clipGIFBudget(frames, pixels); err != nil {
				return err
			}
			if err := skipTable(d[8]); err != nil {
				return err
			}
			codeSize, err := c.byte()
			if err != nil {
				return err
			}
			if codeSize < 2 || codeSize > 8 {
				return clipInvalidImage()
			}
			if verifyLZW {
				if err := verifyClipGIFLZW(c, int(codeSize), int64(fw*fh)); err != nil {
					return err
				}
			} else if err := blocks(); err != nil {
				return err
			}
		default:
			return clipInvalidImage()
		}
	}
}

type clipGIFBlocks struct {
	c    *clipImageCursor
	left int
	done bool
}

func (r *clipGIFBlocks) ReadByte() (byte, error) {
	if r.done {
		return 0, io.EOF
	}
	if r.left == 0 {
		n, err := r.c.byte()
		if err != nil {
			return 0, err
		}
		if n == 0 {
			r.done = true
			return 0, io.EOF
		}
		r.left = int(n)
	}
	b, err := r.c.byte()
	if err == nil {
		r.left--
	}
	return b, err
}
func (r *clipGIFBlocks) Read(p []byte) (int, error) {
	for i := range p {
		b, err := r.ReadByte()
		if err != nil {
			return i, err
		}
		p[i] = b
	}
	return len(p), nil
}
func verifyClipGIFLZW(c *clipImageCursor, codeSize int, pixels int64) error {
	blocks := &clipGIFBlocks{c: c}
	decoder := lzw.NewReader(blocks, lzw.LSB, codeSize)
	n, err := io.Copy(io.Discard, io.LimitReader(decoder, pixels+1))
	closeErr := decoder.Close()
	if c.ctx.Err() != nil {
		return c.ctx.Err()
	}
	if err != nil || closeErr != nil || n != pixels {
		return clipInvalidImage()
	}
	if _, err := blocks.ReadByte(); err != io.EOF {
		return clipInvalidImage()
	}
	return nil
}

func preflightClipJPEG(c *clipImageCursor) error {
	var sig [2]byte
	if err := c.read(sig[:]); err != nil {
		return err
	}
	if sig != [2]byte{0xff, 0xd8} {
		return clipInvalidImage()
	}
	inScan, seenScan, seenFrame := false, false, false
	for {
		b, err := c.byte()
		if err != nil {
			return err
		}
		if inScan && b != 0xff {
			continue
		}
		if b != 0xff {
			return clipInvalidImage()
		}
		marker, err := c.byte()
		if err != nil {
			return err
		}
		for marker == 0xff {
			marker, err = c.byte()
			if err != nil {
				return err
			}
		}
		if inScan && (marker == 0 || marker >= 0xd0 && marker <= 0xd7) {
			continue
		}
		inScan = false
		if marker == 0xd9 {
			if !seenScan || !seenFrame || c.pos != c.size {
				return clipInvalidImage()
			}
			return nil
		}
		if marker == 0 || marker == 0xd8 || marker == 1 || marker >= 0xd0 && marker <= 0xd7 {
			return clipInvalidImage()
		}
		var length [2]byte
		if err := c.read(length[:]); err != nil {
			return err
		}
		n := int64(binary.BigEndian.Uint16(length[:])) - 2
		if n < 0 {
			return clipInvalidImage()
		}
		if marker == 0xc0 || marker == 0xc1 || marker == 0xc2 {
			var frame [5]byte
			if n < 5 {
				return clipInvalidImage()
			}
			if err := c.read(frame[:]); err != nil {
				return err
			}
			n -= 5
			if err := clipImagePixels(uint64(binary.BigEndian.Uint16(frame[3:5])), uint64(binary.BigEndian.Uint16(frame[1:3]))); err != nil {
				return err
			}
			seenFrame = true
		}
		if err := c.skip(n); err != nil {
			return err
		}
		if marker == 0xda {
			inScan = true
			seenScan = true
		}
	}
}

func preflightClipWebP(c *clipImageCursor) error {
	var h [12]byte
	if err := c.read(h[:]); err != nil {
		return err
	}
	if string(h[:4]) != "RIFF" || string(h[8:]) != "WEBP" || int64(binary.LittleEndian.Uint32(h[4:8])) != c.size-8 {
		return clipInvalidImage()
	}
	seenImage, seenX, seenAlpha := false, false, false
	var canvasPixels uint64
	for c.pos < c.size {
		var b [8]byte
		if err := c.read(b[:]); err != nil {
			return err
		}
		kind := string(b[:4])
		n := int64(binary.LittleEndian.Uint32(b[4:]))
		if n > c.size-c.pos {
			return clipInvalidImage()
		}
		switch kind {
		case "ANIM", "ANMF":
			return clipperManifestError(model.ClipperUnsupportedImage)
		case "VP8X":
			if seenX || c.pos != 20 || n != 10 {
				return clipInvalidImage()
			}
			seenX = true
			var x [10]byte
			if err := c.read(x[:]); err != nil {
				return err
			}
			n -= 10
			if x[0]&2 != 0 {
				return clipperManifestError(model.ClipperUnsupportedImage)
			}
			if x[0]&0xc1 != 0 || x[1] != 0 || x[2] != 0 || x[3] != 0 {
				return clipInvalidImage()
			}
			w := uint64(x[4]) | uint64(x[5])<<8 | uint64(x[6])<<16
			hh := uint64(x[7]) | uint64(x[8])<<8 | uint64(x[9])<<16
			if err := clipImagePixels(w+1, hh+1); err != nil {
				return err
			}
			canvasPixels = (w + 1) * (hh + 1)
		case "VP8 ", "VP8L":
			if seenImage {
				return clipInvalidImage()
			}
			seenImage = true
			if kind == "VP8L" {
				var v [5]byte
				if n < 5 {
					return clipInvalidImage()
				}
				if err := c.read(v[:]); err != nil {
					return err
				}
				n -= 5
				if v[0] != 0x2f || v[4]&0xe0 != 0 {
					return clipInvalidImage()
				}
				bits := binary.LittleEndian.Uint32(v[1:])
				if err := clipImagePixels(uint64(bits&0x3fff)+1, uint64((bits>>14)&0x3fff)+1); err != nil {
					return err
				}
			} else {
				var v [10]byte
				if n < 10 {
					return clipInvalidImage()
				}
				if err := c.read(v[:]); err != nil {
					return err
				}
				n -= 10
				if v[0]&1 != 0 || string(v[3:6]) != "\x9d\x01\x2a" {
					return clipInvalidImage()
				}
				if err := clipImagePixels(uint64(binary.LittleEndian.Uint16(v[6:8])&0x3fff), uint64(binary.LittleEndian.Uint16(v[8:10])&0x3fff)); err != nil {
					return err
				}
			}
		case "ALPH":
			if !seenX || seenImage || seenAlpha {
				return clipInvalidImage()
			}
			seenAlpha = true
			if n < 1 {
				return clipInvalidImage()
			}
			flags, err := c.byte()
			if err != nil {
				return err
			}
			n--
			// Reserved bits, preprocessing and compression must be defined.
			// The decoder otherwise masks/ignores these fields and raw-alpha
			// suffix bytes. Raw alpha is exactly one byte per canvas pixel.
			if flags&0xc0 != 0 || (flags>>4)&3 > 1 || flags&3 > 1 {
				return clipInvalidImage()
			}
			if flags&3 == 0 && uint64(n) != canvasPixels {
				return clipInvalidImage()
			}
		}
		if err := c.skip(n); err != nil {
			return err
		}
		if binary.LittleEndian.Uint32(b[4:])&1 != 0 {
			pad, err := c.byte()
			if err != nil {
				return err
			}
			if pad != 0 {
				return clipInvalidImage()
			}
		}
	}
	if !seenImage {
		return clipInvalidImage()
	}
	return nil
}
