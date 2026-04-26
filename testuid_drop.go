package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

func chownR(path string, uid, gid int) error {
	return filepath.Walk(path, func(name string, info os.FileInfo, err error) error {
		if err == nil {
			err = os.Chown(name, uid, gid)
		}
		return err
	})
}

func main() {
    fmt.Println("test")
}
