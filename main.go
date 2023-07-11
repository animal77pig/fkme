package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/lulugyf/fkme/cron"
	"github.com/lulugyf/fkme/logger"
	"github.com/lulugyf/fkme/scp"
	"github.com/lulugyf/fkme/util"
	"github.com/lulugyf/fkme/w"
	"github.com/lulugyf/fkme/ws"
)

type SyncConfig struct { //json.Unmarshal struct must public var
	LocalDir  string //absolute path needed
	RemoteDir string

	SshHost     string
	SshPort     int
	SshUserName string
	SshPassword string

	IgnoreFiles []string
	IgnoreDirs  []string //relative path to LocalDir
	ReplaceRule map[string]string
}

var (
	g_WaitGroup   sync.WaitGroup
	g_SyncCfg     SyncConfig
	g_FileSyncer  *FileSyncer
	g_FileWatcher *FileWatcher
	g_ConfigFile  = "config.json"
)

func loadConfig() bool {
	flag.StringVar(&g_ConfigFile, "config", "config.json", "sync config file")
	flag.Parse()
	_, err := os.Stat(g_ConfigFile)
	if err != nil {
		log.Printf("Not Exist ConfigFile:%v\n", err)
		return false
	}
	configJson, err := ioutil.ReadFile(g_ConfigFile)
	if err != nil {
		log.Printf("ReadFile Error:%v\n", err)
		return false
	}
	err = json.Unmarshal(configJson, &g_SyncCfg)
	if err != nil {
		log.Printf("json.Unmarshal Error:%v\n", err)
		return false
	}

	if !filepath.IsAbs(g_SyncCfg.LocalDir) {
		log.Print("LocalDir must be Abs Path\n")
		return false
	}
	log.Printf("---load cfg: %v----\n", g_SyncCfg)

	return true
}

func init() {
	iCpuNum := runtime.NumCPU()
	runtime.GOMAXPROCS(iCpuNum)
}

type CmdFunc func([]string)

type CmdItem struct {
	name string
	desc string
	cmd  CmdFunc
}

func main() {
	logger.InitLogger("")
	cmdlist := []*CmdItem{
		&CmdItem{name: "scp", cmd: scp.SCP, desc: "File / Directory synchronize through sftp"},
		&CmdItem{name: "watch", cmd: Watch},
		&CmdItem{name: "cron", cmd: cron.Run, desc: "A daemon process manager"},
		&CmdItem{name: "w", cmd: w.Run, desc: "a simple static file webserver"},
		&CmdItem{name: "upfile", cmd: w.Upload_client, desc: "upload a file through http"},
		&CmdItem{name: "w1", cmd: w.Run1},
		&CmdItem{name: "hx", cmd: w.Run2, desc: "run a HTTP Proxy Server"},
		&CmdItem{name: "ju-init", cmd: w.JuInit},
		&CmdItem{name: "s5", cmd: util.Socks5, desc: "start a socks5 server"},
		&CmdItem{name: "pm", cmd: util.PM, desc: "A simple port mapper"},
		&CmdItem{name: "ws", cmd: ws.Run, desc: "A socks5 server through websocket"},
		&CmdItem{name: "mtime", cmd: util.Mtime, desc: "Check file modify time"},
		&CmdItem{name: "azj", cmd: util.AZJ, desc: "Mark encrypted files(to *.c_c.c) and rename Decrypted files(from *.c_c.c_txt)"},
		&CmdItem{name: "tunnel", cmd: scp.SSHTunnel, desc: "SSH tunnel from config"},
		&CmdItem{name: "wsmid", cmd: ws.WSMidServ, desc: "Port mapper through web-socket"},
	}
	if len(os.Args) < 2 || os.Args[1] == "-h" {
		fmt.Println("Command name list:")
		for _, c := range cmdlist {
			fmt.Printf("   %s: %s\n", c.name, c.desc)
		}
		fmt.Printf("%s <cmd> -h: show command parameter help!\n", os.Args[0])
		return
	}

	// 启动 jupyter 的判断
	//if strings.HasPrefix(os.Args[1], "http://") {
	//	pkgaddr := os.Args[1]
	//	sha1sum := os.Args[2]
	//	w.StartJupyter(pkgaddr, sha1sum)
	//	return
	//}

	// 子进程退出检查并重启
	if len(os.Args) > 1 && os.Args[1] == "daemon" {
		cur_dir, err := filepath.Abs(filepath.Dir(os.Args[0]))
		args := os.Args[2:] // 子进程要脱掉 daemon 参数
		if err != nil {
			log.Fatal(err)
		}
		for {
			cmd := exec.Command(fmt.Sprintf("%s/%s", cur_dir, os.Args[0]), args...)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				logger.Error("run failed and continue: %v", err)
			}
			time.Sleep(time.Second * 3)
		}
		return
	}

	sargs := os.Args[2:]
	cmdname := os.Args[1]
	for _, c := range cmdlist {
		if c.name == cmdname {
			logger.Info("starting %s...", cmdname)
			c.cmd(sargs)
			return
		}
	}
	logger.Warn("nothing to do!")
}
