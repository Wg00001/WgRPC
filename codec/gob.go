package codec

import (
	"bufio"
	"encoding/gob"
	"io"
	"log"
)

type GobCodec struct {
	conn io.ReadWriteCloser //链接实例
	buf  *bufio.Writer      //缓冲区
	dec  *gob.Decoder       //decoder
	enc  *gob.Encoder       //encoder
}

func (g *GobCodec) Close() error {
	return g.conn.Close()
}

func (g *GobCodec) ReadHeader(header *Header) error {
	return g.dec.Decode(header)
}

func (g *GobCodec) ReadBody(body interface{}) error {
	return g.dec.Decode(body)
}

func (g *GobCodec) Write(header *Header, body interface{}) (err error) {
	defer func() {
		g.buf.Flush()
		if err != nil {
			g.Close()
		}
	}()
	if err := g.enc.Encode(header); err != nil {
		log.Println("codec.GobCoder.Write:Encoding header ERR:", err)
		return err
	}
	if err := g.enc.Encode(body); err != nil {
		log.Println("codec.GobCoder.Write:Encoding body ERR:", err)
		return err
	}
	return nil
}

var _ Codec = (*GobCodec)(nil)

func NewGobCodec(conn io.ReadWriteCloser) Codec {
	buf := bufio.NewWriter(conn)
	return &GobCodec{
		conn: conn,
		buf:  buf,
		dec:  gob.NewDecoder(conn),
		enc:  gob.NewEncoder(buf),
	}
}
