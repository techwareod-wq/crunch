package utils

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif" // register GIF decoder for image.Decode
	"image/jpeg"
	_ "image/png" // register PNG decoder for image.Decode
	"net/http"

	"golang.org/x/image/draw"
)

// visionJPEGQuality is the re-encode quality for downscaled vision inputs —
// style analysis doesn't need archival fidelity, payload size does matter.
const visionJPEGQuality = 85

// PrepareVisionImage turns raw fetched image bytes into a vision-API-ready
// payload: the content type is sniffed, decodable formats (jpeg/png/gif) are
// downscaled so the long edge is at most maxDim and re-encoded as JPEG, and
// webp (which the stdlib can't decode) passes through unchanged — the API
// accepts it and resizes server-side. Returns the encoded bytes and their
// media type, or an error for unsupported/undecodable content.
func PrepareVisionImage(data []byte, maxDim int) ([]byte, string, error) {
	if len(data) == 0 {
		return nil, "", fmt.Errorf("empty image data")
	}
	mediaType := http.DetectContentType(data)
	switch mediaType {
	case "image/jpeg", "image/png", "image/gif":
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, "", fmt.Errorf("decode %s: %w", mediaType, err)
		}
		img = downscale(img, maxDim)
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: visionJPEGQuality}); err != nil {
			return nil, "", fmt.Errorf("re-encode jpeg: %w", err)
		}
		return buf.Bytes(), "image/jpeg", nil
	case "image/webp":
		return data, mediaType, nil
	default:
		return nil, "", fmt.Errorf("unsupported image type %q", mediaType)
	}
}

// downscale returns img scaled so its long edge is at most maxDim, or img
// unchanged when it already fits (or maxDim is non-positive).
func downscale(img image.Image, maxDim int) image.Image {
	if maxDim <= 0 {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	long := w
	if h > long {
		long = h
	}
	if long <= maxDim {
		return img
	}
	scale := float64(maxDim) / float64(long)
	nw := int(float64(w) * scale)
	nh := int(float64(h) * scale)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	return dst
}
