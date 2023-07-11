package scp

// from  https://gist.github.com/0187773933/0f1061d6ada5333dbe462ae2bacd7bbd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/signal"
	"path"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
)

// https://github.com/dsnet/sshtunnel
var version string

type TunnelConfig struct {
	// LogFile is where the proxy daemon will direct its output log.
	// If the path is empty, then the server will output to os.Stderr.
	LogFile string `json:",omitempty"`

	// KeyFiles is a list of SSH private key files.
	KeyFiles []string

	// KnownHostFiles is a list of key database files for host public keys
	// in the OpenSSH known_hosts file format.
	KnownHostFiles []string

	// KeepAlive sets the keep alive settings for each SSH connection.
	// It is recommended that these values match the AliveInterval and
	// AliveCountMax parameters on the remote OpenSSH server.
	// If unset, then the default is an interval of 30s with 2 max counts.
	KeepAlive *KeepAliveConfig `json:",omitempty"`

	// Tunnels is a list of tunnels to establish.
	// The same set of SSH keys will be used to authenticate the
	// SSH connection for each server.
	Tunnels []struct {
		// Tunnel is a pair of host:port endpoints that can be configured
		// to either operate as a forward tunnel or a reverse tunnel.
		//
		// The syntax of a forward tunnel is:
		//	"bind_address:port -> dial_address:port"
		//
		// A forward tunnel opens a listening TCP socket on the
		// local side (at bind_address:port) and proxies all traffic to a
		// socket on the remote side (at dial_address:port).
		//
		// The syntax of a reverse tunnel is:
		//	"dial_address:port <- bind_address:port"
		//
		// A reverse tunnel opens a listening TCP socket on the
		// remote side (at bind_address:port) and proxies all traffic to a
		// socket on the local side (at dial_address:port).
		Tunnel string

		// Server is a remote SSH host. It has the following syntax:
		//	"user@host:port"
		//
		// If the user is missing, then it defaults to the current process user.
		// If the port is missing, then it defaults to 22.
		Server string

		// KeepAlive is a tunnel-specific setting of the global KeepAlive.
		// If unspecified, it uses the global KeepAlive settings.
		KeepAlive *KeepAliveConfig `json:",omitempty"`
	}
}

type KeepAliveConfig struct {
	// Interval is the amount of time in seconds to wait before the
	// tunnel client will send a keep-alive message to ensure some minimum
	// traffic on the SSH connection.
	Interval uint

	// CountMax is the maximum number of consecutive failed responses to
	// keep-alive messages the client is willing to tolerate before considering
	// the SSH connection as dead.
	CountMax uint
}

type tunnel struct {
	auth          []ssh.AuthMethod
	hostKeys      ssh.HostKeyCallback
	mode          byte // '>' for forward, '<' for reverse
	user          string
	hostAddr      string
	bindAddr      string
	dialAddr      string
	retryInterval time.Duration
	keepAlive     KeepAliveConfig
	//log logger
}

func (t tunnel) String() string {
	var left, right string
	mode := "<?>"
	switch t.mode {
	case '>':
		left, mode, right = t.bindAddr, "->", t.dialAddr
	case '<':
		left, mode, right = t.dialAddr, "<-", t.bindAddr
	}
	return fmt.Sprintf("%s@%s | %s %s %s", t.user, t.hostAddr, left, mode, right)
}

func (t tunnel) bindTunnel(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	for {
		var once sync.Once // Only print errors once per session
		func() {
			// Connect to the server host via SSH.
			cl, err := ssh.Dial("tcp", t.hostAddr, &ssh.ClientConfig{
				User:            t.user,
				Auth:            t.auth,
				HostKeyCallback: t.hostKeys,
				Timeout:         5 * time.Second,
			})
			if err != nil {
				once.Do(func() { fmt.Printf("(%v) SSH dial error: %v\n", t, err) })
				return
			}
			wg.Add(1)
			go t.keepAliveMonitor(&once, wg, cl)
			defer cl.Close()

			// Attempt to bind to the inbound socket.
			var ln net.Listener
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
		}()

		select {
		case <-ctx.Done():
			return
		case <-time.After(t.retryInterval):
			fmt.Printf("(%v) retrying...\n", t)
		}
	}
}

