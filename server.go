package tiny

import (
	"TinyRPC/codec"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"
)

const (
	connected        = "200 Connected to Tiny RPC"
	defaultRPCPath   = "/_tinyrpc"
	defaultDebugPath = "/debug/tinyrpc"
)

const MagicNumber = 0x3bef5c

type Option struct {
	MagicNumber    int
	CodecType      codec.Type    // 解编码器
	ConnectTimeout time.Duration // 连接时间
	HandleTimeout  time.Duration // 处理时间
}

var DefaultOption = &Option{
	MagicNumber:    MagicNumber,
	CodecType:      codec.GobType,    // 解编码器
	ConnectTimeout: time.Second * 10, // 连接器
}

var DefaultServer = NewServer()

type Server struct {
	serviceMap sync.Map
}

func NewServer() *Server {
	return &Server{}
}

// Accept 处理网络请求
func (s *Server) Accept(lis net.Listener) {
	for {
		// 获取conn
		conn, err := lis.Accept()
		if err != nil {
			log.Println("rpc server: accept error:", err)
			return
		}
		// 处理conn
		go s.ServeConn(conn)
	}
}

func Accept(lis net.Listener) {
	DefaultServer.Accept(lis)
}

// ServeConn 处理Conn
func (s *Server) ServeConn(conn io.ReadWriteCloser) {
	defer func() {
		_ = conn.Close()
	}()
	// 读取opt
	var opt Option
	if err := json.NewDecoder(conn).Decode(&opt); err != nil {
		log.Println("rpc server: options error: ", err)
		return
	}
	if opt.MagicNumber != MagicNumber {
		log.Printf("rpc server: invalid codec type %s", opt.CodecType)
		return
	}
	// 构建解编码器
	f := codec.NewCodecFuncMap[opt.CodecType]
	if f == nil {
		log.Printf("rpc server: invalid codec type %s", opt.CodecType)
		return
	}
	// 使用解编码器进行处理
	s.serveCodec(f(conn), &opt)
}

var invalidRequest = struct{}{}

func (s *Server) serveCodec(cc codec.Codec, opt *Option) {
	// 使用解编码器来处理conn中的信息
	sending := &sync.Mutex{}
	wg := &sync.WaitGroup{}
	for {
		// 读取请求
		req, err := s.readRequest(cc)
		if err != nil {
			// 已经读完了，跳出循环
			if req == nil {
				break
			}
			req.h.Error = err.Error()
			// 发生错误，返回一个无效得
			s.sendResponse(cc, req.h, invalidRequest, sending)
			continue
		}
		wg.Add(1)
		// 处理请求
		go s.handleRequest(cc, req, sending, wg, opt.HandleTimeout)
	}
	wg.Wait()
	_ = cc.Close()
}

type request struct {
	h      *codec.Header
	argv   reflect.Value
	replyv reflect.Value
	mtype  *methodType
	svc    *service
}

func (s *Server) readRequestHeader(cc codec.Codec) (*codec.Header, error) {
	var h codec.Header
	if err := cc.ReadHeader(&h); err != nil {
		if err != io.EOF && err != io.ErrUnexpectedEOF {
			log.Println("rpc server: read header error:", err)
		}
		return nil, err
	}
	return &h, nil
}

func (s *Server) readRequest(cc codec.Codec) (*request, error) {
	// 读取header
	h, err := s.readRequestHeader(cc)
	if err != nil {
		return nil, err
	}
	// 读取请求
	req := &request{h: h}
	// 找到注册的服务
	req.svc, req.mtype, err = s.findService(h.ServiceMethod)
	// 构建出入参
	req.argv = req.mtype.newArgv()
	req.replyv = req.mtype.newReply()
	argvi := req.argv.Interface()
	// argvi需要是指针
	if req.argv.Type().Kind() != reflect.Ptr {
		argvi = req.argv.Addr().Interface()
	}
	// 从body中读取参数
	if err = cc.ReadBody(argvi); err != nil {
		log.Println("rpc server: read argv err:", err)
	}
	return req, nil
}

func (s *Server) sendResponse(cc codec.Codec, h *codec.Header, body interface{}, sending *sync.Mutex) {
	sending.Lock()
	defer sending.Unlock()
	if err := cc.Write(h, body); err != nil {
		log.Println("rpc server: write response error:", err)
	}
	return
}

func (s *Server) handleRequest(cc codec.Codec, req *request, sending *sync.Mutex, wg *sync.WaitGroup, timeout time.Duration) {
	defer wg.Done()
	called := make(chan struct{}, 1)
	// 这里一定要有缓存，否则在下面的
	sent := make(chan struct{}, 1)
	go func() {
		err := req.svc.call(req.mtype, req.argv, req.replyv)
		called <- struct{}{}
		if err != nil {
			req.h.Error = err.Error()
			s.sendResponse(cc, req.h, invalidRequest, sending)
			sent <- struct{}{}
			return
		}
		s.sendResponse(cc, req.h, req.replyv.Interface(), sending)
		sent <- struct{}{}
	}()
	if timeout == 0 {
		<-called
		<-sent
		return
	}
	select {
	case <-time.After(timeout):
		{
			req.h.Error = fmt.Sprintf("rpc server: request handle timeout: expect within %s", timeout)
			s.sendResponse(cc, req.h, invalidRequest, sending)
		}
	case <-called:
		{
			<-sent
		}
	}
}

func (s *Server) Register(rcvr interface{}) error {
	service := newService(rcvr)
	if _, dup := s.serviceMap.LoadOrStore(service.name, service); dup {
		return errors.New("rpc: service already defined: " + service.name)
	}
	return nil
}

func (s *Server) findService(serviceMethod string) (svc *service, mtype *methodType, err error) {
	dot := strings.LastIndex(serviceMethod, ".")
	if dot < 0 {
		err = errors.New("rpc server: service/method request ill-formed" + serviceMethod)
		return
	}
	// 切分出结构体名和方法名
	serviceName, methodName := serviceMethod[:dot], serviceMethod[dot+1:]
	// 拿出服务
	svci, ok := s.serviceMap.Load(serviceName)
	if !ok {
		err = errors.New("rpc server: can't find service" + serviceName)
		return
	}
	svc = svci.(*service)
	// 取出方法
	mtype = svc.method[methodName]
	if mtype == nil {
		err = errors.New("rpc server: can't find method " + methodName)
	}
	return
}

// ServeHTTP 接受HTTP的CONNECT请求，并回应RPC请求
func (s *Server) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// 仅接受CONNECT请求
	if req.Method != "CONNECT" {
		w.Header().Set("Content-Type", "test/plain; charset=utf-8")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = io.WriteString(w, "405 must CONNECT\n")
		return
	}
	// 劫持http请求，即接管了http请求
	// HTTP 库和 HTTPServer 库将不会管理这个 Socket 连接的生命周期，这个生命周期已经划给 Hijacker 了,然后基于http连接来进行rpc过程
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		log.Print("rpc hijacking ", req.RemoteAddr, ": ", err.Error())
		return
	}
	_, _ = io.WriteString(conn, "HTTP/1.0 "+connected+"\n\n")
	s.ServeConn(conn)
}

func (s *Server) HandleHTTP() {
	// 将基于该路径下的HTTP协议Conn划给rpc
	http.Handle(defaultRPCPath, s)
	// 类似同上
	http.Handle(defaultDebugPath, debugHTTP{s})
	log.Println("rpc server debug path:", defaultDebugPath)
}

func HandleHTTP() {
	DefaultServer.HandleHTTP()
}

func Register(rcvr interface{}) error {
	return DefaultServer.Register(rcvr)
}
