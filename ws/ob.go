package ws

import (
	pt "git.torproject.org/pluggable-transports/goptlib.git"
	"github.com/lulugyf/fkme/logger"
	ob1 "gitlab.com/yawning/obfs4.git/transports/obfs4"
	"net"
)

func OServer1() {
	logger.Info("start obfs4 Server")
	stateDir := "/tmp/ob1"
	// f, err := t.ServerFactory(stateDir, &bindaddr.Options)
	args := pt.Args{}
	tp := &ob1.Transport{}
	ss, err := tp.ServerFactory(stateDir, &args)
	if err != nil {
		logger.Error("create server factory failed: %v", err)
		return
	}
	logger.Info("factory ---: %v", ss)
	lnr, err := net.Listen("tcp", ":4111")
	if err != nil {
		logger.Error("failed listen on 4111: %v", err)
		return
	}
	buf := make([]byte, 1024)
	for {
		conn, err := lnr.Accept()
		if err != nil {
			logger.Error("accept failed: %v", err)
			break
		}
		conn1, err := ss.WrapConn(conn)
		if err != nil {
			logger.Error("WrapConn failed: %v", err)
			conn.Close()
			continue
		}
		conn1.Write([]byte("hello from server!"))
		n, err := conn1.Read(buf)
		if err == nil {
			logger.Info("from client: %s", string(buf[:n]))
		}
		conn1.Close()
		//conn.CLose
	}
	lnr.Close()

}

func OClient1() {
	logger.Info("start obfs4 Client")

	tp := &ob1.Transport{}
	stateDir := "/tmp/ob1"
	_, err := tp.ClientFactory(stateDir)
	if err != nil {
		logger.Error("create client factory failed: %v", err)
		return
	}
	_, err = net.Dial("tcp", "127.0.0.1:4111")
	if err != nil {
		logger.Error("connect failed: %v", err)
		return
	}

	//conn1, err := ss.WrapConn(conn)
	//if err != nil {
	//	logger.Error("WrapConn failed: %v", err)
	//	conn.Close()
	//	return
	//}
	//conn1.Write([]byte("hello from server!"))
	//n, err := conn1.Read(buf)
	//if err == nil {
	//	logger.Info("from client: %s", string(buf[:n]))
	//}
	//conn1.Close()
}
