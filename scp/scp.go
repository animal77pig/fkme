package scp

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lulugyf/fkme/logger"
	"github.com/lulugyf/fkme/sshconfig"
	"github.com/lulugyf/fkme/util"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/net/proxy"
)

type FilePair struct {
	Remote string
	Local  string
}

type Cli struct {
	Ssh                *ssh.Client
	Sftp               *sftp.Client
	user, remote, pass string
	port               int
	socks5             string
}

func (c *Cli) connect() *Cli {
	c1 := &Cli{}
	c1.Connect(c.remote, c.port, c.user, c.pass)
	return c1
}

func (c *Cli) reconnect() {
	// 连接丢失后， 重建连接
	if c.Ssh != nil {
		c.Sftp.Close()
		c.Ssh.Close()
		c.Ssh = nil
	}
	log.Printf("reconnecting...")
	c.Connect(c.remote, c.port, c.user, c.pass)
}

// https://stackoverflow.com/questions/36102036/how-to-connect-remote-ssh-server-with-socks-proxy
func proxiedSSHClient(proxyAddress, sshServerAddress string, sshConfig *ssh.ClientConfig) (*ssh.Client, error) {
	dialer, err := proxy.SOCKS5("tcp", proxyAddress, nil, proxy.Direct)
	if err != nil {
		return nil, err
	}

	conn, err := dialer.Dial("tcp", sshServerAddress)
	if err != nil {
		return nil, err
	}

	c, chans, reqs, err := ssh.NewClientConn(conn, sshServerAddress, sshConfig)
	if err != nil {
		return nil, err
	}

	return ssh.NewClient(c, chans, reqs), nil
}

func (c *Cli) Connect(remote string, port int, user, pass string) {

	auths := []ssh.AuthMethod{ssh.Password(pass)}
	_, err := os.Stat(pass) // if os.IsNotExists(err)
	if err == nil {
		pemBytes, err := ioutil.ReadFile(pass)
		signer, err := ssh.ParsePrivateKey(pemBytes)
		if err == nil {
			auths = []ssh.AuthMethod{ssh.PublicKeys(signer)}
		}
	}
	config := &ssh.ClientConfig{
		User:            user,
		Auth:            auths,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		//HostKeyCallback: ssh.FixedHostKey(hostKey),
	}

	// connect
	var conn *ssh.Client = nil
	addr := fmt.Sprintf("%s:%d", remote, port)
	log.Printf("addr: %s\n", addr)
	if c.socks5 != "" {
		conn, err = proxiedSSHClient(c.socks5, addr, config)
	} else {
		conn, err = ssh.Dial("tcp", addr, config)
	}
	if err != nil {
		log.Fatal("connect failed: ", err)
	} else {
		log.Printf("ssh connected.")
	}

	c.Ssh = conn

	// create new SFTP client
	client, err := sftp.NewClient(conn)
	if err != nil {
		//log.Printf("sftp.NewClient failed")
		log.Fatal("sftp failed: ", err)
	}
	c.Sftp = client
	c.user = user
	c.remote = remote
	c.pass = pass
	c.port = port
}

func (c *Cli) Close() {
	//log.Printf("Closing connections...")
	//c.Sftp.Close()
	c.Ssh.Close()
	log.Printf("ssh Closed\n")
}

