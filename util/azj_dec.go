package util

/*

go tool dist list
set GOOS=windows
set GOARCH=386
go build dec.go
*/

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

func check_file(fpath string) bool {
	f, err := os.Open(fpath)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	chunkSize := 4

	tag := []byte{'b', 0x14, '#', 'e'} // b'b\x14#e':

	buf := make([]byte, chunkSize)
	n, err := f.Read(buf)
	if err != nil || n != 4 {
		return false
	}
	return bytes.Compare(buf, tag) == 0

	// for {
	//     n, err := f.Read(buf)
	//     if err != nil && err != io.EOF {
	//         log.Fatal(err)
	//     }

	//     if err == io.EOF {
	//         break
	//     }

	//     fmt.Println(string(buf[:n]))
	// }

}

func mark_enc_files(dir string) error {
	tag := ".c_c.c"
	return filepath.Walk(dir, func(path string, info os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		basename := filepath.Base(path)
		if basename == ".git" || basename == "__pycache__" || basename == ".idea" {
			return filepath.SkipDir
		}

		// check if it is a regular file (not dir)
		if info.Mode().IsRegular() {
			//fmt.Println("file name:", info.Name())
			if strings.HasSuffix(info.Name(), tag) {
				return nil
			}
			if check_file(path) {
				fmt.Println("file path:", path)
				new_path := path + tag
				os.Rename(path, new_path)
			}
		}
		return nil
	})
}

func rename_dec_files(dpath string) error {
	tag := ".c_c.c_txt"
	return filepath.Walk(dpath, func(path string, info os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		basename := filepath.Base(path)
		if basename == ".git" || basename == "__pycache__" || basename == ".idea" {
			return filepath.SkipDir
		}

		// check if it is a regular file (not dir)
		if info.Mode().IsRegular() {
			if strings.HasSuffix(info.Name(), tag) {
				orig_path := path[0 : len(path)-len(tag)]
				os.Rename(path, orig_path)
			}

		}
		return nil
	})
}

func fexists(fpath string) bool {
	_, err := os.Stat(fpath)
	return err == nil
}

func AZJ(args []string) {
	if (len(args) > 1 && args[0] == "mark") || (len(args) > 0 && fexists(args[0])) {
		err := mark_enc_files(args[1])
		if err != nil {
			fmt.Printf("mark failed, err: %v\n", err)
		}
	} else if len(args) > 1 && args[0] == "rename" {
		err := rename_dec_files(args[1])
		if err != nil {
			fmt.Printf("rename failed, err: %v\n", err)
		}
	} else {
		fmt.Println("nothing todo")
	}
}
