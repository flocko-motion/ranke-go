// package: client / transport
// type:    io
// job:     the two response framings a read arrives in — RFC 7464 JSON text sequences and RFC 8742
// CBOR sequences — split into the records they carry
// limits:  framing only; what a record means is the read's (-> read.go)
package client

import (
	"bufio"
	"bytes"
	"errors"
	"io"

	"github.com/fxamacker/cbor/v2"
)

// Media types the read endpoints answer with, mirroring output.encoding.
const (
	MediaJSONSeq = "application/json-seq"
	MediaCBORSeq = "application/cbor-seq"
)

// rs is RFC 7464's record separator, which opens every record in a JSON text
// sequence.
const rs = 0x1e

// ErrUnknownFraming is a response whose media type names no sequence this can split.
var ErrUnknownFraming = errors.New("ranke/client: response is neither a JSON nor a CBOR sequence")

// splitJSONSeq returns each record of an RFC 7464 stream, without its leading
// separator. A record may span lines, so the separator alone delimits.
func splitJSONSeq(r io.Reader) ([][]byte, error) {
	br := bufio.NewReader(r)
	var out [][]byte
	for {
		chunk, err := br.ReadBytes(rs)
		trimmed := bytes.TrimSpace(bytes.TrimSuffix(chunk, []byte{rs}))
		if len(trimmed) > 0 {
			out = append(out, trimmed)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return nil, err
		}
	}
}

// splitCBORSeq returns each record of an RFC 8742 stream. The items are
// self-delimiting, so the decoder's position after one is where the next begins.
func splitCBORSeq(r io.Reader) ([][]byte, error) {
	dec := cbor.NewDecoder(r)
	var out [][]byte
	for {
		var raw cbor.RawMessage
		err := dec.Decode(&raw)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, raw)
	}
}
