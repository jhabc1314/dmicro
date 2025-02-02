package dserver

import (
	"bytes"
	"context"
	"fmt"
	"github.com/gogf/gf/v2/container/garray"
	"github.com/gogf/gf/v2/errors/gcode"
	"github.com/gogf/gf/v2/errors/gerror"
	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/os/gcfg"
	"github.com/gogf/gf/v2/os/gcmd"
	"github.com/gogf/gf/v2/os/gfile"
	"github.com/gogf/gf/v2/os/gres"
	"github.com/gogf/gf/v2/os/gspath"
	"github.com/gogf/gf/v2/text/gstr"
	"github.com/gogf/gf/v2/util/gmode"
	"github.com/osgochina/dmicro/logger"
)

const (
	commandEnvKeyForFile = "gf.gcfg.file" // commandEnvKeyForFile 设置配置文件名
	commandEnvKeyForPath = "gf.gcfg.path" // commandEnvKeyForPath 设置配置文件搜索目录
)

var resourceTryFiles = []string{"", "/", "config/", "config", "/config", "/config/"}
var searchPaths *garray.StrArray

func init() {
	searchPaths = garray.NewStrArray(true)
	searchPaths.Append(gfile.Pwd())
	searchPaths.Append(gfile.SelfDir())
}

// 传入配置文件地址，获取gcfg对象
func (that *DServer) getGFConf(confFile string) *gcfg.Config {
	if len(confFile) > 0 {
		//指定了具体的配置文件地址
		if gstr.Contains(confFile, gfile.Separator) {
			confPath := gfile.Abs(confFile)
			if gfile.Exists(confPath) {
				g.Cfg().GetAdapter().(*gcfg.AdapterFile).SetContent(gfile.GetContents(confPath), gfile.Basename(confPath))
				g.Cfg().GetAdapter().(*gcfg.AdapterFile).SetContent(gfile.GetContents(confPath), gcfg.DefaultConfigFileName)
				return gcfg.Instance()
			}
			confPath = fmt.Sprintf("%s%s%s", gfile.MainPkgPath(), gfile.Separator, gfile.Basename(confPath))
			if gfile.Exists(confPath) {
				g.Cfg().GetAdapter().(*gcfg.AdapterFile).SetContent(gfile.GetContents(confPath), gfile.Basename(confPath))
				g.Cfg().GetAdapter().(*gcfg.AdapterFile).SetContent(gfile.GetContents(confPath), gcfg.DefaultConfigFileName)
				return gcfg.Instance()
			}
		} else {
			// 未指定配置文件地址，但是指定了配置文件名，需要去默认的目录搜索
			confPath, _ := getFilePath(confFile)
			if !gfile.Exists(confPath) {
				logger.Errorf(context.TODO(), "配置文件 %s 不存在", confFile)
			} else {
				g.Cfg().GetAdapter().(*gcfg.AdapterFile).SetContent(gfile.GetContents(confPath), gfile.Basename(confPath))
				g.Cfg().GetAdapter().(*gcfg.AdapterFile).SetContent(gfile.GetContents(confPath), gcfg.DefaultConfigFileName)
			}
			return gcfg.Instance()
		}
	}
	//如果环境变量有设置，则使用gf框架自带的配置文件获取流程
	if customFile := gcmd.GetOptWithEnv(commandEnvKeyForFile).String(); customFile != "" {
		return gcfg.Instance()
	}
	// 如果环境变量有设置配置文件搜索路径，则使用gf框架自带的配置文件获取流程
	if customPath := gcmd.GetOptWithEnv(commandEnvKeyForPath).String(); customPath != "" {
		if gfile.Exists(customPath) {
			return gcfg.Instance()
		}
	}
	//如果并未设置配置文件，为了让程序不报错，写入空的配置
	confPath, _ := getFilePath(gcfg.DefaultConfigFileName)
	if len(confPath) <= 0 {
		g.Cfg().GetAdapter().(*gcfg.AdapterFile).SetContent("{}", gcfg.DefaultConfigFileName)
	}
	return gcfg.Instance()
}

// 该方法是copy自gcfg组件，在默认目录搜索配置文件
func getFilePath(file string) (path string, err error) {
	name := file
	if !gres.IsEmpty() {
		for _, v := range resourceTryFiles {
			if file := gres.Get(v + name); file != nil {
				path = file.Name()
				return
			}
		}
		searchPaths.RLockFunc(func(array []string) {
			for _, prefix := range array {
				for _, v := range resourceTryFiles {
					if file := gres.Get(prefix + v + name); file != nil {
						path = file.Name()
						return
					}
				}
			}
		})
	}
	autoCheckAndAddMainPkgPathToSearchPaths()
	// Searching the file system.
	searchPaths.RLockFunc(func(array []string) {
		for _, prefix := range array {
			prefix = gstr.TrimRight(prefix, `\/`)
			if path, _ = gspath.Search(prefix, name); path != "" {
				return
			}
			if path, _ = gspath.Search(prefix+gfile.Separator+"config", name); path != "" {
				return
			}
		}
	})
	// If it cannot find the path of `file`, it formats and returns a detailed error.
	if path == "" {
		var (
			buffer = bytes.NewBuffer(nil)
		)
		if searchPaths.Len() > 0 {
			buffer.WriteString(fmt.Sprintf(`[gcfg] cannot find config file "%s" in resource manager or the following paths:`, name))
			searchPaths.RLockFunc(func(array []string) {
				index := 1
				for _, v := range array {
					v = gstr.TrimRight(v, `\/`)
					buffer.WriteString(fmt.Sprintf("\n%d. %s", index, v))
					index++
					buffer.WriteString(fmt.Sprintf("\n%d. %s", index, v+gfile.Separator+"config"))
					index++
				}
			})
		} else {
			buffer.WriteString(fmt.Sprintf("[gcfg] cannot find config file \"%s\" with no path configured", name))
		}
		err = gerror.NewCode(gcode.CodeOperationFailed, buffer.String())
	}
	return
}

func autoCheckAndAddMainPkgPathToSearchPaths() {
	if gmode.IsDevelop() {
		mainPkgPath := gfile.MainPkgPath()
		if mainPkgPath != "" {
			if !searchPaths.Contains(mainPkgPath) {
				searchPaths.Append(mainPkgPath)
			}
		}
	}
}
