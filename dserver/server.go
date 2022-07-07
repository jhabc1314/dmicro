package dserver

import (
	"fmt"
	"github.com/gogf/gf/container/garray"
	"github.com/gogf/gf/container/gmap"
	"github.com/gogf/gf/errors/gerror"
	"github.com/gogf/gf/frame/g"
	"github.com/gogf/gf/os/gcfg"
	"github.com/gogf/gf/os/gcmd"
	"github.com/gogf/gf/os/genv"
	"github.com/gogf/gf/os/gfile"
	"github.com/gogf/gf/os/gtime"
	"github.com/gogf/gf/text/gstr"
	"github.com/gogf/gf/util/gconv"
	"github.com/osgochina/dmicro/dserver/graceful"
	"github.com/osgochina/dmicro/logger"
	"github.com/osgochina/dmicro/supervisor/process"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"
)

const multiProcessMasterEnv = "DServerMultiMasterProcess"

// ProcessModel 进程模型
type ProcessModel int

// ProcessModelSingle 单进程模式
const ProcessModelSingle ProcessModel = 0

// ProcessModelMulti 多进程模型
const ProcessModelMulti ProcessModel = 1

// DServer 服务对象
type DServer struct {
	manager        *process.Manager
	serviceList    *gmap.StrAnyMap  //启动的服务列表
	started        *gtime.Time      //服务启动时间
	shutting       bool             // 服务正在关闭
	beforeStopFunc StopFunc         //服务关闭之前执行该方法
	pidFile        string           //pid文件的路径
	sandboxNames   *garray.StrArray // 启动服务的名称
	cmdParser      *gcmd.Parser     //命令行参数解析信息
	config         *gcfg.Config     ///服务的配置信息
	procModel      ProcessModel     // 进程模式，processModelSingle 单进程模型，processModelMulti 多进程模型
}

// StartFunc 启动回调方法
type StartFunc func(svr *DServer)

// StopFunc 服务关闭回调方法
type StopFunc func(svr *DServer) bool

// newDServer  创建服务
func newDServer() *DServer {
	svr := &DServer{
		serviceList:  gmap.NewStrAnyMap(true),
		sandboxNames: garray.NewStrArray(true),
		manager:      process.NewManager(),
	}
	return svr
}

// SetPidFile 设置pid文件
func (that *DServer) SetPidFile(pidFile string) {
	that.pidFile = pidFile
}

// BeforeStop 设置服务重启方法
func (that *DServer) BeforeStop(f StopFunc) {
	that.beforeStopFunc = f
}

// ProcessModel 设置多进程模式
func (that *DServer) ProcessModel(model ProcessModel) {
	v := gcmd.GetOptVar("model", gconv.String(model))
	that.procModel = ProcessModel(v.Int())
}

