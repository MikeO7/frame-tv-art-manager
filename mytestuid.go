package main

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
)

func main() {
	puidStr := os.Getenv("PUID")
	pgidStr := os.Getenv("PGID")

	if puidStr != "" && pgidStr != "" {
		puid, _ := strconv.Atoi(puidStr)
		pgid, _ := strconv.Atoi(pgidStr)

		fmt.Println("PUID:", puid, "PGID:", pgid)

		err := syscall.Setgid(pgid)
		fmt.Println("Setgid err:", err)

		err = syscall.Setuid(puid)
		fmt.Println("Setuid err:", err)
	}
}
