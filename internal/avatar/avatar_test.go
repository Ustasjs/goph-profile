package avatar

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestKeys(t *testing.T) {
	assert.Equal(t, "avatars/a1/original", OriginalKey("a1"))
	assert.Equal(t, "thumbnails/a1/100x100.jpg", ThumbnailKey("a1", 100))
	assert.Equal(t, "300x300", SizeName(300))
}
