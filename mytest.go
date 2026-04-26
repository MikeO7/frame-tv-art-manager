package main

import (
	"fmt"
	"os"
	"strconv"
)

func main() {
	puidStr := os.Getenv("PUID")
	pgidStr := os.Getenv("PGID")

	if puidStr != "" && pgidStr != "" {
		puid, _ := strconv.Atoi(puidStr)
		pgid, _ := strconv.Atoi(pgidStr)

		err := os.Chown("/tmp", puid, pgid)
		fmt.Printf("Chown err: %v\n", err)
	}
}
