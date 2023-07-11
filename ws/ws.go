package ws

import (
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"github.com/elazarl/goproxy"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/armon/go-socks5"
	"github.com/gorilla/websocket"
	"github.com/lulugyf/fkme/logger"
	"go.uber.org/ratelimit"
)

/*
一个基于websocket的socks5 proxy server and client

参考:  https://blog.logrocket.com/using-websockets-go/
*/

var upgrader = websocket.Upgrader{}
var _counter int32 = 0
var counter = &_counter

func _add(c int) int {
	cc := atomic.AddInt32(counter, int32(c))
	return int(cc)
}

/*
封装 websocket.Conn 类型以满足 net.Conn or io.Reader or io.Writer
*/
type WS struct {
	c      *websocket.Conn
	remain []byte
}

func (ws *WS) Close() error {
	return ws.c.Close()
}

func (ws *WS) LocalAddr() net.Addr {
	return ws.c.LocalAddr()
}

func (ws *WS) RemoteAddr() net.Addr {
	return ws.c.RemoteAddr()
}

func (ws *WS) SetDeadline(t time.Time) error {
	return ws.c.SetReadDeadline(t)
}

func (ws *WS) SetReadDeadline(t time.Time) error {
	return ws.c.SetReadDeadline(t)
}

func (ws *WS) SetWriteDeadline(t time.Time) error {
	return ws.c.SetWriteDeadline(t)
}

func (ws *WS) Read(p []byte) (int, error) {
	var message []byte
	var err error

	if ws.remain != nil {
		message = ws.remain
	} else {
		_, message, err = ws.c.ReadMessage()
		if err != nil {
			return -1, err
		}
	}

	if len(message) <= len(p) {
		ws.remain = nil
		return copy(p, message), nil
	} else {
		ws.remain = message[len(p):]
		return copy(p, message), nil
	}
}
func (ws *WS) Write(p []byte) (int, error) {
	err := ws.c.WriteMessage(websocket.BinaryMessage, p)
	if err != nil {
		return -1, err
	}
	return len(p), nil
}

/*
*
一次 request-response 请求
*/
func (ws *WS) Req(msg string) (string, error) {
	err := ws.c.WriteMessage(websocket.TextMessage, []byte(msg))
	if err != nil {
		return "", err
	}
	_, rdata, err := ws.c.ReadMessage()
	if err != nil {
		return "", err
	}
	return string(rdata), nil
}

func connCopy(conn net.Conn, w *websocket.Conn) {
	raddr := conn.RemoteAddr()
	once := sync.Once{}
	close := func() {
		conn.Close()
		w.Close()
		a := _add(-1)
		logger.Info("connection for %s closed, conn size: %d", raddr, a)
	}
	ws := &WS{c: w}
	go func() {
		io.Copy(conn, ws)
		once.Do(close)
	}()

	go func() {
		io.Copy(ws, conn)
		once.Do(close)
	}()
}

/*
运行在 nginx 后端的 websocket server程序， 将连接转发到其后的端口或者自己启动的 socks5 服务
*/
func serveServer(port int, dst_addr string, uri_prefix string) {
	logger.Info("run ws server on %d  to %s", port, dst_addr)
	var s5 *socks5.Server = nil
	var err error
	if dst_addr == "" || dst_addr == "s5" { // 如果目标地址为空或者 "s5", 则将连接直接转接到socks5服务, 这样这个客户端连接就直接是一个socks5代理连接
		conf := &socks5.Config{}
		s5, err = socks5.New(conf)
		if err != nil {
			panic(err)
		}
	}
	rl := ratelimit.New(20) // per second
	http.HandleFunc(fmt.Sprintf("%s/ping", uri_prefix), func(w http.ResponseWriter, r *http.Request) {
		h := []string{}
		for k, v := range r.Header {
			h = append(h, fmt.Sprintf("%s: %s", k, v))
		}
		fmt.Fprintf(w, "hello:\n%s\n", strings.Join(h, "\n"))
	})
	static_dir := "static"
	uri := fmt.Sprintf("%s/%s/", uri_prefix, static_dir)
	mydir := fmt.Sprintf("%s/", static_dir)
	http.Handle(uri, http.StripPrefix(uri, http.FileServer(http.Dir(mydir))))
	http.HandleFunc(fmt.Sprintf("%s/ws", uri_prefix), func(w http.ResponseWriter, r *http.Request) {
		rl.Take()
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			logger.Error("upgrade failed: %v", err)
			return
		}
		logger.Info("new income ws connection, %v", conn.RemoteAddr())
		if s5 != nil {
			s5.ServeConn(&WS{c: conn})
		} else {
			cr, err := net.Dial("tcp", dst_addr)
			if err != nil {
				logger.Error("connect to %s failed %v", dst_addr, err)
				conn.Close()
				return
			}
			logger.Info("remote connection to %s done", dst_addr)
			connCopy(cr, conn)
		}
	})

	logger.Info("Start websocket server on port %d", port)
	http.ListenAndServe(fmt.Sprintf(":%d", port), nil)
}