func (c *Cli) Upload(local_file, remote_file string) error {
	log.Printf("upload %s => %s", local_file, remote_file)
	// check if remote dir exists
	if strings.Index(remote_file, "/") >= 0 {
		pp := strings.Split(remote_file, "/")
		pdir := strings.Join(pp[:len(pp)-1], "/")
		_, err := c.Sftp.Stat(pdir)
		if err != nil {
			c.Sftp.MkdirAll(pdir)
		}
	}

	// create source file
	srcFile, err := os.Open(local_file)
	if err != nil {
		log.Printf("open local file %s failed %v\n", local_file, err)
		return err
	}
	defer srcFile.Close()

	dstFile, err := c.Sftp.Create(remote_file)
	if err != nil {
		log.Printf("sftp create file %s failed %v\n", remote_file, err)
		return err
	}
	defer dstFile.Close()

	// copy source file to destination file
	_, err = io.Copy(dstFile, srcFile)
	if err != nil {
		//log.Fatal(err)
		return err
	}
	//log.Printf("Upload file: %d bytes copied\n", bytes)
	return nil
}
func (c *Cli) Download(remote_file, local_file string) bool {
	// check if local path exists
	if strings.Index(local_file, "/") >= 0 {
		pp := strings.Split(local_file, "/")
		pdir := strings.Join(pp[:len(pp)-1], "/")
		st, err := os.Stat(pdir)
		if err != nil {
			os.MkdirAll(pdir, os.FileMode(0700))
		} else {
			if !st.IsDir() {
				logger.Error("local path is a file")
				return false
			}
		}
	}
	// create destination file
	dstFile, err := os.Create(local_file)
	if err != nil {
		log.Fatal(err)
		return false
	}
	defer dstFile.Close()

	// open source file
	srcFile, err := c.Sftp.Open(remote_file)
	if err != nil {
		log.Fatal(err)
		return false
	}

	// copy source file to destination file
	_, err = io.Copy(dstFile, srcFile)
	if err != nil {
		log.Fatal(err)
		return false
	}
	//log.Printf("Download file: %d bytes copied\n", bytes)

	// flush in-memory copy
	err = dstFile.Sync()
	if err != nil {
		log.Fatal(err)
		return false
	}
	return true
}

/**

下载整个目录,  /tmp/abc, /tmp/vvv -> /tmp/vvv
*/
func (c *Cli) DownloadDir(remote_dir, local_dir string) bool {
	st, err := c.Sftp.Stat(remote_dir)
	if err != nil {
		logger.Error("remote %s not found", remote_dir)
		return false
	}
	//pp := strings.Split(remote_dir, "/")
	if !st.IsDir() {
		//c.Download(remote_dir, local_dir+"/"+pp[len(pp)-1])
		return c.Download(remote_dir, local_dir)
	}
	//remote_plen := len(remote_dir) - len(pp[len(pp)-1]) - 1 // length of /tmp/
	remote_plen := len(remote_dir) // length of /tmp/abc
	walker := c.Sftp.Walk(remote_dir)
	for walker.Step() {
		local_file := local_dir + walker.Path()[remote_plen:]
		if walker.Stat().IsDir() {
			os.MkdirAll(local_file, os.FileMode(0755))
		} else {
			//log.Printf("D: %s->%s\n", walker.Path(), local_file)
			if !c.Download(walker.Path(), local_file) {
				return false
			}
		}
	}
	log.Printf("Done!")
	return true
}

func (c *Cli) ChanDownload(remote_dir, local_dir string, go_count int) {
	st, err := c.Sftp.Stat(remote_dir)
	if err != nil {
		log.Fatal(err)
	}
	//pp := strings.Split(remote_dir, "/")
	if !st.IsDir() {
		//c.Download(remote_dir, local_dir+"/"+pp[len(pp)-1])
		c.Download(remote_dir, local_dir)
		return
	}
	pipe := make(chan FilePair) // 向管道里送 FilePair, 然后有别的goroutine来实施上传
	notify := make(chan int)

	for i := 0; i < go_count; i++ {
		go c.chanDownloadWorker(pipe, notify)
	}
	//remote_plen := len(remote_dir) - len(pp[len(pp)-1]) - 1 // length of /tmp/
	remote_plen := len(remote_dir) // length of /tmp/abc
	walker := c.Sftp.Walk(remote_dir)
	for walker.Step() {
		local_file := local_dir + walker.Path()[remote_plen:]
		if walker.Stat().IsDir() {
			os.MkdirAll(local_file, os.FileMode(0755))
		} else {
			//log.Printf("D: %s->%s\n", walker.Path(), local_file)
			//c.Download(walker.Path(), local_file)
			pipe <- FilePair{Remote: walker.Path(), Local: local_file}
		}
	}
	for i := 0; i < go_count; i++ { // 发送结束标记
		pipe <- FilePair{Local: "", Remote: ""}
	}
	for i := 0; i < go_count; i++ { // 等待goroutine 结束
		ii := <-notify
		log.Printf("goroutine %d done!", ii)
	}
	log.Printf("Done!")
}

