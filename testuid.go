package main

import (
	"fmt"
	"os"
	"strconv"
)

func main() {
	puidStr := os.Getenv("PUID")
	pgidStr := os.Getenv("PGID")

	var uid, gid int
	if puidStr != "" {
		uid, _ = strconv.Atoi(puidStr)
	} else {
		uid = os.Getuid()
	}
	if pgidStr != "" {
		gid, _ = strconv.Atoi(pgidStr)
	} else {
		gid = os.Getgid()
	}

	fmt.Println("Target UID:", uid, "GID:", gid)

	dir := "/tmp/testdir3"
	err := os.MkdirAll(dir, 0755)
	if err != nil {
		fmt.Println("MkdirAll err:", err)
	}

	err = os.Chown(dir, uid, gid)
	if err != nil {
		fmt.Println("Chown err:", err)
	} else {
	    fmt.Println("Chown ok")
	}
}
