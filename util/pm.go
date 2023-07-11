package util

/*
set GOARCH=386
set GOOS=windows
set GO111MODULE=off
*/
//---
import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"
)

type Port struct {
	Remote_addr string `json:"remote_addr"`
	Listen_port int32  `json:"listen_port"`
	last_active time.Time
	running     bool
	listener    net.Listener
	repstr      string // 用于替换http请求中的Host, 要求前后的长度一致, 格式:  172.21.4.101:9009,127.00.0.001:9009
}

func (p *Port) Str() string {
	return fmt.Sprintf("%d => %s  %s", p.Listen_port, p.Remote_addr, p.last_active)
}

type Serv struct {
	ports    map[int]*Port
	showdata *bool
	mng_port *int
	cfg_file string
}

func NewServ() *Serv {
	v := &Serv{
		ports:    make(map[int]*Port),
		showdata: flag.Bool("showdata", false, "if show transfer data"),
		mng_port: flag.Int("port", 8181, "manage port"),
		cfg_file: "pm.list",
	}
	plist := flag.String("list", "pm.list", "ports list file")
	flag.Parse()
	v.cfg_file = *plist
	return v
}

const (
	RECV_BUF_LEN = 1024
)

func Trans(conn1, conn2 net.Conn, tag string, repstr string) {
	buf := make([]byte, RECV_BUF_LEN)
	//defer conn1.Close()
	defer conn2.Close()

	ss, sr := []byte{}, []byte{} //[]byte("172.21.4.101:9009")
	//	sr := "" // []byte("127.00.0.001:9009")
	if repstr != "" {
		v := strings.Split(repstr, ",")
		if len(v) > 1 {
			ss = []byte(v[0])
			sr = []byte(v[1])
		}
	}
	for {
		n, err := conn1.Read(buf)
		if err != nil {
			println("Error reading:", err.Error(), "::read from", conn1.RemoteAddr().String())
			break
		}
		if len(ss) > 0 {
			x := 0
			for i := 0; i < 3; i++ {
				r := bytes.Index(buf[x:], ss)
				r = r + x
				if r >= x && r < n {
					fmt.Printf("--r: %d\n", r)
					copy(buf[r:], sr)
					x = r + len(ss)
				} else {
					break
				}
			}
		}
		if tag != "" {
			fmt.Printf("%s [%s]\n", tag, string(buf[:n]))
		}

		//send to target
		_, err = conn2.Write(buf[0:n])
		if err != nil {
			println("Error send reply:", err.Error())
			break
		}
	}
}

func (v *Serv) Run() {
	for {
		time.Sleep(time.Second * 20)
	}
}

func (v *Serv) Listen(port *Port, showdata bool) {
	log.Printf("Listen on %d -> %s\n", port.Listen_port, port.Remote_addr)
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port.Listen_port))
	if err != nil {
		log.Println("error listening:", err.Error())
		os.Exit(1)
	}
	tag_up, tag_down := "<<", ""
	if !showdata {
		tag_up, tag_down = "", ""
	}
	fmt.Printf("tag_up [%s] tag_down[%s]\n", tag_up, tag_down)
	port.listener = listener

	for port.running {
		conn, err := listener.Accept()
		if err != nil {
			log.Println("Error accept:", err.Error())
			break
		}
		go func(port *Port, conn net.Conn) {
			var conn1 net.Conn
			var err1 error
			for i := 0; i < 10; i++ {
				conn1, err1 = net.Dial("tcp", port.Remote_addr)
				if err1 != nil {
					fmt.Printf("Connect to target [%s] failed [%v], retrying %d\n", port.Remote_addr, err1.Error(), i)
					time.Sleep(5 * time.Second)
				} else {
					break
				}
			}
			if err1 != nil {
				fmt.Printf("  -Connect to target [%s] failed [%v], close\n", port.Remote_addr, err1.Error())
				conn.Close()
				return
			}
			fmt.Printf("  =New Connection from %s on port %d to %s\n",
				conn.RemoteAddr().String(), port.Listen_port, port.Remote_addr)
			go Trans(conn, conn1, tag_up, "") //port.repstr)
			go Trans(conn1, conn, tag_down, "")
		}(port, conn)
	}
}

// 实际绑定端口映射
func (serv *Serv) bindOp(port int, remote_addr, repstr string) int {
	log.Printf("---bind:  %d\n", port)
	p, prs := serv.ports[port]
	if !prs {
		p = &Port{
			Remote_addr: remote_addr,
			Listen_port: int32(port), running: true,
			last_active: time.Now(),
			listener:    nil,
			repstr:      repstr}
		serv.ports[port] = p
		fmt.Printf("bind %d to %s\n", port, remote_addr)
		go serv.Listen(p, *(serv.showdata))
		log.Printf("--new Listen: %s", p.Str())
		return 0 // add a portmapper
	} else {
		p.last_active = time.Now()
		if p.Remote_addr != remote_addr {
			p.Remote_addr = remote_addr
			return 2 // remote addr replaced
		}
		return 1 // same remote_addr
	}
}

type arrayFlags []string

func (i *arrayFlags) String() string {
	return "my string representation"
}

func (i *arrayFlags) Set(value string) error {
	*i = append(*i, value)
	return nil
}

func PM(args []string) {
	cmd := flag.NewFlagSet("socks5", flag.ExitOnError)
	//log.Printf("------- [%s] [%s] --\n", os.Args[1], os.Args[2])
	var arr arrayFlags
	cmd.Var(&arr, "b", "bind port item: listen;dst_host:dst_port")
	cmd.Parse(args)

	if len(arr) < 1 {
		log.Fatalf("add bind port!!")
		return
	}
	serv := NewServ()
	for i, b := range arr {
		p := 0
		d := ""
		fmt.Sscanf(b, "%d;%s", &p, &d)
		fmt.Printf("%d == %s  [%d] [%s]\n", i, b, p, d)
		serv.bindOp(p, d, "")
	}

	//port := 0
	//fmt.Sscanf( os.Args[1], "%d", &port)
	//raddr := os.Args[2]
	//log.Printf("-------1 [%d] [%s]\n", port, raddr)
	//serv.bindOp(port, raddr, "")
	serv.Run()
}