func (c *Cli) chanDownloadWorker(pipe chan FilePair, notify chan int) {
	c1 := c.connect()
	defer c1.Close()

	for fp := range pipe {
		if fp.Remote == "" || fp.Local == "" {
			break
		}
		//log.Printf("D: %s->%s\n", fp.Local, fp.Remote)
		c1.Download(fp.Remote, fp.Local)
	}
	notify <- 1
}

/**
上传整个目录  /tmp/abc , /mci/xx => /mci/xx/abc
改成目标路径为 /mci/xx  (以便可以重命名目录)
*/
func (c *Cli) UploadDir(local_dir, remote_dir string) bool {
	st, err := os.Stat(local_dir)
	if err != nil {
		log.Fatal(err)
		return false
	}
	//pp := strings.Split(local_dir, "/")
	if !st.IsDir() {
		//c.Upload(local_dir, remote_dir+"/"+pp[len(pp)-1])
		return c.Upload(local_dir, remote_dir) == nil
	}
	ignores := IgnorePath{}
	ignores.load_conf(local_dir)

	upload_status := true
	//local_plen := len(local_dir) - len(pp[len(pp)-1]) - 1 // length of /tmp/
	local_plen := len(local_dir) // length of /tmp/abc
	mywalkfunc := func(path string, info os.FileInfo, err error) error {
		remote_file := remote_dir + path[local_plen:] //
		//log.Printf("walk: %s -> %s  isdir: %v", path, remote_file, info.IsDir())
		if strings.Index(remote_file, "\\") > 0 {
			remote_file = strings.Replace(remote_file, "\\", "/", -1)
		}
		if info.IsDir() {
			//c.Sftp.MkdirAll(remote_file)
			// basename := filepath.Base(path)
			// if basename == ".git" || basename == "__pycache__" {
			// 	return filepath.SkipDir
			// }
			if ignores.match_dir(filepath.Base(path)) {
				return filepath.SkipDir
			}
		} else {
			if ignores.match_file(filepath.Base(path)) {
				return nil
			}
			//if strings.HasSuffix(remote_file, ".exe") {
			//	log.Printf("ignore file %s\n", path)
			//	return nil
			//}
			//log.Printf("U: %s->%s\n", path, remote_file)
			st_r, err := c.Sftp.Stat(remote_file)
			if err == nil {
				st_l, _ := os.Stat(path)
				//if st_r.ModTime().Before(st_l.ModTime()) || st_r.Size() != st_l.Size() {
				if st_r.Size() != st_l.Size() {
					//log.Printf("--need sync %s -> %s\n", path, remote_file)
				} else {
					//log.Printf("-- No need sync %s -> %s\n", path, remote_file)
					return nil
				}
				//return nil
			}

			if err := c.Upload(path, remote_file); err != nil {
				upload_status = false
				logger.Error("upload %s failed %v", path, err)
				return errors.New("upload file failed")
			}
		}
		return nil
	}
	filepath.Walk(local_dir, mywalkfunc)
	return upload_status
}

/**
列出目录, 用于并发上传
*/
func (c *Cli) ChanUpload(local_dir, remote_dir string,
	go_count int, // 有多少个 goroutine 在运行, 完了后要发送多少个空的FilePair
) {
	st, err := os.Stat(local_dir)
	if err != nil {
		log.Fatal(err)
	}
	//pp := strings.Split(local_dir, "/")
	if !st.IsDir() {
		//c.Upload(local_dir, remote_dir+"/"+pp[len(pp)-1])
		c.Upload(local_dir, remote_dir)
		return
	}
	pipe := make(chan FilePair) // 向管道里送 FilePair, 然后有别的goroutine来实施上传
	notify := make(chan int)

	for i := 0; i < go_count; i++ {
		go c.chanUploadWorker(pipe, notify)
	}

	//local_plen := len(local_dir) - len(pp[len(pp)-1]) - 1 // length of /tmp/
	local_plen := len(local_dir) // length of /tmp/abc
	log.Printf("local_plen: %d", local_plen)
	mywalkfunc := func(path string, info os.FileInfo, err error) error {
		remote_file := remote_dir + path[local_plen:] //
		//log.Printf("walk: %s -> %s  isdir: %v", path, remote_file, info.IsDir())
		if info.IsDir() {
			c.Sftp.MkdirAll(remote_file)
		} else {
			if strings.Index(remote_file, "\\") > 0 {
				remote_file = strings.Replace(remote_file, "\\", "/", -1)
			}
			//log.Printf("D: %s->%s\n", path, remote_file)
			//c.Upload(path, remote_file)
			pipe <- FilePair{Local: path, Remote: remote_file}
		}
		return nil
	}
	filepath.Walk(local_dir, mywalkfunc)
	for i := 0; i < go_count; i++ { // 发送结束标记
		pipe <- FilePair{Local: "", Remote: ""}
	}
	for i := 0; i < go_count; i++ { // 等待goroutine 结束
		ii := <-notify
		log.Printf("goroutine %d done!", ii)
	}
	log.Printf("done!")
}

