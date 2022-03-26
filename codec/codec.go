package codec

import (
	"io"
)

// Header rpc请求头部
type Header struct {
	ServiceMethod string // 服务方法，形如“service.method"
	Seq           uint64 // 序列号
	Error         string // 错误信息
}

// Codec 解编码器
type Codec interface {
	io.Closer
	ReadHeader(*Header) error
	ReadBody(interface{}) error
	Write(*Header, interface{}) error
}

type NewCodecFunc func(writer io.ReadWriteCloser) Codec

type Type string

const (
	GobType  Type = "application/gob"
	JsonType Type = "application/json"
)

var NewCodecFuncMap map[Type]NewCodecFunc

func init() {
	NewCodecFuncMap = make(map[Type]NewCodecFunc)
	NewCodecFuncMap[GobType] = NewGocCodec
}
