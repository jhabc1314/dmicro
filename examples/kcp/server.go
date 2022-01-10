package main

import (
	"fmt"
	"github.com/gogf/gf/os/gfile"
	"github.com/osgochina/dmicro/drpc"
	"github.com/osgochina/dmicro/logger"
	"time"
)

func main() {

	// server peer
	srv := drpc.NewEndpoint(drpc.EndpointConfig{
		Network:     "kcp",
		CountTime:   true,
		ListenPort:  9090,
		PrintDetail: true,
	})
	e := srv.SetTLSConfigFromFile(
		fmt.Sprintf("%s/cert.pem", gfile.MainPkgPath()),
		fmt.Sprintf("%s/key.pem", gfile.MainPkgPath()))
	if e != nil {
		logger.Fatalf("%v", e)
	}

	// router
	srv.RouteCall(new(Math))

	// broadcast per 5s
	go func() {
		for {
			time.Sleep(time.Second * 5)
			srv.RangeSession(func(sess drpc.Session) bool {
				sess.Push(
					"/push/status",
					fmt.Sprintf("this is a broadcast, server time: %v", time.Now()),
				)
				return true
			})
		}
	}()

	// listen and serve
	srv.ListenAndServe()
	select {}
}

// Math handler
type Math struct {
	drpc.CallCtx
}

// Add handles addition request
func (m *Math) Add(arg *[]int) (int, *drpc.Status) {
	// test query parameter
	logger.Infof("author: %s", m.PeekMeta("author"))
	// add
	var r int
	for _, a := range *arg {
		r += a
	}
	// response
	return r, nil
}