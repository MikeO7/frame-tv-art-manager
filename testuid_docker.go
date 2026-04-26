package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

func main() {
	go func() {
		for i := 0; i < 3; i++ {
			fmt.Println("Goroutine UID:", syscall.Getuid())
			time.Sleep(1 * time.Second)
		}
	}()
	time.Sleep(500 * time.Millisecond)

	puid := 1000
	pgid := 1000

	err := syscall.Setgid(pgid)
	if err != nil {
		fmt.Println("Setgid err:", err)
	}
	err = syscall.Setuid(puid)
	if err != nil {
		fmt.Println("Setuid err:", err)
	}

	cmd := exec.Command("id")
	out, _ := cmd.CombinedOutput()
	fmt.Printf("id after:\n%s\n", out)
	time.Sleep(2 * time.Second)
}