// Setup 启动服务，并执行传入的启动方法
func (that *DServer) setup(startFunction StartFunc) {
	//解析命令行
	parser, err := gcmd.Parse(defaultOptions)
	if err != nil {
		logger.Fatal(err)
	}
	//解析参数
	if !that.parserArgs(parser) {
		return
	}
	//解析配置文件
	that.parserConfig(parser)

	//启动时间
	that.started = gtime.Now()

	// 命令行解析
	that.cmdParser = parser

	//判断是否是守护进程运行
	if e := that.demonize(that.config); e != nil {
		logger.Fatalf("error:%v", e)
	}
	//初始化日志配置
	if e := that.initLogSetting(that.config); e != nil {
		logger.Fatalf("error:%v", e)
	}

	//启动自定义方法
	startFunction(that)

	//设置优雅退出时候需要做的工作
	graceful.SetShutdown(15*time.Second, that.firstSweep, that.beforeExiting)

	// 如果开启了多进程模式，并且当前进程在主进程中
	if that.procModel == ProcessModelMulti && that.isMaster() {
		that.serviceList.Iterator(func(_ string, v interface{}) bool {
			dService := v.(*DService)
			if dService.sList.Size() == 0 {
				return true
			}
			// 如果命令行传入了需要启动的服务名称，则需要把改服务名提取出来，作为启动参数
			var sandBoxNames []string
			if that.sandboxNames.Len() > 0 {
				for _, name := range dService.sList.Keys() {
					if that.sandboxNames.ContainsI(name) {
						sandBoxNames = append(sandBoxNames, name)
					}
				}
			} else {
				sandBoxNames = dService.sList.Keys()
			}
			// 如果未匹配服务名称，则说明该service不需要启动
			if len(sandBoxNames) == 0 {
				return true
			}
			var args = []string{"start", gstr.Implode(",", sandBoxNames)}

			if len(that.config.GetString("ENV_NAME")) > 0 {
				args = append(args, fmt.Sprintf("--env=%s", that.config.GetString("ENV_NAME")))
			}
			confFile := that.cmdParser.GetOpt("config")
			fmt.Println(confFile)
			if len(confFile) > 0 {
				args = append(args, fmt.Sprintf("--config=%s", confFile))
			}
			if that.config.GetBool("Debug") {
				args = append(args, "--debug")
			}
			p, e := that.manager.NewProcessByOptions(process.NewProcOptions(
				process.ProcCommand(that.cmdParser.GetArg(0)),
				process.ProcName(dService.Name()),
				process.ProcArgs(args...),
				process.ProcSetEnvironment(multiProcessMasterEnv, "false"),
				process.ProcStdoutLog("/dev/stdout", ""),
				process.ProcRedirectStderr(true),
			))
			if e != nil {
				logger.Warning(e)
			}
			p.Start(false)
			return true
		})
	} else {
		// 业务进程启动sandbox
		that.serviceList.Iterator(func(_ string, v interface{}) bool {
			dService := v.(*DService)
			dService.iterator(func(name string, sandbox ISandbox) bool {
				// 如果命令行传入了要启动的服务名，则需要匹配启动对应的sandbox
				if that.sandboxNames.Len() > 0 && !that.sandboxNames.ContainsI(sandbox.Name()) {
					return true
				}
				go func() {
					e := sandbox.Setup()
					if e != nil {
						logger.Warningf("Sandbox Setup Return: %v", e)
					}
				}()
				return true
			})
			return true
		})
	}
	// 多进程模式下，只有主进程需要写入pid文件
	if that.procModel == ProcessModelMulti && that.isMaster() {
		//写入pid文件
		that.putPidFile()
	}
	// 单进程模式下写入pid文件
	if that.procModel == ProcessModelSingle {
		//写入pid文件
		that.putPidFile()
	}

	//等待服务结束
	logger.Printf("%d: 服务已经初始化完成, %d 个协程被创建.", os.Getpid(), runtime.NumGoroutine())

	//监听重启信号
	graceful.GraceSignal(int(that.procModel))
}

// AddSandBox 添加sandbox到服务中
// services 是可选，如果不传入则表示使用默认服务
func (that *DServer) AddSandBox(s ISandbox, services ...*DService) error {
	var service *DService
	if len(services) > 0 {
		service = services[0]
	} else {
		s2, found := that.serviceList.Search("default")
		if !found {
			service = that.NewService("default")
		} else {
			service = s2.(*DService)
		}
	}
	err := service.addSandBox(s)
	if err != nil {
		return err
	}
	// 查看service服务是否存在于列表中，如果不存在则添加
	_, found := that.serviceList.Search(service.Name())
	if !found {
		that.serviceList.Set(service.Name(), service)
	}
	return nil
}

// Config 获取配置信息
func (that *DServer) Config() *gcfg.Config {
	return that.config
}

// CmdParser 返回命令行解析
func (that *DServer) CmdParser() *gcmd.Parser {
	return that.cmdParser
}

// StartTime 返回启动时间
func (that *DServer) StartTime() *gtime.Time {
	return that.started
}

//通过参数设置日志级别
// 日志级别通过环境默认分三个类型，开发环境，测试环境，生产环境
// 开发环境: 日志级别为 DEVELOP,标准输出打开
// 测试环境：日志级别为 INFO,除了debug日志，都会被打印，标准输出关闭
// 生产环境: 日志级别为 PRODUCT，会打印 WARN,ERRO,CRIT三个级别的日志，标准输出为关闭
// Debug开关会无视以上设置，强制把日志级别设置为ALL，并且打开标准输出。
func (that *DServer) initLogSetting(config *gcfg.Config) error {
	loggerCfg := config.GetJson("logger")
	env := config.GetString("ENV_NAME")
	level := loggerCfg.GetString("Level")
	logger.SetDebug(false)
	logger.SetStdoutPrint(false)
	//如果配置文件中的日志配置不存在，则判断环境变量，通过不同的环境变量，给与不同的日志级别
	if len(level) <= 0 {
		if env == "dev" || env == "develop" {
			level = "DEVELOP"
		} else if env == "test" {
			level = "INFO"
		} else {
			level = "PRODUCT"
		}
	}

	setConfig := g.Map{"level": level}

	if env == "dev" || env == "develop" {
		setConfig["stdout"] = true
		logger.SetDebug(true)
	}
	logPath := loggerCfg.GetString("Path")
	if len(logPath) > 0 {
		setConfig["path"] = logPath
	} else {
		logger.SetDebug(true)
	}

	// 如果开启debug模式，则无视其他设置
	if config.GetBool("Debug", false) {
		setConfig["level"] = "ALL"
		setConfig["stdout"] = true
		logger.SetDebug(true)
	}
	return logger.SetConfigWithMap(setConfig)
}

