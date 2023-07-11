package main

import (
	"bufio"
	"flag"
	"fmt"
	"github.com/fsnotify/fsnotify"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type FSEvent struct {
	Path string
	Op   int // 1 - create 2 - modify  3 - remove
}
type FSWatcher struct {
	handler      *fsnotify.Watcher
	basedir      string
	Events       chan FSEvent
	ignore_files []string
	ignore_dirs  []string
	doneEvent    chan FSEvent
}

func (f *FSWatcher) Init() {
	filepath.Walk(f.basedir, func(path string, info os.FileInfo, err error) error {
		if info != nil && info.IsDir() {
			path, err := filepath.Abs(path)
			if err != nil {
				log.Fatalf("Walk filepath:%s err1:%v\n", path, err)
			}
			if f.IsIgnoreDir(path) {
				// log.Printf("Ignore path: %s\n", path)
				return nil
			}
			err = f.handler.Add(path)
			if err != nil {
				log.Fatalf("Walk filepath:%s err2:%v\n", path, err)
			}
			log.Printf("Watching path: %s\n", path)
		}

		return nil
	})
}

func (f *FSWatcher) Run() {
	for {
		select {
		case event := <-f.handler.Events:
			{
				if (event.Op & fsnotify.Create) == fsnotify.Create {
					//log.Printf("----create event (name:%s) (op:%v)\n", event.Name, event.Op)
					file, err := os.Stat(event.Name)
					if err != nil {
						log.Printf("----create no exist path:%s (op:%v)\n", event.Name, event.Op)
						break
					}
					if file.IsDir() {
						f.handler.Add(event.Name)
					}
					f.Events <- FSEvent{Path: event.Name, Op: 1}
				}

				if (event.Op & fsnotify.Write) == fsnotify.Write {
					//log.Printf("----write event (name:%s) (op:%v)\n", event.Name, event.Op)
					f.Events <- FSEvent{Path: event.Name, Op: 2}
				}

				if (event.Op & fsnotify.Remove) == fsnotify.Remove {
					//log.Printf("----remove event (name:%s) (op:%v)\n", event.Name, event.Op)
					file, err := os.Stat(event.Name)
					if err == nil && file.IsDir() {
						f.handler.Remove(event.Name)
					}
					f.Events <- FSEvent{Path: event.Name, Op: 3}
				}
			}
		case err := <-f.handler.Errors:
			{
				log.Printf("File Watch Error: %v\n", err)
			}
		case <-f.doneEvent:
			{
				return
			}
		}
	}
}

func (f *FSWatcher) Done() {
	f.doneEvent <- FSEvent{Path: ""}
}

func (f *FSWatcher) IsIgnoreFile(fpath string) bool {
	dirname, filename := filepath.Split(fpath)
	for _, suffix := range f.ignore_files {
		if strings.HasSuffix(filename, suffix) {
			return true
		}
	}
	for _, prefix := range f.ignore_dirs {
		absPrefix := filepath.Join(f.basedir, prefix)
		if strings.HasPrefix(dirname, absPrefix) {
			return true
		}
	}
	return false
}

func (f *FSWatcher) IsIgnoreDir(dirname string) bool {
	for _, prefix := range f.ignore_dirs {
		absPrefix := filepath.Join(f.basedir, prefix)
		if strings.HasPrefix(dirname, absPrefix) {
			return true
		}
	}
	return false
}

func newFSWatcher(basedir string, ignore_files []string, ignore_dirs []string) *FSWatcher {
	fw, _ := fsnotify.NewWatcher()
	f := &FSWatcher{
		handler:      fw,
		basedir:      basedir,
		Events:       make(chan FSEvent),
		ignore_dirs:  ignore_dirs,
		ignore_files: ignore_files,
	}
	f.Init()
	return f
}

// sftpcli -w d:\worksrc\gosrc\fkme

/**

fkme watch -w d:\worksrc\gosrc\fkme -i c:/users/yuanf/.ssh/id_rsa_tr -p 2022 -dst _base_@localhost:/fkme

fkme watch -w d:\worksrc\gosrc\fkme -i c:/users/yuanf/.ssh/id_rsa_tr -p 2022 -dst _base_@localhost:/fkme

*/
func Watch(args []string) {

	wCmd := flag.NewFlagSet("watch", flag.ExitOnError)
	watch_path := wCmd.String("w", "", "Path to be watched")
	key_file := wCmd.String("i", "", "ssh private key file")
	passcode := wCmd.String("pw", "", "ssh password")
	port := wCmd.Int("p", 22, "ssh port")
	dst_arg := wCmd.String("dst", "", "destination ssh path: {user}[/{pass}]@{host}:{remote-dir/file}")
	wCmd.Parse(args)

	if *watch_path == "" || *dst_arg == "" {
		wCmd.Usage()
		return
	}
	path := *watch_path

	ignore_files := []string{"~"}
	ignore_dirs := []string{".idea"}

	dst := *dst_arg

	var (
		user  string
		pass  string
		host  string
		rpath string
	)

	if err := ssh_str_parse(dst, &user, &pass, &host, &rpath); err != nil {
		log.Printf("invalid dst format")
		wCmd.Usage()
		return
	}

	if _, err := os.Stat(path); err != nil {
		log.Printf("watch path %s not found!\n", path)
		return
	}

	if pass == "" {
		pass = *key_file
	}
	if pass == "" {
		pass = *passcode
	}
	c := Cli{}
	c.Connect(host, *port, user, pass)
	defer c.Close() // 关闭sftp

	log.Printf("Watching %s\n", path)

	f := newFSWatcher(path, ignore_files, ignore_dirs)
	go f.Run()
	defer f.Done() // 关闭watcher

	lpath_len := len(path)
	wait := func() {
		upfiles := make(map[string]int)
		loop := true

		for {
			if !loop {
				break
			}
			select {
			case event := <-f.Events:
				{
					p := event.Path
					if p == "" {
						loop = false
						break
					}
					if p[len(p)-1] == '~' || filepath.Base(p)[0] == '.' {
						continue
					}
					if event.Op == 1 || event.Op == 2 {
						st, _ := os.Stat(p)
						if st.IsDir() {
							continue
						} else {
							upfiles[p] = 0
						}
					}
				}
			case <-time.After(time.Second * 2):
				{ // 延迟2秒再上传
					for x, _ := range upfiles {
						//  sftp operations!  upload
						remote_fpath := strings.Replace(fmt.Sprintf("%s%s", rpath, x[lpath_len:]), "\\", "/", -1)
						//log.Printf("--UPLOAD: %s to %s\n", x, remote_fpath)
						c.Upload(x, remote_fpath)
					}
					if len(upfiles) > 0 {
						upfiles = make(map[string]int)
					}

				}
			}
		}
	}
	go wait()

	inputReader := bufio.NewReader(os.Stdin)
	for {
		input, err := inputReader.ReadString('\n')
		if err != nil {
			log.Printf("read input:%s error:%v\n", input, err)
			break
		}
		input = strings.TrimSpace(input)
		if input == "quit" {
			log.Printf("---quit!")
			break
		}
		log.Printf("---[%s]\n", input)
	}

}
