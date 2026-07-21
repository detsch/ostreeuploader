package ostree

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// ostreeB64 is the "modified base64" ostree uses for on-disk delta directory
// names: standard base64 with '/' replaced by '_' and no '=' padding. See
// ostree_checksum_b64_inplace_from_bytes in libostree (ostree-core.c).
var ostreeB64 = base64.StdEncoding.WithPadding(base64.NoPadding)

// digestToB64 encodes a 64-hex commit checksum to ostree's modified base64
// (43 chars). It is the Go equivalent of ostree's
// ostree_checksum_inplace_to_bytes + ostree_checksum_b64_inplace_from_bytes.
func digestToB64(hexCsum string) (string, error) {
	raw, err := hex.DecodeString(hexCsum)
	if err != nil {
		return "", fmt.Errorf("bad checksum %q: %w", hexCsum, err)
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("bad checksum %q: want 32 bytes, got %d", hexCsum, len(raw))
	}
	return strings.ReplaceAll(ostreeB64.EncodeToString(raw), "/", "_"), nil
}

// staticDeltaDir returns the repo-relative directory of the from->to static
// delta, mirroring libostree's static_delta_path_base (ostree-core.c):
//
//	from set:  deltas/<B(to)[0:2]>/<B(from)[2:]>-<B(to)>
//	from empty: deltas/<B(to)[0:2]>/<B(to)[2:]>
//
// where B is digestToB64.
func staticDeltaDir(from, to string) (string, error) {
	if to == "" {
		return "", fmt.Errorf("empty 'to' commit")
	}
	bTo, err := digestToB64(to)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	sb.WriteString("deltas/")
	if from != "" {
		bFrom, err := digestToB64(from)
		if err != nil {
			return "", err
		}
		sb.WriteString(bFrom[:2])
		sb.WriteByte('/')
		sb.WriteString(bFrom[2:])
		sb.WriteByte('-')
		sb.WriteString(bTo)
	} else {
		sb.WriteString(bTo[:2])
		sb.WriteByte('/')
		sb.WriteString(bTo[2:])
	}
	return sb.String(), nil
}