func (c *Cli) chanUploadWorker(pipe chan FilePair, notify chan int) {
	c1 := c.connect()
	defer c1.Close()

	for fp := range pipe {
		if fp.Remote == "" || fp.Local == "" {
			break
		}
		log.Printf("U: %s->%s\n", fp.Local, fp.Remote)
		c1.Upload(fp.Local, fp.Remote)
	}
	notify <- 1
}

func (c *Cli) executeCmd(command string) string {
	session, _ := c.Ssh.NewSession()
	defer session.Close()

	//var stdoutBuf bytes.Buffer
	//session.Stdout = &stdoutBuf

	//_, err_ := session.StdinPipe()
	//if err_ != nil {
	//	logger.Error("open stdin failed: %v", err_)
	//	return ""
	//}
	//session.Stderr = &stdoutBuf
	// run terminal session
	//modes := ssh.TerminalModes{
	//	ssh.ECHO: 0, // supress echo
	//}
	//if err := session.RequestPty("xterm", 50, 80, modes); err != nil {
	//	log.Fatal(err)
	//}
	session.Setenv("LANG", "en_US.UTF8")
	out, err := session.CombinedOutput(command)
	//err := session.Run(command)
	if err != nil {
		logger.Error("run command failed: %v", err)
		return ""
	}
	//session.Wait()

	//return stdoutBuf.String()
	return string(out)
}

/*
上传路径中要忽略的文件名或后缀名或前缀名,  这个存放在本地目录中的 .scp_upload_ignore 文件中
配置方式:
directory/  # 目录名, 只能全匹配
*<suffix>   # 文件名, 后缀
<prefix>*   # 文件名, 前缀
*/
type IgnorePath struct {
	directories []string
	file_suffix []string
	file_prefix []string
	files       []string
}

func (self *IgnorePath) load_conf(dir string) error {
	fpath := dir + "/.scp_upload_ignore"
	_, err := os.Stat(fpath)
	if os.ErrNotExist == err {
		s := []byte(".git/\n__pycache__/\n")
		ioutil.WriteFile(fpath, s, 0644)
		//return nil
	} else if err != nil {
		return err
	}
	fp, err := os.Open(fpath)
	if err != nil {
		log.Printf("open file %s failed\n", fpath)
		return err
	}
	defer fp.Close()
	scanner := bufio.NewScanner(fp)
	scanner.Split(bufio.ScanLines)
	for scanner.Scan() {
		text := scanner.Text()
		if strings.HasPrefix(text, "#") {
			continue
		}
		log.Printf("---- ignore conf: [%s]\n", text)
		if strings.HasSuffix(text, "/") {
			self.directories = append(self.directories, text[0:len(text)-1])
		} else if strings.HasSuffix(text, "*") {
			self.file_suffix = append(self.file_suffix, text[0:len(text)-1])
		} else if strings.HasPrefix(text, "*") {
			self.file_prefix = append(self.file_prefix, text[1:])
		} else {
			self.files = append(self.files, text)
		}
		//text = append(text, scanner.Text())
	}
	return nil
}
func (self *IgnorePath) match_dir(dir string) bool {
	for _, s := range self.directories {
		if s == dir {
			log.Printf("---ignore dir: %s by %s\n", dir, s)
			return true
		}
	}
	return false
}

