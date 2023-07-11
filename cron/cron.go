package cron

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/fsnotify/fsnotify"
	"github.com/go-co-op/gocron"
	"github.com/lulugyf/fkme/logger"
	"github.com/lulugyf/fkme/util"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

/*
{"logdir":"/tmp/logs",
 "home":"/data",
 "once_dir":"/tmp/.app/once_proc",
 "init_tgz":{"src":"/app/init.tgz", "dst": "/tmp/.app/"},
 "apps":[
  {"name":"sshserv", "cwd":"/tmp/.app/serv", "args":["./sshserv", "serve"]},
  {"name":"plservice", "cwd":"/data",
    "args":["/app/anaconda3/envs/gm/bin/python", "-c", "import sitech.aipaas.gm.runner.plservice as pl; pl.main(8181)"],
    "env": {"PYTHONPATH":"/app/_app/guimod"}},
  {"name":"nginx", "cwd":"/tmp/.app/ng", "args":["sbin/nginx", "-p", "/tmp/.app/ng", "-g", "daemon off;"]}
]}
*/

type App struct {
	Name        string            `json:"name"`
	Cwd         string            `json:"cwd"`
	Cmd         []string          `json:"args"`
	Env         map[string]string `json:"env"`
	Cron        string            `json:"crontab"`
	CheckExists string            `json:"check_exists"`
}
type InitTgz struct {
	Dst string `json:"dst"`
	Src string `json:"src"`
	App *App   `json:"app"` // 初始化命令
}
type CronConf struct {
	LogDir  string   `json:"logdir"`
	Home    string   `json:"home"`
	OnceDir string   `json:"once_dir"`
	InitTgz *InitTgz `json:"init_tgz"`
	Apps    []*App   `json:"apps"`
}

func LoadConfig(fpath string) (*CronConf, error) {
	_, err := os.Stat(fpath)
	if err != nil && os.IsNotExist(err) {
		log.Printf("Not Exist ConfigFile:%v\n", err)
		return nil, err
	}
	configJson, err := ioutil.ReadFile(fpath)
	if err != nil {
		log.Printf("ReadFile Error:%v\n", err)
		return nil, err
	}
	conf := &CronConf{}
	err = json.Unmarshal(configJson, conf)
	if err != nil {
		log.Printf("json.Unmarshal Error:%v\n", err)
		return nil, err
	}
	return conf, nil
}

func checkExeExists(exefile string) bool {
	//exefile := app.Cmd[0]
	if strings.Index(exefile, "/") < 0 {
		// without path specify, check
		_, err := exec.LookPath(exefile)
		return err == nil
	} else {
		return true
	}
}

func checkEnv(cwd string, env map[string]string) {
	fpath := fmt.Sprintf("%s/.app/env.txt")
	if _, err := os.Stat(fpath); err != nil {
		return
	}
	f, err := os.Open(fpath)
	if err != nil {
		logger.Error("open env.txt [%s] failed %v", fpath, err)
		return
	}
	defer f.Close()
	buffer := bufio.NewReader(f)
	for {
		line, _, err := buffer.ReadLine()
		nf := strings.SplitN(string(line), "=", 2)
		if len(nf) < 2 {
			continue
		}
		k := nf[0]
		v := nf[1]
		if k == "PATH" {
			v = v + ":" + env[k]
		}
		env[k] = v
		if err == io.EOF {
			break
		}
	}
}

