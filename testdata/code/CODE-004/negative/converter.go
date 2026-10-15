package codec

import (
	"go.temporal.io/sdk/converter"
)

func NewEncryptionCodec(key []byte) converter.PayloadCodec { return nil }

// Codecs encode last to first, so zlib listed last compresses before the
// encryption codec runs.
func DataConverter(key []byte) converter.DataConverter {
	return converter.NewCodecDataConverter(
		converter.GetDefaultDataConverter(),
		NewEncryptionCodec(key),
		converter.NewZlibCodec(converter.ZlibCodecOptions{AlwaysEncode: true}),
	)
}

// Compression alone is fine.
func Compressed() converter.DataConverter {
	return converter.NewCodecDataConverter(converter.GetDefaultDataConverter(), converter.NewZlibCodec(converter.ZlibCodecOptions{}))
}