func (self *IgnorePath) match_file(file string) bool {
	if file == ".scp_upload_ignore" {
		return true
	}
	for _, s := range self.files {
		if s == file {
			return true
		}
	}
	for _, s := range self.file_prefix {
		if strings.HasPrefix(file, s) {
			return true
		}
	}
	for _, s := range self.file_suffix {
		if strings.HasSuffix(file, s) {
			return true
		}
	}
	return false
}

func ssh_str_parse(s string, user, pass, host, rpath *string) error {
	// {user}[/{pass}]@{host}:{remote-dir/file}
	// mci@172.18.231.76:/tmp/771
	// mci/mypass@172.18.231.76:/tmp/771
	re := regexp.MustCompile("^([^/]+)/?([^@]*)@([^:]+):(.+)")

	reout := re.FindStringSubmatch(s)
	//for groupIdx, group := range reout {
	//	fmt.Printf("%d = [%s]\n", groupIdx, group)
	//}
	if len(reout) > 0 {
		*user = reout[1]
		*pass = reout[2]
		*host = reout[3]
		*rpath = reout[4]
		return nil
	} else {
		log.Printf("--- %v, %d [%s]\n", reout, len(reout), s)
		return errors.New("Invalid format!")
	}
	//groupNames := re.SubexpNames()
	//for matchNum, match := range re.FindAllStringSubmatch("Alan Turing ", -1) {
	//	for groupIdx, group := range match {
	//		name := groupNames[groupIdx]
	//		if name == "" {
	//			name = "*"
	//		}
	//		fmt.Printf("#%d text: '%s', group: '%s'\n", matchNum, group, name)
	//	}
	//}
}

func (c *Cli) parse_ssh_config(conf_file, segname string, user, pass, host *string) error {
	if conf_file == "" {
		if os.Getenv("HOME") != "" {
			conf_file = os.ExpandEnv("${HOME}/.ssh/config")
		} else if os.Getenv("USERPROFILE") != "" {
			// windows
			conf_file = os.ExpandEnv("${USERPROFILE}\\.ssh\\config")
		}
	}
	if _, err := os.Stat(conf_file); err != nil {
		log.Printf("conf file %s not found\n", conf_file)
		return errors.New("Config file not found")
	}

	hosts, err := sshconfig.Parse(conf_file)
	if err != nil {
		return err
	}
	for _, h := range hosts {
		log.Printf(" Host: %v\n", h.Host)
	}
	//freader := bufio.NewReader(conf_file)
	//if err != nil {
	//	return errors.New()
	//}
	return nil
}

func (c *Cli) get_ssh_conf(conf_file string) {

}

type cmd_args struct {
	key_file  *string
	passcode  *string
	port      *int
	cc        *int // concurrent goroutine count, default 1
	conf_file *string
	s5        *string
	daemon    *bool
	exec      *string // only for upload single file, execute command, {} replaced with target file
}

