---
id: CODE-004
title: Payload encryption without compression
why_it_costs: Storage is billed on the bytes the client sends. Encrypted bytes don't compress, so a codec chain that encrypts without compressing first stores payloads at full size.
fix: Add a compression codec that runs before encryption. In Go, NewCodecDataConverter applies codecs last to first when encoding, so list the compression codec after the encryption codec.
tradeoffs: Compression costs Worker CPU on every encode and decode. By default the zlib codec leaves a payload uncompressed when compressing wouldn't make it smaller.
docs:
  - kb: cost/best-practices-cost-optimization
snippets:
  go: snippets/CODE-004.go.md
locator:
  sdk_calls:
    go: ["converter.NewCodecDataConverter", "converter.NewZlibCodec"]
---

The data converter encrypts payloads but doesn't compress them first. Server-side compression doesn't lower the bill, because storage is billed on what the client sends, and encryption removes the patterns compression relies on.

Compress, then encrypt. The order of codecs in `NewCodecDataConverter` is easy to get backwards: when encoding, the last codec in the list runs first.
