package tinyRPC

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/CenGinHong/TinyRPC/codec"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Call struct {
	Seq           uint64      // 一个唯一标识的序号
	ServiceMethod string      // 形如"Foo.sum"
	Args          interface{} // 参数
	Reply         interface{} // 结果
	Error         error       // 错误
	Done          chan *Call  // 请求完成的通道
}

func (c *Call) done() {
	c.Done <- c
}

type Client struct {
	cc       codec.Codec // 编解码器
	opt      *Option
	sending  sync.Mutex       // 发送请求的互斥锁
	header   codec.Header     // 请求发送是互斥的，只需维护一个header
	mu       sync.Mutex       // pending,closing,shutdown的安全
	seq      uint64           // 全局编号
	pending  map[uint64]*Call // 正在发送的请求
	closing  bool             // 用户主动关闭
	shutdown bool             // 因错误发生导致连接关闭
}

var _ io.Closer = (*Client)(nil)

var ErrShutDown = errors.New("connection is shut down")

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return ErrShutDown
	}
	c.closing = true
	return c.cc.Close()
}

func (c *Client) IsAvailable() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.shutdown && !c.closing
}

func (c *Client) registerCall(call *Call) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing || c.shutdown {
		return 0, ErrShutDown
	}
	// 进行编号
	call.Seq = c.seq
	// 设置为pending
	c.pending[call.Seq] = call
	c.seq++
	return call.Seq, nil
}

func (c *Client) removeCall(seq uint64) *Call {
	c.mu.Lock()
	defer c.mu.Unlock()
	call := c.pending[seq]
	delete(c.pending, seq)
	return call
}

func (c *Client) terminateCalls(err error) {
	c.sending.Lock()
	defer c.sending.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.shutdown = true
	for _, call := range c.pending {
		call.Error = err
		call.done()
	}
}

func (c *Client) receive() {
	var err error
	for err == nil {
		// 读取header
		var h codec.Header
		if err = c.cc.ReadHeader(&h); err != nil {
			break
		}
		// 以接收到回复，从pending中移除
		call := c.removeCall(h.Seq)
		switch {
		// 该call因为某些原因被取消了，无视该返回结果
		case call == nil:
			{
				err = c.cc.ReadBody(nil)
			}
			// 该call在server端出现错误
		case h.Error != "":
			{
				call.Error = fmt.Errorf(h.Error)
				err = c.cc.ReadBody(nil)
				call.done()
			}
		default:
			{
				// 读取结果
				err = c.cc.ReadBody(call.Reply)
				if err != nil {
					call.Error = errors.New("reading body" + err.Error())
				}
				call.done()
			}
		}
	}
	// 停止所有call
	c.terminateCalls(err)
}

func NewClient(conn net.Conn, opt *Option) (*Client, error) {
	// 获取解编码器
	f := codec.NewCodecFuncMap[opt.CodecType]
	if f == nil {
		err := fmt.Errorf("invalid codec type %s", opt.CodecType)
		log.Println("rpc client: codec error:", err)
		return nil, err
	}
	// 使用json将opt写入conn
	if err := json.NewEncoder(conn).Encode(opt); err != nil {
		log.Println("rpc client: options error:", err)
		_ = conn.Close()
		return nil, err
	}
	return newClientCodec(f(conn), opt), nil
}

func newClientCodec(cc codec.Codec, opt *Option) *Client {
	client := &Client{
		seq:     1,
		cc:      cc,
		opt:     opt,
		pending: make(map[uint64]*Call),
	}
	go client.receive()
	return client
}

func parseOptions(opts ...*Option) (*Option, error) {
	if len(opts) == 0 || opts[0] == nil {
		return DefaultOption, nil
	}
	if len(opts) != 1 {
		return nil, errors.New("number of options is more than 1")
	}
	opt := opts[0]
	opt.MagicNumber = DefaultOption.MagicNumber
	if opt.CodecType == "" {
		opt.CodecType = DefaultOption.CodecType
	}
	return opt, nil
}