func (a *cmd_args) connect(c *Cli, args []string) (to_remote bool, local_path, remote_path string) {
	to_remote = false
	local_path = ""
	remote_path = ""

	cmd := flag.NewFlagSet("scp", flag.ExitOnError)
	a.key_file = cmd.String("i", "", "ssh private key file")
	a.passcode = cmd.String("pw", "", "ssh password")
	a.port = cmd.Int("p", 22, "ssh port")
	a.cc = cmd.Int("c", 1, "concurrent count")
	a.conf_file = cmd.String("f", "", "Use sshconfig file, ~ is $HOME/.ssh/config")
	a.s5 = cmd.String("s5", "", "Socks5 proxy addr, x.x.x.x:nnn")
	a.daemon = cmd.Bool("daemon", false, "run daemon")
	a.exec = cmd.String("exec", "", "only for upload single file, execute command, {} replaced with target file")

	usage := func() {
		fmt.Println("fkme scp [-i=keyfile] [-p=port] <local-dir/file> <{user}[/{pass}]@{host}:{remote-dir/file}>")
		fmt.Println("fkme scp [-i=keyfile] [-p=port] <{user}[/{pass}]@{host}:{remote-dir/file}> <local-dir/file>")
	}
	cmd.Parse(args)
	c.socks5 = *a.s5

	if cmd.NArg() != 2 {
		usage()
		return
	}

	src := cmd.Arg(0)
	dst := cmd.Arg(1)

	var sshost *sshconfig.SSHHost = nil
	if *a.conf_file != "" {
		c1 := *a.conf_file
		if c1 == "~" {
			c1 = "~/.ssh/config"
		}
		p := c1
		if strings.HasPrefix(c1, "~") {
			p = sshconfig.ExpandHome(c1)
		}
		log.Printf("--- using config: [%s]\n", p)
		sc, err := sshconfig.Parse(p)
		if err != nil {
			log.Printf("config parse failed %v\n", err)
			return
		}
		ssh_host := ""
		ssh_path := ""
		if src[1] != ':' && strings.Index(src, ":") > 0 {
			s1 := strings.SplitN(src, ":", 2)
			ssh_host = s1[0]
			ssh_path = s1[1]
		} else if dst[1] != ':' && strings.Index(dst, ":") > 0 {
			s1 := strings.SplitN(dst, ":", 2)
			ssh_host = s1[0]
			ssh_path = s1[1]
			to_remote = true
		}
		log.Printf("ssh_host: %s, to_remote: %v\n",
			ssh_host, to_remote)
		for _, s := range sc {
			if s.Host[0] == ssh_host {
				sshost = s
			}
		}
		if sshost != nil {
			s := sshost
			idfile := s.IdentityFile
			log.Printf("idfile: [%s]\n", idfile)
			if idfile == "" {
				idfile = "~/.ssh/id_rsa"
			}
			if strings.HasPrefix(idfile, "~") {
				idfile = sshconfig.ExpandHome(idfile)
			}
			//log.Printf("Host: %v, %s:%d %s %s\n",
			//	s.Host, s.HostName, s.Port, s.IdentityFile, s.User)
			c.Connect(sshost.HostName, sshost.Port, sshost.User, idfile)
			if to_remote {
				local_path = src
				remote_path = ssh_path
			} else {
				local_path = dst
				remote_path = ssh_path
			}
		}
	} else {
		var (
			user  string
			pass  string
			host  string
			rpath string
		)

		if err := ssh_str_parse(src, &user, &pass, &host, &rpath); err == nil {
			// download
			log.Printf("D: %s  err: %v\n", rpath, err)
			if err == nil {
				if pass == "" {
					pass = *a.key_file
				}
				if pass == "" {
					pass = *a.passcode
				}
				//c := Cli{}
				c.Connect(host, *a.port, user, pass)
				to_remote = false
				local_path = dst
				remote_path = rpath
			}
		} else if err := ssh_str_parse(dst, &user, &pass, &host, &rpath); err == nil {
			log.Printf("U: %s  err: %v\n", rpath, err)
			if err == nil {
				if pass == "" {
					pass = *a.key_file
				}
				if pass == "" {
					pass = *a.passcode
				}
				//c := Cli{}
				c.Connect(host, *a.port, user, pass)
				to_remote = true
				local_path = src
				remote_path = rpath
			}
		}
	}
	return
}

