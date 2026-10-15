package codec

import (
	"go.temporal.io/sdk/converter"
)

func NewEncryptionCodec(key []byte) converter.PayloadCodec { return nil }

func DataConverter(key []byte) converter.DataConverter {
	return converter.NewCodecDataConverter(converter.GetDefaultDataConverter(), NewEncryptionCodec(key)) // want CODE-004
}

func WrongOrder(key []byte) converter.DataConverter {
	codecs := []converter.PayloadCodec{
		converter.NewZlibCodec(converter.ZlibCodecOptions{AlwaysEncode: true}),
		NewEncryptionCodec(key),
	}
	return converter.NewCodecDataConverter(converter.GetDefaultDataConverter(), codecs...) // want CODE-004
}
