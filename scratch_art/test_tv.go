package main

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
)

func main() {
	host := "192.168.1.106"
	port := 8002
	name := "Nox Test Remote"
	b64Name := base64.StdEncoding.EncodeToString([]byte(name))
	token := "11898792"
	endpoint := "com.samsung.art-app"
	u := url.URL{
		Scheme: "wss",
		Host:   fmt.Sprintf("%s:%d", host, port),
		Path:   fmt.Sprintf("/api/v2/channels/%s", endpoint),
	}
	q := u.Query()
	q.Set("name", b64Name)
	q.Set("token", token)
	u.RawQuery = q.Encode()

	fmt.Printf("Connecting to %s with token...\n", u.String())

	dialer := websocket.Dialer{
		TLSClientConfig:  &tls.Config{InsecureSkipVerify: true},
		HandshakeTimeout: 10 * time.Second,
	}

	conn, _, err := dialer.Dial(u.String(), nil)
	if err != nil {
		log.Fatalf("Dial failed: %v", err)
	}
	defer conn.Close()

	fmt.Println("Connected! Waiting for first message (ms.channel.connect)...")

	// Read first message
	_, msg, err := conn.ReadMessage()
	if err != nil {
		log.Fatalf("Read message failed: %v", err)
	}

	fmt.Printf("Received: %s\n", string(msg))

	// Try to send a request for art mode status
	requestID := "test-req-123"
	payload := map[string]interface{}{
		"method": "ms.channel.emit",
		"params": map[string]interface{}{
			"event": "art_app_request",
			"to":    "host",
			"data": fmt.Sprintf(`{"request":"get_artmode_status","request_id":"%s"}`, requestID),
		},
	}

	data, _ := json.Marshal(payload)
	fmt.Printf("Sending: %s\n", string(data))

	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		log.Fatalf("Write message failed: %v", err)
	}

	// Try to get content list
	contentReqID := "test-req-content-456"
	contentPayload := map[string]interface{}{
		"method": "ms.channel.emit",
		"params": map[string]interface{}{
			"event": "art_app_request",
			"to":    "host",
			"data": fmt.Sprintf(`{"request":"get_content_list","request_id":"%s","category_id":"MY-C0002"}`, contentReqID),
		},
	}
	contentData, _ := json.Marshal(contentPayload)
	fmt.Printf("\nSending get_content_list: %s\n", string(contentData))
	if err := conn.WriteMessage(websocket.TextMessage, contentData); err != nil {
		log.Fatalf("Write message failed: %v", err)
	}

	// Read response
	fmt.Println("Waiting for content list...")
	for i := 0; i < 5; i++ {
		_, msg, err = conn.ReadMessage()
		if err != nil {
			log.Fatalf("Read response failed: %v", err)
		}
		// Truncate output for readability
		s := string(msg)
		if len(s) > 200 {
			s = s[:200] + "..."
		}
		fmt.Printf("Received: %s\n", s)
	}
}