func startApp(app *App, logdir string, home string) {
	var cmd *exec.Cmd = nil

	cmd = exec.Command(app.Cmd[0], app.Cmd[1:]...)
	logger.Info("---start cmd %s with %v ...", app.Name, app.Cmd)

	cmd.Dir = app.Cwd
	env := make(map[string]string)
	for _, e := range os.Environ() {
		i := strings.Index(e, "=")
		env[e[:i]] = e[i+1:]
	}
	if home != "" {
		env["HOME"] = home
	}
	env["TERM"] = "xterm"
	//cmd.Env = append(os.Environ(), "TERM=xterm", fmt.Sprintf("HOME=%s", a.Cwd))
	for k, v := range app.Env {
		if k == "PATH" || k == "LD_LIBRARY_PATH" {
			v = v + ":" + env[k]
			//logger.Info("Path of %s=%s", k, v)
		}
		env[k] = v
	}
	Env := []string{}
	for k, v := range env {
		Env = append(Env, fmt.Sprintf("%s=%s", k, v))
	}
	cmd.Env = Env
	stdoutWriter, err := os.OpenFile(fmt.Sprintf("%s/%s-0.log", logdir, app.Name),
		os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		logger.Error("failed open stdout file in %s, %v", logdir, err)
		return
	}
	defer stdoutWriter.Close()
	stderrWriter, err := os.OpenFile(fmt.Sprintf("%s/%s-1.log", logdir, app.Name),
		os.O_CREATE|os.O_WRONLY, 0644) //|os.O_APPEND
	if err != nil {
		logger.Error("failed open stderr file in %s, %v", logdir, err)
		return
	}
	defer stderrWriter.Close()

	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter
	err = cmd.Start()
	if err != nil {
		logger.Error("start %s failed %s", app.Name, err)
		return
	}

	p_stat, err := cmd.Process.Wait()
	logger.Warn("%s exited with code: %v err: %v", app.Name, p_stat.ExitCode(), err)
}

type FileWriteCallback func(fpath string) error

