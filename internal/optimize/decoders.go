package optimize

// The formats artwork is read in. image.Decode and image.DecodeConfig
// recognize only the formats registered here. Other files import these
// packages to encode, but nothing may rely on that to register a decoder.
import (
	_ "image/jpeg" // registers the JPEG decoder
	_ "image/png"  // registers the PNG decoder
)
