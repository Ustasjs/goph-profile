// Package thumbnail turns an original image into square JPEG
// thumbnails.
package thumbnail

import (
	"bytes"
	"fmt"

	"github.com/disintegration/imaging"

	// Register the WebP decoder: originals may be WebP, and
	// imaging.Decode reads through image.Decode. JPEG and PNG come
	// registered with imaging itself.
	_ "golang.org/x/image/webp"
)

// jpegQuality balances size and fidelity for small previews.
const jpegQuality = 85

// Generate decodes src, crops it to a centered px-by-px square and
// encodes the result as JPEG. Thumbnails are always JPEG regardless
// of the original format.
func Generate(src []byte, px int) ([]byte, error) {
	img, err := imaging.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}

	thumb := imaging.Fill(img, px, px, imaging.Center, imaging.Lanczos)

	var buf bytes.Buffer
	if err := imaging.Encode(&buf, thumb, imaging.JPEG, imaging.JPEGQuality(jpegQuality)); err != nil {
		return nil, fmt.Errorf("encode thumbnail: %w", err)
	}
	return buf.Bytes(), nil
}
