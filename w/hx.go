package w

import (
	"flag"
	"github.com/elazarl/goproxy"
	"net/http"
)

func Run2(args []string) {
	cmd := flag.NewFlagSet("http-proxy", flag.ExitOnError)
	svraddr := cmd.String("p", ":8181", "server addr")
	cmd.Parse(args)

	proxy := goproxy.NewProxyHttpServer()
	proxy.Verbose = true

	http.ListenAndServe(*svraddr, proxy)
}
