package codec

import (
	"bufio"
	"encoding/gob"
	"io"
	"log"
)

// GobCodec Gob解编码器
type GobCodec struct {
	conn io.ReadWriteCloser
	buf  *bufio.Writer
	dec  *gob.Decoder
	enc  *gob.Encoder
}

func NewGocCodec(conn io.ReadWriteCloser) Codec {
	buf := bufio.NewWriter(conn)
	return &GobCodec{
		conn: conn,
		buf:  buf,
		dec:  gob.NewDecoder(conn),
		enc:  gob.NewEncoder(buf),
	}
}

// Close 关闭关闭信道
func (g *GobCodec) Close() error {
	return g.conn.Close()
}

// ReadHeader 读取header，写到结构体
func (g *GobCodec) ReadHeader(header *Header) error {
	return g.dec.Decode(header)
}

// ReadBody 读取body，写到结构体
func (g *GobCodec) ReadBody(body interface{}) error {
	return g.dec.Decode(body)
}

// Write 写入conn,注意这里使用了buf，会先写入buf，最后再flush
func (g *GobCodec) Write(header *Header, body interface{}) (err error) {
	defer func() {
		_ = g.buf.Flush()
		// 发生了错误，断开conn
		if err != nil {
			_ = g.Close()
		}
	}()
	// 写入header
	if err = g.enc.Encode(header); err != nil {
		log.Println("rpc codec: gob error encoding header:", err)
		return err
	}
	// 写入body
	if err = g.enc.Encode(body); err != nil {
		log.Println("rpc codec: gob error encoding body")
		return err
	}
	return nil
}

var _ Codec = (*GobCodec)(nil)