//守护进程
func (that *DServer) demonize(config *gcfg.Config) error {

	//判断是否需要后台运行
	daemon := config.GetBool("Daemon", false)
	if !daemon {
		return nil
	}

	if syscall.Getppid() == 1 {
		return nil
	}
	// 将命令行参数中执行文件路径转换成可用路径
	filePath := gfile.SelfPath()
	logger.Infof("Starting %s: ", filePath)
	arg0, e := exec.LookPath(filePath)
	if e != nil {
		return e
	}
	argv := make([]string, 0, len(os.Args))
	for _, arg := range os.Args {
		if arg == "--daemon" || arg == "-d" {
			continue
		}
		argv = append(argv, arg)
	}
	cmd := exec.Command(arg0, argv[1:]...)
	cmd.Env = os.Environ()
	// 将其他命令传入生成出的进程
	cmd.Stdin = os.Stdin // 给新进程设置文件描述符，可以重定向到文件中
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Start() // 开始执行新进程，不等待新进程退出
	if err != nil {
		return err
	}
	os.Exit(0)
	return nil
}

//写入pid文件
func (that *DServer) putPidFile() {
	pid := os.Getpid()

	////在GraceMasterWorker模型下，只有子进程才会执行到该逻辑，所以需要把pid设置为父进程的id
	//if graceful.GetModel() == graceful.GraceMasterWorker && graceful.IsChild() {
	//	pid = os.Getppid()
	//}

	f, e := os.OpenFile(that.pidFile, os.O_WRONLY|os.O_CREATE, os.FileMode(0600))
	if e != nil {
		logger.Fatalf("os.OpenFile: %v", e)
	}
	defer func() {
		_ = f.Close()
	}()
	if e := os.Truncate(that.pidFile, 0); e != nil {
		logger.Fatalf("os.Truncate: %v.", e)
	}
	if _, e := fmt.Fprintf(f, "%d", pid); e != nil {
		logger.Fatalf("Unable to write pid %d to file: %s.", os.Getpid(), e)
	}
}

// Shutdown 主动结束进程
func (that *DServer) Shutdown(timeout ...time.Duration) {
	graceful.Shutdown(timeout...)
}

func (that *DServer) firstSweep() error {
	if that.shutting {
		return nil
	}
	that.shutting = true

	if len(that.pidFile) > 0 && gfile.Exists(that.pidFile) {
		if e := gfile.Remove(that.pidFile); e != nil {
			logger.Errorf("os.Remove: %v", e)
		}
		logger.Printf("删除pid文件[%s]成功", that.pidFile)
	}

	//结束服务前调用该方法,如果结束回调方法返回false，则中断结束
	if that.beforeStopFunc != nil && !that.beforeStopFunc(that) {
		err := gerror.New("执行完服务停止前的回调方法，该方法强制中断了服务结束流程！")
		logger.Warning(err)
		that.shutting = false
		return err
	}

	return nil
}

//进行结束收尾工作
func (that *DServer) beforeExiting() error {
	//结束各组件
	that.serviceList.Iterator(func(_ string, v interface{}) bool {
		dService := v.(*DService)
		dService.iterator(func(name string, sandbox ISandbox) bool {
			if e := sandbox.Shutdown(); e != nil {
				logger.Errorf("服务 %s .结束出错，error: %v", sandbox.Name(), e)
			} else {
				logger.Printf("%s 服务 已结束.", sandbox.Name())
			}
			return true
		})
		return true
	})
	return nil
}

// IsMaster 判断当前进程是否是主进程
func (that *DServer) isMaster() bool {
	return genv.GetVar(multiProcessMasterEnv, true).Bool()
}

// NewService 创建新的服务
// 注意: 如果是多进程模式，则每个service表示一个进程
func (that *DServer) NewService(name string) *DService {
	return newDService(name, that)
}
