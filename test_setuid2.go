package main

import (
	"fmt"
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

	err := syscall.Setgid(1000)
	fmt.Println("Setgid err:", err)
	err = syscall.Setuid(1000)
	fmt.Println("Setuid err:", err)

	time.Sleep(2 * time.Second)
}
