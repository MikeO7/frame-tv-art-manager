package main

import (
	"fmt"
	"os/exec"
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

	// We'll run this in docker as root to test dropping to 1000
	err := syscall.Setgid(1000)
	fmt.Println("Setgid err:", err)
	err = syscall.Setuid(1000)
	fmt.Println("Setuid err:", err)

	cmd := exec.Command("id")
	out, _ := cmd.CombinedOutput()
	fmt.Printf("id after:\n%s\n", out)
	time.Sleep(2 * time.Second)
}
