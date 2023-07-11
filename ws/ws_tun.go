package ws

/* web-socket server 直接做中间人中转TCP连接

一类客户端 来自 GFW 外, 作为Tunnel 的出口
	出口使用 shadowsocks server 端
	有两种连接, 如下:
		1. command connection, 注册一个出口, 允许客户端通过其主机向其它地址建立TCP连接
		2. data-serve connection, 根据客户端请求发起的数据连接, 连接的外端是一个目标地址连接, 此目标地址由客户端提供
另一类客户端来自 GFW 内, 作为 Tunnel 的入口
	在客户端本地监听端口, 接收到连接后, 通过ws连接将此连接对接到一个 data-serve connection上



有3个进程, 分别运行在 3 个网络位置:
1. server-server
	在 GFW 内的公网IP服务器上, 作为 web-socket 的服务端, 作为中转服务, command连接闲置超过3分钟则销毁
2. client-server
	在 GFW 外, tunnel 出口, 报文定义:
	   	注册一个 server-server, 名称用于客户端识别,  此连接断开后其注册的名称应被删除(选择一个slot, 暂只支持2个(0,1))
			REQ: server-server,<server-slot>
			RES: res,OK|FAIL       成功或者失败, 失败的原因只有一个可能: slot名称重复了
			-- 请求一个 data 连接(server-server => client-server, 在server-server连接上):
				REQ: server-data,<serial-no>,<target-addr>
				RES: res,OK|FAIL        # 对端成功建立连接后返回 OK; 超时或者失败返回FAIL, 此时应销毁对应的 client-client 连接
			-- 心跳请求 (server-server => client-server), 当连接闲置超过1分钟, 则发送心跳报文, 超过3次心跳无应答, 则断开连接重新注册
				REQ: heart-beat
				RES: res,OK
		建立一个server端数据连接, 其serial-no对应请求中的值:
			REQ: server-data,<serial-no>
			RES: res,OK|FAIL-<code>
			应答成功, 就可以开始对接转发
3. client-client
	在 GFW 内, tunnel 的入口,  请求报文如下:
	REQ: client-data,<server-slot>,<serial-no>,<target-addr>
	RES: res,OK|FAIL-<code>
	如果应答成功, 就可以对接转发了


*/

