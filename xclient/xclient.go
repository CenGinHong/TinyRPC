package xclient

import (
	tiny "TinyRPC"
	"context"
	"io"
	"reflect"
	"sync"
)

type XClient struct {
	d       Discovery
	mode    SelectMode
	opt     *tiny.Option
	mu      sync.Mutex
	clients map[string]*tiny.Client
}

func NewXClient(d Discovery, mode SelectMode, opt *tiny.Option) *XClient {
	return &XClient{d: d, mode: mode, opt: opt, clients: make(map[string]*tiny.Client)}
}

func (x *XClient) Close() error {
	x.mu.Lock()
	defer x.mu.Unlock()
	for key, client := range x.clients {
		_ = client.Close()
		delete(x.clients, key)
	}
	return nil
}

// dial 向rpcAddr发起请求，获取client
func (x *XClient) dial(rpcAddr string) (*tiny.Client, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	// 取出对应的client
	client, ok := x.clients[rpcAddr]
	if ok && !client.IsAvailable() {
		_ = client.Close()
		delete(x.clients, rpcAddr)
		client = nil
	}
	if client == nil {
		var err error
		// 建立连接，获取客户端，存入客户端
		client, err = tiny.XDial(rpcAddr, x.opt)
		if err != nil {
			return nil, err
		}
		x.clients[rpcAddr] = client
	}
	return client, nil
}

var _ io.Closer = (*XClient)(nil)

func (x *XClient) call(rpcAddr string, ctx context.Context, serviceMethod string, args interface{}, reply interface{}) error {
	client, err := x.dial(rpcAddr)
	if err != nil {
		return err
	}
	return client.Call(ctx, serviceMethod, args, reply)
}

func (x *XClient) Call(ctx context.Context, serviceMethod string, args interface{}, reply interface{}) error {
	// 获取server地址
	rpcAddr, err := x.d.Get(x.mode)
	if err != nil {
		return err
	}
	// 根据地址获取client并获取请求
	return x.call(rpcAddr, ctx, serviceMethod, args, reply)
}

// Broadcast 向所有的server发送请求，只去最小到达的一个
func (x *XClient) Broadcast(ctx context.Context, serviceMethod string, args interface{}, reply interface{}) error {
	// 获取全部server
	servers, err := x.d.GetAll()
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	// e是所有goroutine共享的error
	var e error
	replyDone := reply == nil
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for _, rpcAddr := range servers {
		wg.Add(1)
		go func(rpcAddr string) {
			defer wg.Done()
			var clonedReply interface{}
			if reply != nil {
				clonedReply = reflect.New(reflect.ValueOf(reply).Elem().Type()).Interface()
			}
			err := x.call(rpcAddr, ctx, serviceMethod, args, clonedReply)
			// 锁
			mu.Lock()
			defer mu.Unlock()
			if err != nil && e == nil {
				e = err
				// 如果任意一个call失败了，取消所有未完成的call
				cancel()
			}
			// clonedReply吃call得到值，reply是外层函数希望得到的唯一值
			if err == nil && !replyDone {
				reflect.ValueOf(reply).Elem().Set(reflect.ValueOf(clonedReply).Elem())
				replyDone = true
			}
		}(rpcAddr)
	}
	wg.Wait()
	return e
}
