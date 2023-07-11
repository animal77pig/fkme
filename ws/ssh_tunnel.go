package ws

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
)

type KeepAliveConfig struct {
	Interval uint
	CountMax uint
}

type _port struct {
	mode     byte // '>' for forward, '<' for reverse
	bindAddr string
	dialAddr string
}
type tunnel struct {
	auth          []ssh.AuthMethod
	hostKeys      ssh.HostKeyCallback
	user          string
	hostAddr      string
	retryInterval time.Duration
	keepAlive     KeepAliveConfig

	//mode     byte // '>' for forward, '<' for reverse
	//bindAddr string
	//dialAddr string

	ports []_port // 单个连接多个端口映射， 只有单个端口的时候使用上面的配置， 多于一个其它的则在此配置
}

func (t _port) bind(ctx context.Context, wg *sync.WaitGroup, cl *ssh.Client) {
	var err error
	var once sync.Once

	// Attempt to bind to the inbound socket.
	var ln net.Listener

	defer wg.Done()

	switch t.mode {
	case '>':
		ln, err = net.Listen("tcp", t.bindAddr)
	case '<':
		ln, err = cl.Listen("tcp", t.bindAddr)
	}
	if err != nil {
		once.Do(func() { fmt.Printf("(%v) bind error: %v\n", t, err) })
		return
	}

	// The socket is binded. Make sure we close it eventually.
	bindCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		cl.Wait()
		cancel()
	}()
	go func() {
		<-bindCtx.Done()
		once.Do(func() {}) // Suppress future errors
		ln.Close()
	}()

	fmt.Printf("(%v) binded tunnel\n", t)
	defer fmt.Printf("(%v) collapsed tunnel\n", t)

	// Accept all incoming connections.
	for {
		cn1, err := ln.Accept()
		if err != nil {
			once.Do(func() { fmt.Printf("(%v) accept error: %v\n", t, err) })
			return
		}
		wg.Add(1)
		go t.dialTunnel(bindCtx, wg, cl, cn1)
	}
}

func (t _port) dialTunnel(ctx context.Context, wg *sync.WaitGroup, client *ssh.Client, cn1 net.Conn) {
	defer wg.Done()

	// The inbound connection is established. Make sure we close it eventually.
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-connCtx.Done()
		cn1.Close()
	}()

	// Establish the outbound connection.
	var cn2 net.Conn
	var err error
	switch t.mode {
	case '>':
		cn2, err = client.Dial("tcp", t.dialAddr)
	case '<':
		cn2, err = net.Dial("tcp", t.dialAddr)
	}
	if err != nil {
		fmt.Printf("(%v) dial error: %v\n", t, err)
		return
	}

	go func() {
		<-connCtx.Done()
		cn2.Close()
	}()

	fmt.Printf("(%v) connection established\n", t)
	defer fmt.Printf("(%v) connection closed\n", t)

	// Copy bytes from one connection to the other until one side closes.
	var once sync.Once
	var wg2 sync.WaitGroup
	wg2.Add(2)
	go func() {
		defer wg2.Done()
		defer cancel()
		if _, err := io.Copy(cn1, cn2); err != nil {
			once.Do(func() { fmt.Printf("(%v) connection error: %v", t, err) })
		}
		once.Do(func() {}) // Suppress future errors
	}()
	go func() {
		defer wg2.Done()
		defer cancel()
		if _, err := io.Copy(cn2, cn1); err != nil {
			once.Do(func() { fmt.Printf("(%v) connection error: %v", t, err) })
		}
		once.Do(func() {}) // Suppress future errors
	}()
	wg2.Wait()
}

func (t _port) String() string {
	var left, right string
	mode := "<?>"
	switch t.mode {
	case '>':
		left, mode, right = t.bindAddr, "->", t.dialAddr
	case '<':
		left, mode, right = t.dialAddr, "<-", t.bindAddr
	}
	return fmt.Sprintf("%s %s %s", left, mode, right)
}