// test address availability
func ping(wsaddr string) error {

	var transport *http.Transport = &http.Transport{}
	var config *tls.Config = &tls.Config{
		InsecureSkipVerify: true,
	}
	transport.TLSClientConfig = config
	client := http.Client{
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			r.URL.Opaque = r.URL.Path
			return nil
		},
		Transport: transport,
	}
	url := "http" + wsaddr[2:len(wsaddr)-2] + "ping" // replace ws://-> http:// & /ws -> /ping wss:// -> https://
	// Put content on file
	resp, err := client.Get(url)
	if err != nil {
		logger.Error("error open url %s, %v\n", url, err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		log.Printf("http failed %d\n", resp.StatusCode)
		return errors.New(fmt.Sprintf("http request failed %s", resp.Status))
	}
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		logger.Error("read body failed: %v", err)
		return errors.New("read body failed")
	}
	logger.Info("ping response: \n%s", string(body))
	return nil
}

/*
客户端， 监听一个端口， 通过ws映射到远端的端口
*/
func ServeClient(port int, wsaddr string) {
	logger.Info("WS socks5 listen on %d -> %s", port, wsaddr)
	if err := ping(wsaddr); err != nil {
		logger.Error("ping failed, exit! %v", err)
		return
	}
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		log.Println("error listening:", err.Error())
		os.Exit(1)
	}

	var InsecureDialer = &websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: 45 * time.Second,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
	}

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Println("Error accept:", err.Error())
			break
		}
		go func(conn net.Conn) {
			// c, _, err := websocket.DefaultDialer.Dial(wsaddr, nil)
			c, _, err := InsecureDialer.Dial(wsaddr, nil)
			if err != nil {
				logger.Error("websocket dial failed %s, %v", wsaddr, err)
				conn.Close()
				return
			}
			a := _add(1)
			logger.Info("websocket for %s connected, conn size: %d",
				conn.RemoteAddr(), a)
			connCopy(conn, c)
		}(conn)
	}
}

/*客户端提供socks5服务，  通过 ws + ssh 映射到远程主机*/
func ServeClientSocks(socks_port int, wsaddr string, rport int) {
	logger.Info("WS socks5 listen on %d -> %s", socks_port, wsaddr)
	if err := ping(wsaddr); err != nil {
		logger.Error("ping failed, exit! %v", err)
		return
	}

	// 启动 socks5 server
	conf := &socks5.Config{}
	server, _ := socks5.New(conf)
	go server.ListenAndServe("tcp", fmt.Sprintf("127.0.0.1:%d", socks_port))

	//WSTunnel_R(wsaddr, socks_port, rport)

	// // 建立一个 ws 连接
	// var InsecureDialer = &websocket.Dialer{
	// 	Proxy:            http.ProxyFromEnvironment,
	// 	HandshakeTimeout: 45 * time.Second,
	// 	TLSClientConfig: &tls.Config{
	// 		InsecureSkipVerify: true,
	// 	},
	// }
	// c, _, err := InsecureDialer.Dial(wsaddr, nil)
	// if err != nil {
	// 	logger.Error("websocket dial failed %s, %v", wsaddr, err)
	// 	return
	// }

}