func (t tunnel) dialTunnel(ctx context.Context, wg *sync.WaitGroup, client *ssh.Client, cn1 net.Conn) {
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
		fmt.Printf("(%v) dial error: %v", t, err)
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

type TunnelConf struct {
	Pass_OR_Keyfile string `json:"keyfile"`

	Tunnels []struct {
		// The syntax of a forward tunnel is:
		//	"bind_address:port -> dial_address:port"
		// The syntax of a reverse tunnel is:
		//	"dial_address:port <- bind_address:port"
		Tunnel string `json:"tunnel"`

		// Server is a remote SSH host. It has the following syntax:
		//	"user@host:port"
		//
		// If the user is missing, then it defaults to the current process user.
		// If the port is missing, then it defaults to 22.
		Server   string `json:"server"`
		RetrySec int    `json:"retry_sec`
	} `json: tunnels`
}

/*
:: on client
{
	"keyfile":"d:/devtool/bin/id_rsa",
	"tunnels":[
		{"tunnel":"localhost:7122 -> localhost:2022", "server":"app@121.43.230.103:22", "retry_sec":30}
	]
}

:: on office desktop
{
	"keyfile":"d:/devtool/bin/id_rsa",
	"tunnels":[
		{"tunnel":"localhost:2022 <- localhost:2022", "server":"app@121.43.230.103:22", "retry_sec":300}
	]
}

*/
func loadConf(conf_file string) (tunns []tunnel, closer func() error) {
	configJson, err := ioutil.ReadFile(conf_file)
	if err != nil {
		log.Printf("ReadFile Error:%v\n", err)
		return nil, closer
	}
	var conf TunnelConf
	err = json.Unmarshal(configJson, &conf)
	if err != nil {
		log.Printf("json decode failed: %v\n", err)
		return nil, closer
	}
	var auth []ssh.AuthMethod
	_, err = os.Stat(conf.Pass_OR_Keyfile)
	if err == nil {
		pemBytes, err := ioutil.ReadFile(conf.Pass_OR_Keyfile)
		signer, err := ssh.ParsePrivateKey(pemBytes)
		if err == nil {
			auth = append(auth, ssh.PublicKeys(signer))
		}
	} else {
		auth = append(auth, ssh.Password(conf.Pass_OR_Keyfile))
	}

	for _, t := range conf.Tunnels {

		var tunn tunnel
		tunn.auth = auth
		tunn.hostKeys = func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			return nil
		}

		// user@172.18.231.76:7122
		re := regexp.MustCompile("^([^@]+)@([^:]+):([0-9]+)")
		reout := re.FindStringSubmatch(t.Server)
		if len(reout) > 0 {
			tunn.user = reout[1]
			tunn.hostAddr = net.JoinHostPort(reout[2], reout[3])
		} else {
			log.Printf("--- %v, %d [%s]\n", reout, len(reout), t.Server)
			continue
		}
		tt := strings.Split(t.Tunnel, " ")
		if len(tt) != 3 {
			log.Printf("invalid Tunnel config: %s\n", t.Tunnel)
			continue
		}
		tunn.retryInterval = time.Duration(t.RetrySec) * time.Second
		if tt[1] == "->" { // forward tunnel
			tunn.mode = '>'
			tunn.bindAddr = tt[0]
			tunn.dialAddr = tt[2]
			//tunn1.keepAlive = *KeepAliveConfig
			tunns = append(tunns, tunn)
		} else if tt[1] == "<-" { // reverse tunnel
			tunn.mode = '<'
			tunn.bindAddr = tt[0]
			tunn.dialAddr = tt[2]
			tunns = append(tunns, tunn)
		}
	}
	return tunns, closer
}

func loadConfig(is_remote bool) (tunns []tunnel, closer func() error) {

	// 1.) Build Auth Agent and Config
	var auth []ssh.AuthMethod
	var pass_or_keyfile = "D:\\devtool\\bin\\id_rsa"

	_, err := os.Stat(pass_or_keyfile) // if os.IsNotExists(err)
	if err == nil {
		pemBytes, err := ioutil.ReadFile(pass_or_keyfile)
		signer, err := ssh.ParsePrivateKey(pemBytes)
		if err == nil {
			auth = append(auth, ssh.PublicKeys(signer))
		}
	} else {
		auth = append(auth, ssh.Password(pass_or_keyfile))

	}

	// case "->":
	// 	tunn.bindAddr, tunn.mode, tunn.dialAddr = tt[0], '>', tt[2]
	// case "<-":
	// 	tunn.dialAddr, tunn.mode, tunn.bindAddr = tt[0], '<', tt[2]

	// ssh $USER@$HOST -i /path/to/key.priv -L $BIND_ADDRESS:$BIND_PORT:$DIAL_ADDRESS:$DIAL_PORT
	// ssh $USER@$HOST -i /path/to/key.priv -R $BIND_ADDRESS:$BIND_PORT:$DIAL_ADDRESS:$DIAL_PORT

	// Example 1
	// Binds Redis from Tailscale Pihole to Localhost of Mini
	if !is_remote {
		var tunn1 tunnel
		tunn1.auth = auth
		tunn1.hostKeys = func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			return nil
		}
		tunn1.mode = '>' // '>' for forward, '<' for reverse
		tunn1.user = "app"
		tunn1.hostAddr = net.JoinHostPort("121.43.230.103", "22")
		tunn1.bindAddr = "localhost:7122"
		tunn1.dialAddr = "localhost:2022"
		tunn1.retryInterval = 30 * time.Second
		//tunn1.keepAlive = *KeepAliveConfig
		tunns = append(tunns, tunn1)
	}

	// Example 2
	// Binds Temporary Python Server from Mini to Localhost of Tailscale Pihole
	if is_remote {
		var tunn2 tunnel
		tunn2.auth = auth
		tunn2.hostKeys = func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			return nil
		}
		tunn2.mode = '<' // '>' for forward, '<' for reverse
		tunn2.user = "app"
		tunn2.hostAddr = net.JoinHostPort("121.43.230.103", "22")
		tunn2.bindAddr = "localhost:2022"
		tunn2.dialAddr = "localhost:2022"
		tunn2.retryInterval = 300 * time.Second
		//tunn1.keepAlive = *KeepAliveConfig
		tunns = append(tunns, tunn2)
	}

	return tunns, closer
}

func main__() {
	var tunns []tunnel
	var closer func() error
	if len(os.Args) > 1 && os.Args[1] == "client" {
		tunns, closer = loadConfig(false)
	} else {
		tunns, closer = loadConfig(true)
	}
	defer closer()

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
	for _, t := range tunns {
		wg.Add(1)
		go t.bindTunnel(ctx, &wg)
	}
	wg.Wait()
}

func SSHTunnel(args []string) {
	tunns, closer := loadConf(args[0])
	defer closer()

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
	for _, t := range tunns {
		wg.Add(1)
		go t.bindTunnel(ctx, &wg)
	}
	wg.Wait()
}