import (
	"crypto/tls"
	"flag"
	"fmt"
	"github.com/gorilla/websocket"
	"github.com/lulugyf/fkme/go-socks5"
	"github.com/lulugyf/fkme/logger"
	"go.uber.org/ratelimit"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ws_upgrader = websocket.Upgrader{}

/*
封装 websocket.Conn 类型以满足 net.Conn or io.Reader or io.Writer
*/
type WSConn struct {
	c      *websocket.Conn
	remain []byte
}

func (ws *WSConn) Close() error {
	return ws.c.Close()
}

func (ws *WSConn) LocalAddr() net.Addr {
	return ws.c.LocalAddr()
}

func (ws *WSConn) RemoteAddr() net.Addr {
	return ws.c.RemoteAddr()
}

func (ws *WSConn) SetDeadline(t time.Time) error {
	return ws.c.SetReadDeadline(t)
}

func (ws *WSConn) SetReadDeadline(t time.Time) error {
	return ws.c.SetReadDeadline(t)
}

func (ws *WSConn) SetWriteDeadline(t time.Time) error {
	return ws.c.SetWriteDeadline(t)
}

func (ws *WSConn) Read(p []byte) (int, error) {
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
func (ws *WSConn) Write(p []byte) (int, error) {
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
func (ws *WSConn) Req(msg string) (string, error) {
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

type WaitWS struct { // 等待对接的 client-data 连接
	uptime int64 // 产生的时间, todo 超过 60s 就销毁
	ws     *WSConn
}
type ConnWS struct { // 已经对接的一对ws连接
	uptime int64
	conn_s *WSConn
	conn_c *websocket.Conn
}
type Slot struct {
	uptime int64
	ch     chan string
}

// 中转服务
type SrvServer struct {
	waitWS ConcurrentMap[string, *WaitWS] // serial-no -> WS(来自client-client)
	slots  ConcurrentMap[string, *Slot]
	conns  ConcurrentMap[string, *ConnWS] // 已经对接的配对连接列表, key为 <serial-no>

	srv_port   int
	uri_prefix string // URI prefix
}

func NewSrvServer(port int, uri_prefix string) *SrvServer {
	s := &SrvServer{
		waitWS:     NewMap[*WaitWS](),
		slots:      NewMap[*Slot](),
		conns:      NewMap[*ConnWS](),
		srv_port:   port,
		uri_prefix: uri_prefix,
	}

	return s
}

func (s *SrvServer) Serve() {
	logger.Info("run ws server on %d with URI: %s", s.srv_port, s.uri_prefix)

	rl := ratelimit.New(20) // per second
	http.HandleFunc(fmt.Sprintf("%s/ping", s.uri_prefix), func(w http.ResponseWriter, r *http.Request) {
		h := []string{}
		for k, v := range r.Header {
			h = append(h, fmt.Sprintf("%s: %s", k, v))
		}
		fmt.Fprintf(w, "hello:\n%s\n", strings.Join(h, "\n"))
	})
	static_dir := "static"
	uri := fmt.Sprintf("%s/%s/", s.uri_prefix, static_dir)
	mydir := fmt.Sprintf("%s/", static_dir)
	http.Handle(uri, http.StripPrefix(uri, http.FileServer(http.Dir(mydir))))
	http.HandleFunc(fmt.Sprintf("%s/ws", s.uri_prefix), func(w http.ResponseWriter, r *http.Request) {
		rl.Take()
		conn, err := ws_upgrader.Upgrade(w, r, nil)
		if err != nil {
			logger.Error("upgrade failed: %v", err)
			return
		}
		logger.Info("new income ws connection, %v", conn.RemoteAddr())
		_, req_bytes, err := conn.ReadMessage()
		if err != nil {
			logger.Error("readMessage failed: %v", err)
			conn.Close()
			return
		}
		req := strings.Split(string(req_bytes), ",")
		cmd := req[0]
		if cmd == "server-server" && len(req) == 2 { // server-server,<server-slot>
			// 从外部来的 cmd 连接, 连接保持
			slot := req[1]
			_, prs := s.slots.Get(slot)
			if prs {
				logger.Error("slot [%s] already exists, close it", slot)
				conn.WriteMessage(websocket.TextMessage, []byte("res,FAIL"))
				conn.Close()
				return
			}
			err := conn.WriteMessage(websocket.TextMessage, []byte("res,OK"))
			if err != nil {
				logger.Error("write ok-response of server-server failed, %v", err)
				conn.Close()
				return
			}
			ss := &Slot{uptime: time.Now().Unix(), ch: make(chan string)}
			s.slots.Set(slot, ss)
			go s.SrvServer(&WSConn{c: conn}, ss, slot)
		} else if cmd == "server-data" && len(req) == 2 {
			// 从外部来的数据连接
			ser_no := req[1]
			k, prs := s.waitWS.Get(ser_no)
			if !prs {
				logger.Warn("server-data [%s] but serial-no not found", ser_no)
				conn.WriteMessage(websocket.TextMessage, []byte("res,FAIL-1043"))
				conn.Close()
			} else {
				s.waitWS.Remove(ser_no)
				err := conn.WriteMessage(websocket.TextMessage, []byte("res,OK"))
				if err != nil {
					conn.Close()
					k.ws.Close()
				} else {
					err := k.ws.c.WriteMessage(websocket.TextMessage, []byte("res,OK"))
					if err != nil {
						logger.Error("response to client-data failed, %v", err)
						conn.Close()
						k.ws.c.Close()
					} else {
						ws := &WSConn{c: conn}
						cc := &ConnWS{uptime: time.Now().Unix(), conn_s: ws, conn_c: k.ws.c}
						s.conns.Set(ser_no, cc)
						s.connCopy(ser_no, cc)
						logger.Info("===new conn [%s]", ser_no)
					}
				}
			}
		} else if cmd == "client-data" && len(req) == 4 {
			slot_name := req[1]
			ser_no := req[2]
			target_addr := req[3]
			slot, prs := s.slots.Get(slot_name)
			if !prs {
				logger.Warn("slot not found [%s]", slot)
				conn.WriteMessage(websocket.TextMessage, []byte("res,FAIL-1043"))
				conn.Close()
			} else {
				s.waitWS.Set(ser_no, &WaitWS{uptime: time.Now().Unix(), ws: &WSConn{c: conn}})
				req_str := fmt.Sprintf("conn,%s,%s", ser_no, target_addr)
				slot.ch <- req_str
				logger.Info("new client-data to %s with ser_no %s", target_addr, ser_no)
			}

		} else {
			logger.Error("invalid command [%s]", req[0])
			conn.Close()
		}
	})

	logger.Info("Start websocket server on port %d with prefix %s", s.srv_port, s.uri_prefix)
	http.ListenAndServe(fmt.Sprintf(":%d", s.srv_port), nil)
}

func (s *SrvServer) connCopy(serial_no string, c *ConnWS) {
	once := sync.Once{}
	close := func() {
		c.conn_s.Close()
		logger.Info("connection for %s closed", c.conn_s.RemoteAddr())
		c.conn_c.Close()
		s.conns.Remove(serial_no)
	}
	ws1 := &WSConn{c: c.conn_c}
	go func() {
		io.Copy(ws1, c.conn_s)
		once.Do(close)
	}()

	go func() {
		io.Copy(c.conn_s, ws1)
		once.Do(close)
	}()
}

func (s *SrvServer) SrvServer(c *WSConn, slot *Slot, slotname string) {
	logger.Info("Server-Server on slot [%s]", slotname)
	timeout_counter := 0
	defer func() {
		c.Close()
		s.slots.Remove(slotname)
		logger.Warn("server-server [%s] exited", slotname)
	}()
	running := true
	for running {
		select {
		case msg := <-slot.ch:
			logger.Info("got message: [%s]", msg)
			// msg format: conn,<serial-no>,<target-addr>  or quit
			if msg == "quit" {
				running = false
				break
			} else {
				q := strings.Split(msg, ",")
				if q[0] != "conn" || len(q) != 3 {
					logger.Warn("invalid server-server message: [%s]", msg)
					break
				}
				ser_no := q[1]
				dst_addr := q[2]
				req := fmt.Sprintf("server-data,%s,%s", ser_no, dst_addr)
				res, err := c.Req(req)
				if err != nil || res != "res,OK" {
					logger.Error("request a server-data to [%s] failed", dst_addr)
					if err != nil {
						running = false
					}
					// close and remove the cli-cli connection
					k, prs := s.waitWS.Get(q[1])
					if prs {
						s.waitWS.Remove(q[1])
						k.ws.Close()
					}
					break
				}
				timeout_counter = 0
			}
		case <-time.After(10 * time.Second):
			succ := false
			res, err := c.Req("heart-beat")
			if err != nil {
				logger.Error("write heart-beat failed, exit")
				running = false
				break
			}
			if "res,OK" != res {
				timeout_counter += 1
				if timeout_counter > 2 {
					running = false
					break
				}
			} else {
				succ = true
				timeout_counter = 0
			}
			logger.Info("timeout, send heart-beat, result: %v", succ)
		}
	}
}

func ws_connect(addr string, req_str string) *WSConn {
	if !strings.HasPrefix(addr, "ws://") && !strings.HasPrefix(addr, "wss://") {
		logger.Error("invalid ws_addr: [%s], cancel", addr)
		return nil
	}

	// 通过 ws 连接产生 ssh.Client
	var InsecureWSDialer = &websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: 45 * time.Second,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
	}
	conn, _, err := InsecureWSDialer.Dial(addr, nil)
	if err != nil {
		logger.Error("websocket dial failed %s, %v\n", addr, err)
		return nil
	}
	ws := &WSConn{c: conn}
	res, err := ws.Req(req_str)
	if err != nil || res != "res,OK" {
		logger.Error("register server-server failed [%s] [%v]", res, err)
		conn.Close()
		return nil
	}
	return ws
}

type CliServer struct {
	slot_name   string
	ws_addr     string
	target_addr string
}

func (s *CliServer) Serv() {
	ws := ws_connect(s.ws_addr, fmt.Sprintf("server-server,%s", s.slot_name))
	if ws == nil {
		return
	}
	logger.Info("register slot: %s success", s.slot_name)
	limit := ratelimit.New(20)
	defer ws.Close()

	var s5 *socks5.Server = nil
	conf := &socks5.Config{}
	s5, err := socks5.New(conf)
	if err != nil {
		panic(err)
	}
	//go s5.ListenAndServe()

	for {
		limit.Take()
		_, qdata, err := ws.c.ReadMessage()
		if err != nil {
			logger.Error("read message failed, %v", err)
			break
		}
		req := strings.Split(string(qdata), ",")
		cmd := req[0]
		if cmd == "heart-beat" {
			err := ws.c.WriteMessage(websocket.TextMessage, []byte("res,OK"))
			if err != nil {
				logger.Error("write response of heart-beat failed, %v", err)
				break
			}
			logger.Info("heart-beat response!!")
		} else if cmd == "server-data" && len(req) == 3 {
			ser_no := req[1]
			target_addr := req[2]

			if target_addr == "s5" {
				err := ws.c.WriteMessage(websocket.TextMessage, []byte("res,OK"))
				if err != nil {
					logger.Error("write ok-response of conn failed")
					break
				}
				ws := ws_connect(s.ws_addr, fmt.Sprintf("server-data,%s", ser_no))
				if ws == nil {
					ws.c.WriteMessage(websocket.TextMessage, []byte("res,FAIL-1044"))
					logger.Error("connect server-data failed, close it")
				} else {
					go s5.ServeConn(ws)
					logger.Info("new socks5 connection")
				}
				continue
			}
			cr, err := net.Dial("tcp", target_addr)
			if err != nil {
				logger.Error("connect to %s failed %v", target_addr, err)
				err := ws.c.WriteMessage(websocket.TextMessage, []byte("res,FAIL-1044"))
				if err != nil {
					logger.Error("write fail-response of server-data failed, %v", err)
					break
				}
			} else {
				logger.Info("remote connected to %s", target_addr)
				err := ws.c.WriteMessage(websocket.TextMessage, []byte("res,OK"))
				if err != nil {
					logger.Error("write ok-response of server-data failed, %v", err)
					cr.Close()
					break
				}
				go s.connCopy(cr, ser_no)
			}
		} else {
			logger.Warn("invalid command: %s", cmd)
		}

	}

}

/*
对接外部连接
*/
func (s *CliServer) connCopy(conn net.Conn, ser_no string) {
	ws := ws_connect(s.ws_addr, fmt.Sprintf("server-data,%s", ser_no))
	if ws == nil {
		conn.Close()
		logger.Error("connect server-data failed, close it")
		return
	}

	raddr := conn.RemoteAddr()
	once := sync.Once{}
	close := func() {
		conn.Close()
		ws.Close()
		logger.Info("connection for %s closed", raddr)
	}
	go func() {
		io.Copy(conn, ws)
		once.Do(close)
	}()

	io.Copy(ws, conn)
	once.Do(close)
}

type CliClient struct {
	slot_name   string
	ws_addr     string
	ser_no_seed uint16
	listen_port int
	target_addr string
}

func (s *CliClient) Serv() {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", s.listen_port))
	if err != nil {
		logger.Error("listen on %d failed, %v", s.listen_port, err)
		return
	}
	defer listener.Close()

	running := true
	limit := ratelimit.New(20)
	for running {
		conn, err := listener.Accept()
		if err != nil {
			logger.Error("Error accept: %v", err)
			break
		}
		limit.Take()
		s.ser_no_seed += 1
		ser_no := fmt.Sprintf("%s-%d", s.slot_name, s.ser_no_seed)
		go s.connCopy(conn, ser_no)
	}
}

func (s *CliClient) connCopy(conn net.Conn, ser_no string) {
	ws := ws_connect(s.ws_addr, fmt.Sprintf("client-data,%s,%s,%s", s.slot_name, ser_no, s.target_addr))
	if ws == nil {
		conn.Close()
		logger.Error("connect server-data failed, close it")
		return
	}

	raddr := conn.RemoteAddr()
	once := sync.Once{}
	close := func() {
		conn.Close()
		ws.Close()
		logger.Info("connection for %s closed", raddr)
	}
	go func() {
		io.Copy(conn, ws)
		once.Do(close)
	}()

	io.Copy(ws, conn)
	once.Do(close)
}

/*
*
原来的 zhr2 上的命令: ./fkme ws -p 8899 -prefix /yt -s -addr 127.0.0.1:22
新的  zhr2 上的命令: ./fkme1 wsmid -mode server-server -p 8899 -prefix /yt

curl -o go-ss -k https://121.43.230.103/yt/static/go-ss
curl -o s -k https://121.43.230.103/yt/static/s

./go-ss -s 'ss://AEAD_CHACHA20_POLY1305:mypass@:8488' -ws wss://121.43.230.103:443/yt/ws

fkme wsmid -m ss -p 8899 -prefix /yt
fkme wsmid -m cs -slot dd -ws ws://127.0.0.1:8899/yt/ws
fkme wsmid -m cc -slot dd -ws ws://127.0.0.1:8899/yt/ws -p 31080 -to s5
*/
func WS_CliServer(ws_addr, pass string) {
	ws := &CliServer{ws_addr: ws_addr, slot_name: pass}
	ws.Serv()
}

func WS_CliClient(ws_addr string, pass string, addr string) {
	t := strings.Split(addr, ":")
	port, err := strconv.Atoi(t[1])
	if err != nil {
		logger.Error("invalid target addr [%s], err: %v", addr, err)
		return
	}
	ws := &CliClient{ws_addr: ws_addr, slot_name: pass, listen_port: int(port), target_addr: addr}
	ws.Serv()
}

func WSMidServ(args []string) {
	logger.InitLogger("")

	cmd := flag.NewFlagSet("wsmid", flag.ExitOnError)
	port := cmd.Int("p", 8899, "server port")
	mode := cmd.String("m", "server-server", "Work mode: server-server|client-server|client-client")
	prefix := cmd.String("prefix", "/yt", "URI prefix, begin with /")

	ws_addr := cmd.String("ws", "", "WS server address")
	slot := cmd.String("slot", "", "choose a slot name")
	to := cmd.String("to", "", "target address")
	cmd.Parse(args)

	m := *mode
	if m == "server-server" || m == "ss" {
		// fkme wsmid -mode server-server -p 8899 -prefix /yt
		ws := NewSrvServer(*port, *prefix)
		ws.Serve()
	} else if m == "client-server" || m == "cs" {
		// fkme wsmid -mode client-server -ws ws://127.0.0.1:8899/yt/ws -slot slot1
		ws := &CliServer{ws_addr: *ws_addr, slot_name: *slot}
		ws.Serv()
	} else if m == "client-client" || m == "cc" {
		// fkme wsmid -mode client-client -ws ws://127.0.0.1:8899/yt/ws -slot slot1 -p 7711 -to 127.0.0.1:9191
		// start fkme w
		// curl http://127.0.0.1:7711/hello
		ws := &CliClient{ws_addr: *ws_addr, slot_name: *slot, listen_port: *port, target_addr: *to}
		ws.Serv()
	}
}