/*

/mci/openresty/11991/nginx/conf
iasp-web-develop-local.conf  (9099)

mgr68:/data/.app/ng_pyserv/conf/nginx.conf.tamplate
location /jupyter-911/ {
  proxy_pass http://172.18.243.24:9192;
  proxy_http_version 1.1;
  proxy_set_header Upgrade $http_upgrade;
  proxy_set_header Connection "Upgrade";
  proxy_read_timeout 86400;
}

idle timeout:  proxy_read_timeout, default 60

server: fkme ws -p 9192 -s -addr s5   # 运行在 24 主机上, ~/.gg/ud2_root/ss
client: fkme ws -p 31080 -addr ws://114.255.113.43:9099/jupyter-911/ws

-- 下面是连接ssh的
server: fkme ws -p 9193 -s -addr 121.43.230.103:22
client: fkme ws -p 9192 -addr ws://127.0.0.1:9193/jupyter-911/ws
ssh-client: ssh -p 9192 root@127.0.0.1

http://114.255.113.43:9099/jupyter-911/ping





==========for minikube ladder ===========
zhr2:
cd /tmp/.fk
./fkme ws -p 8899 -prefix /yt -s -addr 127.0.0.1:22   # 监听 :8899/yt/ws  并将连接转接到 -addr 上
curl -k https://127.0.0.1/yt/ping

pc:
# 通过ws 映射远程端口到本地
fkme ws -p 7022 -addr wss://121.43.230.103:443/yt/ws

# 通过ws 映射本地socks5 服务到 远程主机(8000端口是本地新启动的socks5 端口)
curl -k -o fkme https://121.43.230.103/yt/static/fkme
chmod +x fkme
./fkme ws -p 8000 -rport 21080 -addr wss://121.43.230.103:443/yt/ws

from zhr2:   curl -x socks5h://localhost:21080 http://127.0.0.1:7171/m/  # 临时测试， 启动inoweb.py


# 在 minikube 控制台上操作
# open https://kubernetes.io/docs/tutorials/hello-minikube/
curl -k -o fkme https://121.43.230.103/yt/static/fkme
chmod +x fkme
./fkme ws -p 8000 -rport 21080 -addr wss://121.43.230.103:443/yt/ws  &

# hosts for ino_down.py:
# dependencies:  PySocks termcolor flask waitress requests
# set HTTPS_PROXY=socks5://127.0.0.1:21080
# 连接zhr2, 并映射远程端口 21080
# python ino_down.py
==================== c:\windows\system32\drivers\etc\hosts
92.247.181.40 www.inoreader.com
104.22.42.172 chinadigitaltimes.net
23.73.140.140 ichef.bbci.co.uk
188.114.97.3  botanwang.com
127.0.0.1     www.google-analytics.com

# chrome set proxy:
# --proxy-server="socks5://127.0.0.1:21080"

*/

type arrayFlags []string

func (i *arrayFlags) String() string {
	return "my string representation"
}

func (i *arrayFlags) Set(value string) error {
	*i = append(*i, value)
	return nil
}
func Run(args []string) {
	var ports arrayFlags
	cmd := flag.NewFlagSet("ws", flag.ExitOnError)
	svrport := cmd.Int("p", 0, "server port")
	prefix := cmd.String("prefix", "/yt", "URI prefix, begin with /")

	servaddr := cmd.String("addr", "", "WS server address, or target addr(server)")

	socks := cmd.Int("socks", 0, "socks listen port")
	http_port := cmd.Int("http", 0, "http proxy listen port")
	cmd.Var(&ports, "port", "")

	cmd.Parse(args)

	//if *tool != "" {
	//	switch *tool {
	//	case "gencert":
	//		createCert()
	//	case "server":
	//		server()
	//	case "client":
	//		client()
	//	}
	//	return
	//}

	if *svrport > 0 { // running server endpoint
		// 启动 web-socket 服务器, 可以在 nginx 之后, 作为端口转发服务器
		// ./fkme -p 8899 -prefix /yt -addr 127.0.0.1:9022
		dir, _ := os.Getwd()
		go ReadAll(dir)
		serveServer(*svrport, *servaddr, *prefix)
	} else if len(ports) > 0 {
		if *socks > 0 {
			// 启动 socks5 server
			conf := &socks5.Config{}
			server, _ := socks5.New(conf)
			go server.ListenAndServe("tcp", fmt.Sprintf("127.0.0.1:%d", *socks))
		}
		if *http_port > 0 {
			proxy := goproxy.NewProxyHttpServer()
			proxy.Verbose = true

			svraddr := fmt.Sprintf(":%d", *http_port)
			go http.ListenAndServe(svraddr, proxy)
		}
		// 一个连接多端口映射， 样例
		// fkme ws -addr wss://121.43.230.103:443/yt/ws -port "7022;>;2022" -port "31080;>;21080" -port "15900;>;5900"
		// fkme ws -addr wss://121.43.230.103:443/yt/ws -port "8000;<;21080" -socks 8000
		for i, p := range ports {
			fmt.Printf(" --%d = [%s]\n", i, p)
		}
		//fmt.Printf("todo something\n")
		WSTunnel_M(*servaddr, ports)
	} else {
		//ServeClient(*svrport, *servaddr)
		fmt.Printf("Nothing to do!!!!!\n")
	}
}