//func (t tunnel) String() string {
//	var left, right string
//	mode := "<?>"
//	switch t.mode {
//	case '>':
//		left, mode, right = t.bindAddr, "->", t.dialAddr
//	case '<':
//		left, mode, right = t.dialAddr, "<-", t.bindAddr
//	}
//	return fmt.Sprintf("%s@%s | %s %s %s", t.user, t.hostAddr, left, mode, right)
//}

func (t tunnel) bindTunnel(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	for {
		var once sync.Once // Only print errors once per session
		func() {
			// Connect to the server host via SSH.
			var cl *ssh.Client = nil
			var err error

			if strings.HasPrefix(t.hostAddr, "ws://") || strings.HasPrefix(t.hostAddr, "wss://") {

				// 通过 ws 连接产生 ssh.Client
				var InsecureWSDialer = &websocket.Dialer{
					Proxy:            http.ProxyFromEnvironment,
					HandshakeTimeout: 45 * time.Second,
					TLSClientConfig: &tls.Config{
						InsecureSkipVerify: true,
					},
				}
				conn, _, err := InsecureWSDialer.Dial(t.hostAddr, nil)
				if err != nil {
					fmt.Printf("websocket dial failed %s, %v\n", t.hostAddr, err)
					return
				}
				config := &ssh.ClientConfig{
					User:            t.user,
					Auth:            t.auth,
					HostKeyCallback: t.hostKeys,
					Timeout:         5 * time.Second,
				}
				ws := &WS{c: conn}
				c, chans, reqs, err := ssh.NewClientConn(ws, "127.0.0.1:22", config)
				if err != nil {
					fmt.Printf("ssh.NewClientConn failed: %v\n", err)
					return
				}
				cl = ssh.NewClient(c, chans, reqs)
			} else {
				cl, err = ssh.Dial("tcp", t.hostAddr, &ssh.ClientConfig{
					User:            t.user,
					Auth:            t.auth,
					HostKeyCallback: t.hostKeys,
					Timeout:         5 * time.Second,
				})
				if err != nil {
					once.Do(func() { fmt.Printf("(%v) SSH dial error: %v\n", t, err) })
					return
				}
			}
			wg.Add(1)
			go t.keepAliveMonitor(&once, wg, cl)
			defer cl.Close()

			// 多端口映射
			for _, port := range t.ports[1:] {
				go port.bind(ctx, wg, cl)
			}
			t.ports[0].bind(ctx, wg, cl)
		}()

		select {
		case <-ctx.Done():
			return
		case <-time.After(t.retryInterval):
			fmt.Printf("(%v) retrying...\n", t)
		}
	}
}

// keepAliveMonitor periodically sends messages to invoke a response.
// If the server does not respond after some period of time,
// assume that the underlying net.Conn abruptly died.
func (t tunnel) keepAliveMonitor(once *sync.Once, wg *sync.WaitGroup, client *ssh.Client) {
	defer wg.Done()
	if t.keepAlive.Interval == 0 || t.keepAlive.CountMax == 0 {
		return
	}

	// Detect when the SSH connection is closed.
	wait := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		wait <- client.Wait()
	}()

	// Repeatedly check if the remote server is still alive.
	var aliveCount int32
	ticker := time.NewTicker(time.Duration(t.keepAlive.Interval) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case err := <-wait:
			if err != nil && err != io.EOF {
				once.Do(func() { fmt.Printf("(%v) SSH error: %v", t, err) })
			}
			return
		case <-ticker.C:
			if n := atomic.AddInt32(&aliveCount, 1); n > int32(t.keepAlive.CountMax) {
				once.Do(func() { fmt.Printf("(%v) SSH keep-alive termination", t) })
				client.Close()
				return
			}
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
			if err == nil {
				atomic.StoreInt32(&aliveCount, 0)
			}
		}()
	}
}

