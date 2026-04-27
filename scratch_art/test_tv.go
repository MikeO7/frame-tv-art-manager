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
	token := "39903652" // Using the latest token captured from local test
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

	for {
		_, msg, err := conn.ReadMessage()
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
