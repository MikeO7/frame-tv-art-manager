package samsung

import (
	"fmt"
	"strings"
	"time"
)

const (
	keyRequest          = "request"
	keyRequestID        = "request_id"
	keyGetArtModeStatus = "get_artmode_status"
	keyGetContentList   = "get_content_list"
	keyContentID        = "content_id"
	keyCategoryID       = "category_id"
	keyCategory         = "category"
	keyValue            = "value"
	requestSendImage    = "send_image"
	keyRemoteCommand    = "Cmd"
	keyRemoteData       = "DataOfCmd"
	keyRemoteOption     = "Option"
	keyRemoteType       = "TypeOfRemote"
	remotePowerKey      = "KEY_POWER"
	remoteSendKey       = "SendRemoteKey"
)

const (
	endpointArtApp        = "com.samsung.art-app"
	endpointRemoteControl = "samsung.remote.control"
)

const (
	portArtWSS         = 8002
	portRESTGate       = 8001
	wolBroadcastPort   = 9
	wolEchoPort        = 7
	maxBackoffDelay    = time.Hour
	maxDeviceInfoBytes = 1 << 20
	powerKeyHold       = 3 * time.Second
)

// checkArtError reports the TV's error envelope. Real TVs send error_code as a
// string ("-1"), some firmware sends a number, and failures arrive as
// "event":"error" with the failed request echoed in request_data.
func checkArtError(response *artResponse) error {
	code := strings.TrimSpace(string(response.ErrorCode))
	if code == "" || code == "0" {
		if response.Event == "error" {
			return fmt.Errorf("%w: error event without a code", ErrArtAPIError)
		}
		return nil
	}
	if code == "507" || code == "11001" {
		return fmt.Errorf("%w: code %s", ErrStorageFull, code)
	}
	return fmt.Errorf("%w: code %s", ErrArtAPIError, code)
}