const priv_key string = `-----BEGIN RSA PRIVATE KEY-----
MIIEogIBAAKCAQEAlG8b7EnPT55JzWler7UsNArF8rB+YXG3UktFg0Mzxb6/RLcl
GkDGT0InCtSo1i6fCHZX9XIdxypxMVYLyFilFG4y5pv7KMlG1b70UKbLd2cd9ZpK
9KGihvCYx7QnMrEyDNSGg19nSLOeZm3cyneClniZhhYRDwABIJEah9FkvEMgB5p6
BCHENJRVyNJMjq1tNqrRwKWEl0GFih2xtCs74408y5UeIxCYCBeH7gJDxXVHoMMq
GRFJl77Xlt7NUgOKtdj+8kkPJYoJ1YONYC9OdTfdUWI98nV+uB8RrDavwZuretBl
EQFvp9ol3aR1jRaICX+S3g5nWrOwiIBOuuda2wIDAQABAoIBACK37l8RUJU93+NU
7xnIFaPClVRTpevi7k8oXgT61gQ9vn0zHVGLrxbg0UL+RNN8KiSPkblOTNrF+Z3h
k3X0DgC+WdeIynFayt+5/2lR6ituihplUXzwxZQseH/ViomX2q4Xk7LswLrHkJhC
wC73Tysk4Dv1s12/0YOtjPgRqS9DZOuCWMx4IGjQb0+AUGLeQtXf0SocKB2VSb8F
k97UXmFa3wx/4FqTu1jRsuipZkPOCFowgN2iDBsFl3JPqAYc5SJ+XDbg/YeNgjn8
MPY5QLN9EP34PECU/ZIrQd813CmgifKeOtnI1niWKy3QWHKKA+bRhlJZHTQYycTa
b+SVR0kCgYEAxObs+NsYmMorK2xiLhhEXyOdQn9Cb0UOLMX5zxUhgdyQYMymUCoe
MlFuJDlKR601conhSh7k5uCHvUAlTzbE+GWPijFt3Zy9vi95A3VwIlD28j5StsD7
bxPy7TNL4Aks1NSBQ4uQlX9R05RF5QxzIk+z+0G0lfvpTrlYcIHwvYcCgYEAwPwd
vhpfx51/9ZsNzkfwapviie3hO+/LlPL6tDRAd4w4Fr3WBg+5iKh/vkhg06Scaqd5
/2eyJKwys+8HvrUSqYECpsARYR8RPQLFd4XfeOJXUndCU7E/Tn+zocR1Hh7cgA47
iJiAS7a5uZyyC5YOn4MHE5z2JAzgGERJ6Fv7LQ0CgYArOZKeEuL0b7VIZBOtkNA5
noTgWzWHXb5937w2VKo1aukbBvIfuQ9F9pBaTWVcFM8d5NzbO6r+cB38Ur+eAyT8
brczHCTFOKqCvMMxGi/SqLl9dmcMDZNk0BlNLyyh8wGvezMhU9sapoedDfjGDpSb
3KljKApvvox6JsAeergRswKBgFd37eMkARVwhXbEeFVutcEcNmldsCCCZztzhb33
kOCeZS2pjT/iEK2n8X5FP92tVlfg4KKqVUvZ4IE9bb06ROMe3hzGIRpsAlwszWOH
AerAa+OsuhtE0vS5XKmNaaflRPuld8ZJmJy4jSVbqDcoJCiYMrTpB4b/bvKQwQ7X
4dhhAoGAEPdZZ2uYsu2V8YbEWghG3VbO/V/0CxlSUmYr7RA1nNfejqsX1vcKtX97
OEGCLpN2Qgg0g7E3bxaB6ZZ0x4qQd9GXUUPh45BTCyHqesuHCMG+QI3G8wB1tYFC
afjqjRktOHhAAZjjjSTyYbABlg7HJjGPqyqjRiOhEcQ0JP0Szwc=
-----END RSA PRIVATE KEY-----`

