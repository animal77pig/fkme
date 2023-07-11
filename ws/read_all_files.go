package ws

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func readfile(filename string) {
	f, err := os.Open(filename)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer f.Close()
	data := make([]byte, 4096)
	zeroes := 0
	for {
		//data = data[:cap(data)]
		n, err := f.Read(data)
		if err != nil {
			if err == io.EOF {
				break
			}
			fmt.Println(err)
			return
		}
		zeroes += n
	}
}

func iter_dir(path string) {
	err := filepath.Walk(path,
		func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			//fmt.Println(path, info.Size())
			if strings.HasSuffix(path, ".log") {
				return nil // Ignore log files
			}
			readfile(path)
			return nil
		})
	if err != nil {
		log.Println(err)
	}
}

func ReadAll(path string) {
	for {
		time.Sleep(300 * time.Second)
		iter_dir(path)
	}
}
