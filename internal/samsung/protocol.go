package samsung

import (
	"fmt"
	"time"
)

const (
	keyRequest          = "request"
	keyRequestID        = "request_id"
	keyGetArtModeStatus = "get_artmode_status"
	keyGetContentList   = "get_content_list"
	keyContentID        = "content_id"
	keyCategoryID       = "category_id"
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
	maxBackoffDelay    = time.Hour
	maxDeviceInfoBytes = 1 << 20
	powerKeyHold       = 3 * time.Second
)

func checkArtError(response *artResponse) error {
	if response.ErrorCode == 0 {
		return nil
	}
	if response.ErrorCode == 507 || response.ErrorCode == 11001 {
		return fmt.Errorf("%w: code %d", ErrStorageFull, response.ErrorCode)
	}
	return fmt.Errorf("%w: code %d", ErrArtAPIError, response.ErrorCode)
}
