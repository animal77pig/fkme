package util

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"errors"
	"flag"
	"fmt"
	"github.com/armon/go-socks5"
	"github.com/fsnotify/fsnotify"
	"github.com/lulugyf/fkme/logger"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type DiskStatus struct {
	All  uint64 `json:"all"`
	Used uint64 `json:"used"`
	Free uint64 `json:"free"`
}

// reference: https://golangdocs.com/tar-gzip-in-golang
func ExtractTarGz(gzipStream io.Reader, target string) error {
	st, err := os.Stat(target)
	if err != nil && os.IsNotExist(err) {
		err = os.MkdirAll(target, 0755)
		if err != nil {
			logger.Error("Can not create dir of %s, %v", target, err)
			return errors.New("can not create target dir")
		}
	} else if err == nil {
		if !st.IsDir() {
			logger.Error("target [%s] is not a directory", target)
			return errors.New("target dir is not a directory")
		}
	} else {
		logger.Error("Stat target %s failed %v", target, err)
		return errors.New("unknown error of stat target directory")
	}

	//gzipStream, err := os.Open(tgz_file)
	//if err != nil {
	//	logger.Error("open tgz_file %s failed %v", tgz_file, err)
	//	return err
	//}
	uncompressedStream, err := gzip.NewReader(gzipStream)
	if err != nil {
		logger.Error("ExtractTarGz: NewReader failed")
		return err
	}

	tarReader := tar.NewReader(uncompressedStream)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			logger.Error("ExtractTarGz: Next() failed: %s", err.Error())
		}

		fpath := filepath.Join(target, header.Name)
		info := header.FileInfo()
		if info.IsDir() {
			if err := os.Mkdir(fpath, 0755); err != nil {
				if !os.IsExist(err) {
					logger.Error("ExtractTarGz: Mkdir() failed: %s", err.Error())
				}
			}
			//logger.Info("---dir %s", fpath)
			continue
		}
		if header.Typeflag == tar.TypeSymlink {
			//logger.Info("--link: %s -> %s", header.Name, header.Linkname)
			if _, err := os.Stat(fpath); err == nil {
				os.Remove(fpath)
			}
			os.Symlink(header.Linkname, fpath)
			continue
		}
		//logger.Info("---file: %s typeflag: %v", fpath, header.Typeflag)
		file, err := os.OpenFile(fpath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode())
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(file, tarReader)
		if err != nil {
			return err
		}

	}
	return nil
}

func WriteFile(filename, content string) {
	log.Printf("write file to %s\n", filename)
	f, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		log.Printf("can not open file %s, %v\n", filename, err)
		return
	}
	defer f.Close()
	buffer := bufio.NewWriter(f)
	defer buffer.Flush()
	buffer.WriteString(content)
}

func Socks5(args []string) {
	cmd := flag.NewFlagSet("socks5", flag.ExitOnError)
	port := cmd.Int("p", 1080, "Port to bind")
	host := cmd.String("b", "", "Host ip to bind, default all")
	cmd.Parse(args)

	conf := &socks5.Config{}
	server, err := socks5.New(conf)
	if err != nil {
		panic(err)
	}

	addr := fmt.Sprintf("%s:%d", *host, *port)
	// Create SOCKS5 proxy on localhost port 8000
	if err := server.ListenAndServe("tcp", addr); err != nil {
		panic(err)
	}
}

type FileWriteCallback func(fpath string) error

