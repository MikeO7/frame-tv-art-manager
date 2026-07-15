package optimize

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

func enforceColorProfilePolicy(ctx context.Context, file *os.File, extension, policy string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if _, err := file.Seek(0, 0); err != nil {
		return "", fmt.Errorf("seek color metadata: %w", err)
	}
	metadata, err := findColorMetadata(&contextReader{ctx: ctx, reader: file}, extension)
	if err != nil {
		return "", err
	}
	if metadata != "" && policy == profileRejectEmbedded {
		return metadata, fmt.Errorf("unsupported embedded color metadata %s", metadata)
	}
	return metadata, nil
}

func findColorMetadata(reader io.Reader, extension string) (string, error) {
	if extension == extPNG {
		return findPNGColorMetadata(reader)
	}
	return findJPEGColorMetadata(reader)
}

//nolint:gocognit,gocyclo // marker framing and short-read checks intentionally stay in one streaming parser
func findJPEGColorMetadata(reader io.Reader) (string, error) {
	var marker [2]byte
	if _, err := io.ReadFull(reader, marker[:]); err != nil {
		return "", err
	}
	if marker != [2]byte{0xff, 0xd8} {
		return "", fmt.Errorf("not a JPEG while reading color metadata")
	}
	for {
		if _, err := io.ReadFull(reader, marker[:]); err != nil {
			return "", err
		}
		if marker[0] != 0xff {
			return "", fmt.Errorf("invalid JPEG marker while reading color metadata")
		}
		if marker[1] == 0xd9 || marker[1] == 0xda {
			return "", nil
		}
		var lengthBytes [2]byte
		if _, err := io.ReadFull(reader, lengthBytes[:]); err != nil {
			return "", err
		}
		length := int64(binary.BigEndian.Uint16(lengthBytes[:])) - 2
		if length < 0 {
			return "", fmt.Errorf("invalid JPEG color metadata marker length")
		}
		if marker[1] == 0xe2 {
			prefixLength := min(length, 14)
			prefix := make([]byte, prefixLength)
			if _, err := io.ReadFull(reader, prefix); err != nil {
				return "", err
			}
			if string(prefix) == "ICC_PROFILE\x00\x01\x01" || (len(prefix) >= 12 && string(prefix[:12]) == "ICC_PROFILE\x00") {
				return "JPEG ICC", nil
			}
			length -= prefixLength
		}
		if _, err := io.CopyN(io.Discard, reader, length); err != nil {
			return "", err
		}
	}
}

func findPNGColorMetadata(reader io.Reader) (string, error) {
	var signature [8]byte
	if _, err := io.ReadFull(reader, signature[:]); err != nil {
		return "", err
	}
	if signature != pngSignature {
		return "", fmt.Errorf("not a PNG while reading color metadata")
	}
	for {
		var header [8]byte
		if _, err := io.ReadFull(reader, header[:]); err != nil {
			return "", err
		}
		length := int64(binary.BigEndian.Uint32(header[:4]))
		chunkType := string(header[4:])
		switch chunkType {
		case "iCCP", "gAMA", "cHRM":
			return "PNG " + chunkType, nil
		case "IDAT", pngChunkEnd:
			return "", nil
		}
		if _, err := io.CopyN(io.Discard, reader, length+4); err != nil {
			return "", err
		}
	}
}
