package thumbnail

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func encode(t *testing.T, enc func(*bytes.Buffer, image.Image) error, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, enc(&buf, image.NewRGBA(image.Rect(0, 0, w, h))))
	return buf.Bytes()
}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	return encode(t, func(buf *bytes.Buffer, img image.Image) error {
		return png.Encode(buf, img)
	}, w, h)
}

func jpegBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	return encode(t, func(buf *bytes.Buffer, img image.Image) error {
		return jpeg.Encode(buf, img, nil)
	}, w, h)
}

func TestGenerate(t *testing.T) {
	tests := []struct {
		name string
		src  []byte
	}{
		{"png landscape", pngBytes(t, 640, 480)},
		{"png portrait", pngBytes(t, 480, 640)},
		{"jpeg", jpegBytes(t, 640, 480)},
		{"smaller than thumbnail", pngBytes(t, 40, 30)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := Generate(tt.src, 100)
			require.NoError(t, err)

			// The output must be a decodable 100x100 JPEG.
			img, format, err := image.Decode(bytes.NewReader(out))
			require.NoError(t, err)
			assert.Equal(t, "jpeg", format)
			assert.Equal(t, 100, img.Bounds().Dx())
			assert.Equal(t, 100, img.Bounds().Dy())
		})
	}
}

func TestGenerateNotAnImage(t *testing.T) {
	_, err := Generate([]byte("just text, no pixels"), 100)
	assert.Error(t, err)
}
