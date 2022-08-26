package main

import (
	"github.com/osgochina/dmicro/dserver"
)

func main() {
	dserver.Authors = "osgochina@gmail.com"
	dserver.SetName("DMicro_simple")
	dserver.CloseCtl()
	dserver.Setup(func(svr *dserver.DServer) {
	})
}
