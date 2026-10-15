Before: payloads are encrypted at full size.

```go
dc := converter.NewCodecDataConverter(converter.GetDefaultDataConverter(), encryptionCodec)
```

After: zlib runs first because codecs encode last to first.

```go
dc := converter.NewCodecDataConverter(
	converter.GetDefaultDataConverter(),
	encryptionCodec,
	converter.NewZlibCodec(converter.ZlibCodecOptions{}),
)
```