/**

~/gosrc/sshserv$   ./sshserv serve -c `pwd`/dist -f t.json


fkme scp -i c:/users/yuanf/.ssh/id_rsa_fs -p 38022 _dd_@172.18.243.18:/data/.datasets/guanyf/txt d:/tmp/2/txt1
fkme scp -i c:/users/yuanf/.ssh/id_rsa_fs -p 38022 d:/tmp/2/txt1 _dd_@172.18.243.18:/data/.datasets/guanyf/txt2


fkme scp -i c:/users/yuanf/.ssh/id_rsa_tr -p 2022 D:\worksrc\zhr\2022\headpose _base_@localhost:/headpose
fkme scp -i c:/users/yuanf/.ssh/id_rsa_tr -p 2022 -c 4 D:\worksrc\zhr\2022\headpose _base_@localhost:/headpose

-- 通过 ~/.ssh/config 文件来查找ssh名称, 将本地目录 fkme 上传到 ud7 的 gosrc/fkme 目录, 比较文件大小不一样或者远端没有才上传, 忽略的目录文件名 在 .scp_upload_ignore 中
fkme scp -f ~ fkme ud7:gosrc/fkme
fkme scp -f ~ datacollecting od:datacollecting
fkme scp -f ~ ann-matrix od:ann-matrix

-- 功能与上面相同, 不过通过 socks5 代理 127.0.0.1:8007
fkme scp -f ~ -s5 127.0.0.1:8007 fkme ud7:gosrc/fkme

-- 守护模式
fkme scp -f ~ -daemon fkme ud7:gosrc/fkme

-- 上传文件后执行它
./fkme scp -f '~' -exec "{} mtime" fkme tt:/tmp/fkme
*/
func (c *Cli) Run(args []string) {

	c1 := &cmd_args{}
	to_remote, local_path, remote_path := c1.connect(c, args)
	logger.Info("to_remote[%v], local_path[%s], remote_path[%s]", to_remote, local_path, remote_path)
	if local_path == "" {
		logger.Error("invalid args")
		os.Exit(2)
		return
	}
	defer c.Close()
	if !to_remote {
		if *c1.cc > 1 {
			c.ChanDownload(remote_path, local_path, *c1.cc)
		} else {
			if !c.DownloadDir(remote_path, local_path) {
				logger.Error("download dir failed")
				os.Exit(3)
			}
		}
	} else {
		if *c1.cc > 1 {
			c.ChanUpload(local_path, remote_path, *c1.cc)
		} else {
			if *c1.daemon {
				// 守护进程模式, 只在这个情况下使用, 用于监控目录变更并向远端同步
				if !c.UploadDir(local_path, remote_path) { // 先整个检查上传一遍
					logger.Error("uploadDir failed")
					os.Exit(4)
				}

				// 再添加 Directory watcher
				lpath, err := filepath.Abs(local_path)
				if err != nil {
					logger.Error("can not find abs path of %s, %v", local_path, err)
					return
				}
				local_plen := len(lpath) // length of /tmp/abc
				util.WatchDir(local_path, func(fpath string) error {
					fname := filepath.Base(fpath)
					if fname == "__pycache__" || strings.HasSuffix(fpath, "~") {
						return nil
					}
					remote_file := remote_path + fpath[local_plen:]
					remote_file = strings.Replace(remote_file, "\\", "/", -1)
					if strings.Index(remote_file, "/.git/") >= 0 || strings.Index(remote_file, "/.idea/") >= 0 {
						return nil
					}
					logger.Info("file %s changed, to: %s",
						fpath, remote_file)
					err = c.Upload(fpath, remote_file)
					if err != nil {
						c.reconnect()
						err = c.Upload(fpath, remote_file) // 只重试一次
						logger.Info("reupload return %v", err)
					}
					return nil
				})
			} else {
				if !c.UploadDir(local_path, remote_path) {
					logger.Error("upload failed!")
					os.Exit(3)
					return
				}
				if st, err := os.Stat(local_path); err == nil && !st.IsDir() && *c1.exec != "" {
					// ./fkme scp -i /data/.ssh/id_rsa_tr -p 33384 -exec "{} mtime -p /data -o /tmp/nn" /data/gosrc/fkme/fkme _base_@172.18.243.18:/tmp/fkme
					// ./fkme scp -i /data/.ssh/id_rsa -p 9022 -exec "ls -l /tmp" /data/gosrc/fkme/fkme _base_@172.18.243.24:/tmp/fkme
					// ./fkme scp -i /data/.ssh/id_rsa -p 22 -exec "ls -l /tmp" /data/gosrc/fkme/fkme mci@172.18.231.76:/tmp/fkme
					// jupyter-1650868352506876710001-d9746d68c-kk82r
					c.Sftp.Chmod(remote_path, 0755)
					// upload a file and execute a command
					cmd := *c1.exec
					if strings.Index(cmd, "{}") >= 0 {
						cmd = strings.ReplaceAll(cmd, "{}", remote_path)
					}
					logger.Warn("exec [%s]", cmd)
					ret := c.executeCmd(cmd)
					fmt.Printf("[[[%s]]]\n", ret)
				}
			}
		}

	}
}

func SCP(args []string) {
	c := Cli{}
	c.Run(args)
}