func Dial(network string, address string, opts ...*Option) (client *Client, err error) {
	return dialTimeout(NewClient, network, address, opts...)
}

func (c *Client) send(call *Call) {
	// 先登记call
	seq, err := c.registerCall(call)
	if err != nil {
		call.Error = err
		call.done()
		return
	}
	c.sending.Lock()
	defer c.sending.Unlock()
	// header是公用的，必须使用sending保护header
	c.header.ServiceMethod = call.ServiceMethod
	c.header.Seq = seq
	c.header.Error = ""
	if err = c.cc.Write(&c.header, call.Args); err != nil {
		call := c.removeCall(seq)
		if call != nil {
			call.Error = err
			call.done()
		}
	}
}

func (c *Client) Go(serviceMethod string, args interface{}, reply interface{}, done chan *Call) *Call {
	// 构建call
	call := &Call{
		ServiceMethod: serviceMethod,
		Args:          args,
		Reply:         reply,
		Done:          done,
	}
	c.send(call)
	return call
}

func (c *Client) Call(ctx context.Context, serviceMethod string, args interface{}, reply interface{}) error {
	// ctx中可能存在超时控制
	call := c.Go(serviceMethod, args, reply, make(chan *Call, 1))
	select {
	case <-ctx.Done():
		{
			c.removeCall(call.Seq)
			return errors.New("rpc client: call failed: " + ctx.Err().Error())
		}
	case call := <-call.Done:
		{
			return call.Error
		}
	}
}

type clientResult struct {
	client *Client
	err    error
}

type newClientFunc func(conn net.Conn, opt *Option) (client *Client, err error)

func dialTimeout(f newClientFunc, network string, address string, opts ...*Option) (client *Client, err error) {
	// 获取opt
	opt, err := parseOptions(opts...)
	if err != nil {
		return nil, err
	}
	// 原生拨号获取conn
	conn, err := net.DialTimeout(network, address, opt.ConnectTimeout)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = conn.Close()
		}
	}()
	ch := make(chan clientResult, 1)
	go func() {
		client, err := f(conn, opt)
		if err != nil {
			return
		}
		ch <- clientResult{client: client, err: err}
	}()
	// 如果没有设超时，阻塞等待连接成功
	if opt.ConnectTimeout == 0 {
		result := <-ch
		return result.client, result.err
	}
	// 采用超时逻辑
	select {
	case <-time.After(opt.ConnectTimeout):
		{
			return nil, fmt.Errorf("rpc client: connect timeout: expect within %s", opt.ConnectTimeout)
		}
	case result := <-ch:
		{
			return result.client, result.err
		}
	}
}

// NewHTTPClient 该初始化器会与服务端进行通信发生协议转换
func NewHTTPClient(conn net.Conn, opt *Option) (*Client, error) {
	// 写入conn
	_, _ = io.WriteString(conn, fmt.Sprintf("CONNECT %s HTTP/1.0\n\n", defaultRPCPath))
	// 读取请求
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "CONNECT"})
	// 进行协议转换后再新建客户端
	if err == nil && resp.Status == connected {
		return NewClient(conn, opt)
	}
	if err == nil {
		err = errors.New("unexpected HTTP response " + resp.Status)
	}
	return nil, err
}

func DialHTTP(network string, address string, opts ...*Option) (*Client, error) {
	return dialTimeout(NewHTTPClient, network, address, opts...)
}

func XDial(rpcAddr string, opts ...*Option) (*Client, error) {
	parts := strings.Split(rpcAddr, "@")
	if len(parts) != 2 {
		return nil, fmt.Errorf("rpc client err: wrong format '%s', expect protocol@addr", rpcAddr)
	}
	// 获取
	protocol, addr := parts[0], parts[1]
	switch protocol {
	case "http":
		{
			return DialHTTP("tcp", addr, opts...)
		}
	default:
		{
			return Dial(protocol, addr, opts...)
		}
	}
}