//func loadConfig(ws_url string, socks_port, rport int) (tunns []tunnel) {
//
//	// 1.) Build Auth Agent and Config
//	var auth []ssh.AuthMethod
//	var pass_or_keyfile = "D:\\devtool\\bin\\id_rsa"
//	pass_or_keyfile = ""
//
//	_, err := os.Stat(pass_or_keyfile) // if os.IsNotExists(err)
//	if err == nil {
//		pemBytes, err := ioutil.ReadFile(pass_or_keyfile)
//		signer, err := ssh.ParsePrivateKey(pemBytes)
//		if err == nil {
//			auth = append(auth, ssh.PublicKeys(signer))
//		}
//	} else {
//		if pass_or_keyfile == "" {
//			signer, err := ssh.ParsePrivateKey([]byte(priv_key))
//			if err == nil {
//				auth = append(auth, ssh.PublicKeys(signer))
//			}
//		} else {
//			auth = append(auth, ssh.Password(pass_or_keyfile))
//		}
//
//	}
//
//	var tunn2 tunnel
//	tunn2.auth = auth
//	tunn2.hostKeys = func(hostname string, remote net.Addr, key ssh.PublicKey) error {
//		return nil
//	}
//	tunn2.mode = '<' // '>' for forward, '<' for reverse
//	tunn2.user = "_base_"
//	if ws_url == "" {
//		//tunn2.hostAddr = net.JoinHostPort("121.43.230.103", "9022")
//	} else {
//		tunn2.hostAddr = ws_url
//	}
//	tunn2.bindAddr = fmt.Sprintf("localhost:%d", rport)
//	tunn2.dialAddr = fmt.Sprintf("localhost:%d", socks_port)
//	tunn2.retryInterval = 300 * time.Second
//	//tunn1.keepAlive = *KeepAliveConfig
//	tunns = append(tunns, tunn2)
//
//	return tunns
//}

/*
*
单 ssh 连接多端口映射
ports 格式：

	“{lport}.>.{rport}”  (local)
	"{lport}.<.{rport}"  (remote)
*/
func WSTunnel_M(ws_url string, ports []string) {
	var tunn tunnel

	var auth []ssh.AuthMethod
	signer, err := ssh.ParsePrivateKey([]byte(priv_key))
	if err == nil {
		auth = append(auth, ssh.PublicKeys(signer))
	}

	tunn.auth = auth
	tunn.hostKeys = func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		return nil
	}
	tunn.user = "_base_"
	tunn.hostAddr = ws_url
	tunn.retryInterval = 300 * time.Second
	for _, pp := range ports {
		var _p _port
		ps := strings.Split(pp, ";")
		_p.mode = ps[1][0] // '>' for forward, '<' for reverse
		if _p.mode == '>' {
			_p.bindAddr = fmt.Sprintf(":%s", ps[0]) // 监听的本地端口改为在全部IP上
			_p.dialAddr = fmt.Sprintf("localhost:%s", ps[2])

		} else if _p.mode == '<' {
			_p.bindAddr = fmt.Sprintf("localhost:%s", ps[2])
			_p.dialAddr = fmt.Sprintf("localhost:%s", ps[0])
		}
		tunn.ports = append(tunn.ports, _p)
	}

	//log.Printf("tunnel len: %d\n", len(tunns))

	// Setup signal handler to initiate shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		sigc := make(chan os.Signal, 1)
		signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
		fmt.Printf("received %v - initiating shutdown\n", <-sigc)
		cancel()
	}()

	// Start a bridge for each tunnel.
	var wg sync.WaitGroup
	fmt.Printf("%s starting\n", path.Base(os.Args[0]))
	defer fmt.Printf("%s shutdown\n", path.Base(os.Args[0]))
	//for _, t := range tunns {
	//	wg.Add(1)
	//	go t.bindTunnel(ctx, &wg)
	//}
	wg.Add(1)
	go tunn.bindTunnel(ctx, &wg)

	wg.Wait()
}
