package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

func main() {
	if os.Getenv("IS_CHILD") == "1" {
		fmt.Println("Child UID:", syscall.Getuid(), "GID:", syscall.Getgid())
		return
	}

	puidStr := "1000"
	pgidStr := "1000"

	puid, _ := strconv.Atoi(puidStr)
	pgid, _ := strconv.Atoi(pgidStr)

	cmd := exec.Command(os.Args[0])
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "IS_CHILD=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{}
	cmd.SysProcAttr.Credential = &syscall.Credential{Uid: uint32(puid), Gid: uint32(pgid)}

	err := cmd.Run()
	if err != nil {
		fmt.Println("Run err:", err)
	}
}