/**
监视目录(及子目录)下的文件变更, 并调用给定的方法
- 暂时只反馈文件的写事件
- 延迟1秒处理
- callback函数中传入的文件, 总是绝对路径
*/
func WatchDir(basedir string, callback FileWriteCallback) {
	fw, _ := fsnotify.NewWatcher()
	filepath.Walk(basedir, func(path string, info os.FileInfo, err error) error {
		if info != nil && info.IsDir() {
			path, err := filepath.Abs(path)
			if err != nil {
				logger.Error("Walk filepath:%s err1:%v", path, err)
				return nil
			}
			basename := filepath.Base(path)
			if basename == ".git" || basename == "__pycache__" || basename == ".idea" {
				return filepath.SkipDir // 让 Walk 忽略此目录
			}
			err = fw.Add(path)
			if err != nil {
				logger.Error("Walk filepath:%s err2:%v", path, err)
			}
			if !strings.HasSuffix(path, "__pycache__") {
				logger.Info("Watching path: %s", path)
			}
		}
		return nil
	})

	chgfiles := make(map[string]int)
	for {
		select {
		case event := <-fw.Events:
			{
				file, err := os.Stat(event.Name)
				if err != nil && (event.Op&fsnotify.Remove) != fsnotify.Remove {
					log.Printf("----create no exist path:%s (op:%v)\n", event.Name, event.Op)
					break
				}

				if (event.Op & fsnotify.Create) == fsnotify.Create {
					if file.IsDir() { // 新创建的目录, 也加入到检测中
						fw.Add(event.Name)
					}
				}

				if (event.Op & fsnotify.Write) == fsnotify.Write {
					////log.Printf("----write event (name:%s) (op:%v)\n", event.Name, event.Op)
					//f.Events <- FSEvent{Path: event.Name, Op: 2}
					if !file.IsDir() {
						chgfiles[event.Name] = 1
					}
				}

				if (event.Op & fsnotify.Remove) == fsnotify.Remove {
				}
			}
		case err := <-fw.Errors:
			{
				logger.Error("File Watch Error: %v", err)
			}
		case <-time.After(time.Second * 2):
			{ // do callback after 2 seconds delay
				for k, _ := range chgfiles {
					err := callback(k)
					if err != nil {
						logger.Error("callback on %s failed %v", k, err)
					}
				}
				if len(chgfiles) > 0 { // clean the file list
					chgfiles = make(map[string]int)
				}

			}
		}
	}
}

func DownFile(url, target string) error {
	client := http.Client{
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			r.URL.Opaque = r.URL.Path
			return nil
		},
	}
	// Put content on file
	resp, err := client.Get(url)
	if err != nil {
		log.Printf("error open url %s, %v\n", url, err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		log.Printf("http failed %d\n", resp.StatusCode)
		return errors.New(fmt.Sprintf("http request failed %s", resp.Status))
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("can not open file %s, %v\n", target, err)
		return err
	}
	defer f.Close()

	n, err := io.Copy(f, resp.Body)
	if err != nil {
		log.Printf("download failed %v\n", err)
		return err
	} else {
		log.Printf("down successful with %d bytes\n", n)
	}
	return nil
}

func DownTgz(url, target string) error {
	client := http.Client{
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			r.URL.Opaque = r.URL.Path
			return nil
		},
	}
	// Put content on file
	resp, err := client.Get(url)
	if err != nil {
		logger.Error("download failed! %v", err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		log.Printf("http failed %d\n", resp.StatusCode)
		return errors.New(fmt.Sprintf("http request failed %s", resp.Status))
	}

	return ExtractTarGz(resp.Body, target)
}

//
func Mtime(args []string) {
	// 检查指定目录下的文件最大修改时间， 只检查2层
	cmd := flag.NewFlagSet("mtime", flag.ExitOnError)
	path := cmd.String("p", "/data", "Path to check")
	opath := cmd.String("o", "", "outfile")
	cmd.Parse(args)

	fpath := *path
	flinfo, err := ioutil.ReadDir(fpath)
	if err != nil {
		fmt.Println("-1")
		return
	}
	var max_mtime int64
	var max_file string
	max_mtime = -1
	for _, fi := range flinfo {
		if fi.IsDir() {
			fpath1 := fmt.Sprintf("%s/%s", fpath, fi.Name())
			flinfo1, err := ioutil.ReadDir(fpath1)
			if err != nil {
				continue
			}
			for _, fi1 := range flinfo1 {
				t := fi1.ModTime().Unix()
				if t > max_mtime {
					max_mtime = t
					max_file = fi.Name()
				}
			}
		} else {
			t := fi.ModTime().Unix()
			if t > max_mtime {
				max_mtime = t
				max_file = fi.Name()
			}
		}
	}
	t := time.Now().Unix() - max_mtime
	if *opath == "" {
		fmt.Printf("max_file: %s\n", max_file)
		fmt.Printf("TMDIFF_MINUTES=%d\n", t/60) // diff in minutes
	} else {
		ioutil.WriteFile(*opath, []byte(fmt.Sprintf("%d\n", t)), 0644)
	}

}