func WatchDir(basedir string, callback FileWriteCallback) {
	fw, _ := fsnotify.NewWatcher()
	filepath.Walk(basedir, func(path string, info os.FileInfo, err error) error {
		if info != nil && info.IsDir() {
			path, err := filepath.Abs(path)
			if err != nil {
				logger.Error("Walk filepath:%s err1:%v", path, err)
				return nil
			}
			err = fw.Add(path)
			if err != nil {
				logger.Error("Walk filepath:%s err2:%v", path, err)
			}
			logger.Info("Watching path: %s", path)
		}
		return nil
	})

	chgfiles := make(map[string]int)
	for {
		select {
		case event := <-fw.Events:
			{
				if (event.Op & fsnotify.Create) == fsnotify.Create {
				}

				if (event.Op & fsnotify.Write) == fsnotify.Write {
					////log.Printf("----write event (name:%s) (op:%v)\n", event.Name, event.Op)
					//f.Events <- FSEvent{Path: event.Name, Op: 2}
					chgfiles[event.Name] = 1
				}

				if (event.Op & fsnotify.Remove) == fsnotify.Remove {
				}
			}
		case err := <-fw.Errors:
			{
				logger.Error("File Watch Error: %v", err)
			}
		case <-time.After(time.Second * 1):
			{ // do callback after 1 second
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

func loadOnceFile(fpath string) (*App, error) {
	_, err := os.Stat(fpath)
	if err != nil && os.IsNotExist(err) {
		log.Printf("Not Exist ConfigFile:%v\n", err)
		return nil, err
	}
	strJson, err := ioutil.ReadFile(fpath)
	if err != nil {
		log.Printf("ReadFile Error:%v\n", err)
		return nil, err
	}
	jobj := &App{}
	err = json.Unmarshal(strJson, jobj)
	if err != nil {
		log.Printf("json.Unmarshal Error:%v\n", err)
		return nil, err
	}
	return jobj, nil
}

func init_tgz(t *InitTgz, base_dir, logdir, home string) error {
	logger.Info("un-tgz ing... %s to %s", t.Src, t.Dst)
	src := t.Src
	if src != "" {
		if strings.HasPrefix(src, "~") {
			// 替换 ~
			src = base_dir + src[1:len(src)]
		}
		if strings.HasPrefix(src, "http://") {
			logger.Info("download init_tgz src %s", src)
			err := util.DownTgz(src, t.Dst)
			if err != nil {
				logger.Error("download tgz %s failed %v", src, err)
				return err
			}
		} else {
			gzipStream, err := os.Open(src)
			if err != nil {
				logger.Error("open tgz_file %s failed %v", src, err)
				return err
			}
			if err = util.ExtractTarGz(gzipStream, t.Dst); err != nil {
				logger.Error("extractTgz %s failed %v", src, err)
				return err
			}

		}
	}

	// 如果 app 有定义, 则启动它,  需要等到它结束后再继续
	if t.App != nil {
		logger.Info("Start init cmd %s", t.App.Name)
		startApp(t.App, logdir, home)
	}
	return nil
}

// ./fkme cron -c cron/c1.json
func Run(args []string) {
	cmd := flag.NewFlagSet("cron", flag.ExitOnError)
	cfile := cmd.String("c", "cron/c1.json", "ssh private key file")
	cmd.Parse(args)

	var conf *CronConf
	var err error
	base_dir := "" // 如果 init_tgz 文件前缀是 ~, 则使用 -c 文件的前缀

	conf_file := *cfile
	if strings.HasPrefix(conf_file, "http://") {
		tmpFile, err := ioutil.TempFile(os.TempDir(), "tt1-")
		if err != nil {
			log.Fatal("Cannot create temporary file", err)
		}
		tmpFile.Close()
		tmp_file := tmpFile.Name()
		defer os.Remove(tmp_file)

		err = util.DownFile(conf_file, tmp_file)
		if err != nil {
			log.Fatalf("download file failed %v\n", err)
			return
		}
		conf, err = LoadConfig(tmp_file)
		base_dir = fmt.Sprintf("http://%s", strings.Split(conf_file, "/")[2])
	} else {
		conf, err = LoadConfig(conf_file)
		base_dir = filepath.Dir(conf_file)
	}
	if err != nil {
		log.Printf("load failed %v\n", err)
		return
	}
	if _, err := os.Stat(conf.LogDir); err != nil && os.IsNotExist(err) {
		os.MkdirAll(conf.LogDir, 0755)
	}
	logger.InitLogger(fmt.Sprintf("%s/cron.log", conf.LogDir))

	home := conf.Home
	logdir := conf.LogDir
	logger.Warn("home: %s, oncedir: %s", conf.Home, conf.OnceDir)
	//logger.Error("app size: %d", len(conf.Apps))
	//logger.Info("init_src: %s", conf.InitTgz.Src)

	// process the init_tgz
	if conf.InitTgz != nil {
		init_tgz(conf.InitTgz, base_dir, logdir, home)
	}

	has_cron := false
	cron := gocron.NewScheduler(time.UTC)
	for _, a := range conf.Apps {
		if a.Cron == "" {
			app := a
			logger.Info("---daemon: %s %v", app.Name, app.Cmd)
			if !checkExeExists(app.Cmd[0]) {
				logger.Warn("exec %s of %s not exists, Ignore it", app.Cmd[0], app.Name)
				continue
			}
			if app.CheckExists != "" {
				if !checkExeExists(app.CheckExists) {
					logger.Warn("CheckExists %s of %s failed, Ingore it", app.CheckExists, app.Name)
					continue
				}
			}
			go func() {
				//logger.Warn("in goroutine daemon!!! %s", app.Name)
				for {
					startApp(app, logdir, home)
					time.Sleep(time.Second * 5)
				}
			}()
		} else {
			app := a
			logger.Info("---cron: %s of %s", app.Name, app.Cron)
			has_cron = true
			cron.Cron(app.Cron).Do(func() {
				startApp(app, logdir, home)
			})
		}
	}
	if has_cron {
		cron.StartAsync()
	}

	// once_dir notify
	if _, err = os.Stat(conf.OnceDir); os.IsNotExist(err) {
		os.MkdirAll(conf.OnceDir, 0755)
	}

	WatchDir(conf.OnceDir, func(fpath string) error {
		logger.Info("changed file %s", fpath)
		jobj, err := loadOnceFile(fpath)
		if err != nil {
			logger.Error("load oncedir %s json failed, retry later")
		} else {
			err = os.Remove(fpath)
			if err != nil {
				logger.Error("remove file %s failed %v", fpath, err)
			}
			go startApp(jobj, logdir, home)
		}
		return nil
	})
}
