package registry

import (
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type TinyRegistry struct {
	timeout time.Duration          // 心跳超时
	mu      sync.Mutex             // 互斥锁
	servers map[string]*ServerItem // 服务器
}

type ServerItem struct {
	Addr  string    // 地址
	start time.Time // 启动时间
}

const (
	defaultPath    = "/_tinyrpc/registry"
	defaultTimeout = time.Minute * 5
)

func New(timeout time.Duration) *TinyRegistry {
	return &TinyRegistry{
		servers: make(map[string]*ServerItem),
		timeout: timeout,
	}
}

var DefaultTinyRegistry = New(defaultTimeout)

func (r *TinyRegistry) putServer(addr string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// 获取对应地址的服务器
	s := r.servers[addr]
	if s == nil {
		// 如果没有就添加服务器，置心跳时间
		r.servers[addr] = &ServerItem{Addr: addr, start: time.Now()}
	} else {
		// 更新心跳时间
		s.start = time.Now()
	}
}

func (r *TinyRegistry) aliveServers() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var alive []string
	// 获取所有存活的服务器
	for addr, s := range r.servers {
		if r.timeout == 0 || s.start.Add(r.timeout).After(time.Now()) {
			alive = append(alive, addr)
		} else {
			delete(r.servers, addr)
		}
	}
	sort.Strings(alive)
	return alive
}

func (r *TinyRegistry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	// 获取存活列表
	case "GET":
		{
			// 将存活的服务器列表设置进http服务头，不构造消息体了
			w.Header().Set("X-Tinyrpc-Servers", strings.Join(r.aliveServers(), ","))
		}

	case "POST":
		{
			addr := req.Header.Get("X-Tinyrpc-Server")
			if addr == "" {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			// 添加服务器/更新心跳
			r.putServer(addr)
		}
	default:
		{
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

func (r *TinyRegistry) HandleHTTP(registryPath string) {
	http.Handle(registryPath, r)
	log.Println("rpc registry path:", registryPath)
}

func HandleHTTP() {
	DefaultTinyRegistry.HandleHTTP(defaultPath)
}

func Heartbeat(registry string, addr string, duration time.Duration) {
	// 如果没有间隔
	if duration == 0 {
		duration = defaultTimeout - time.Duration(1)*time.Minute
	}
	var err error
	// 发送心跳
	err = sendHeartbeat(registry, addr)
	go func() {
		// 定时发送心跳
		t := time.NewTicker(duration)
		for err == nil {
			<-t.C
			err = sendHeartbeat(registry, addr)
		}
	}()
}

func sendHeartbeat(registry string, addr string) error {
	log.Println(addr, "send heartbeat to registry", registry)
	httpClient := &http.Client{}
	req, _ := http.NewRequest("POST", registry, nil)
	req.Header.Set("X-Tinyrpc-Server", addr)
	if _, err := httpClient.Do(req); err != nil {
		log.Println("rpc server: heartbeat err:", err)
		return err
	}
	return nil
}
