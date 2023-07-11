package w

import (
	"fmt"
	"github.com/lulugyf/fkme/cron"
	"github.com/lulugyf/fkme/logger"
	"github.com/lulugyf/fkme/util"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

func down_pkg(uu, target string) error {
	//fileURL, err := url.Parse(uu)
	//if err != nil {
	//	log.Fatal(err)
	//}

	// todo check sha1sum

	client := http.Client{
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			r.URL.Opaque = r.URL.Path
			return nil
		},
	}
	// Put content on file
	resp, err := client.Get(uu)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()

	return util.ExtractTarGz(resp.Body, target)
}
func down_file(uu, target string) error {
	client := http.Client{
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			r.URL.Opaque = r.URL.Path
			return nil
		},
	}
	// Put content on file
	resp, err := client.Get(uu)
	if err != nil {
		log.Printf("error open url %s, %v\n", uu, err)
		return err
	}
	defer resp.Body.Close()
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

func old_jupyter() {

	cmd := exec.Command("python", "/data/.app/main.py")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Start()
	if err != nil {
		logger.Error("start main.py failed %v", err)
		return
	}

	p_stat, err := cmd.Process.Wait()
	logger.Warn("%s exited with code: %v err: %v", "main.py", p_stat.ExitCode(), err)
}

/**
export _TOKEN_=123
export BASE_URI=/j
export PYPI_SERVER=http://172.18.243.19:1777

./fkme ju-init http://172.18.231.76:7777

./fkme http://172.18.231.76:7777/libs/f/tr.tgz aaaaaaaa
./fkme http://172.18.231.76:7777/libs/f/jupyter-main.tgz b31e100be83c81f7e198d75b27a56a5091229ade


iasp76:
cd gyf/proc
tar czf ~/gyf/libs/f/tr.tgz.aaaaaaaa fkme serv

*/
func StartJupyter(pkgaddr string, sha1sum string) {

	// download pkg
	url := pkgaddr + "." + sha1sum[:8]
	//logger.Info("StartJupyter ... %s", url)
	down_pkg(url, "/data/.app")

	os.Setenv("PATH", "/data/.local/bin:"+os.Getenv("PATH"))

	fsweb := strings.Join(strings.Split(pkgaddr, "/")[:3], "/")
	log.Printf("fsweb: [%s]", fsweb)

	newapp := "/data/.app/fkme"
	if _, err := os.Stat(newapp); err == nil {
		// start initial program
		_, err = exec.Command(newapp, "ju-init", fsweb).Output()
		if err != nil {
			switch e := err.(type) {
			case *exec.Error:
				fmt.Println("failed executing:", err)
			case *exec.ExitError:
				fmt.Println("command exit rc =", e.ExitCode())
			default:
				log.Printf("exec failed")
			}
		}

		// download jupyter.json
		ju_json := "/data/.app/jupyter.json"
		if _, err := os.Stat(ju_json); err != nil {
			uu := fsweb + "/libs/f/jupyter.json"
			err := down_file(uu, ju_json)
			if err != nil {
				return
			}
		}

		//todo 容器中文件查询服务 /DevContainerFiles

		/*
			实现思路: 单独启动一个http端口在 8181 上, 提供此服务
				这个服务实际上也没啥用
		*/

		cron.Run([]string{"-c", ju_json})
	} else {
		// compatible with old mode (start with main.py)
		logger.Warn("into compatible mode...")
		for {
			old_jupyter()
			time.Sleep(time.Second * 5)
		}
	}
}

func JuInit(args []string) {
	fsweb := args[0]

	// download nbx.tgz
	uu := fsweb + "/libs/f/nbx.tgz"
	down_pkg(uu, "/data")

	// generate jupyter_notebook_config.json
	os.MkdirAll("/data/.jupyter", 0755)
	json := `{"NotebookApp": {
            "token": "%s",
            "open_browser": false,
            "base_url": "%s",
            "allow_origin":"*" },
            "ServerApp":{
    "allow_origin":"*", "ip": "*" }}
`
	util.WriteFile("/data/.jupyter/jupyter_notebook_config.json",
		fmt.Sprintf(json, os.Getenv("_TOKEN_"), os.Getenv("BASE_URI")))

	// generate ~/.config/pip/pip.conf
	os.MkdirAll("/data/.config/pip", 0755)
	pypi := os.Getenv("PYPI_SERVER")
	re := regexp.MustCompile("^http://([^/]+):\\d+/?")
	pypi_host := ""
	reout := re.FindStringSubmatch(pypi)
	if len(reout) > 0 {
		pypi_host = reout[1]
	}
	log.Printf("pypi_host: [%s]\n", pypi_host)
	str := `[global]
index-url=%s
trusted-host=%s
`
	util.WriteFile("/data/.config/pip/pip.conf", fmt.Sprintf(str, pypi, pypi_host))

	//todo pip install silab

	log.Printf("done!\n")
}
