package codec

import "io"

type Header struct {
	ServiceMethod string //服务名和方法名，与GO中的结构体和方法相映射
	Seq           uint64 //请求序号
	Error         string //错误信息
}

// 对消息体进行编解码的接口
type Codec interface {
	io.Closer
	ReadHeader(*Header) error
	ReadBody(interface{}) error
	Write(*Header, interface{}) error
}

// 返回构造函数的工厂：通过Codec的Type得到构造函数，从而创建Codec实例
type NewCodecFunc func(closer io.ReadWriteCloser) Codec
type Type string

// 此处定义了两种Codec，现在只实现Gob一种
const (
	GobType  Type = "application/gob"
	JsonType Type = "application/json"
)

var NewCodecFuncMap map[Type]NewCodecFunc

func init() {
	NewCodecFuncMap = make(map[Type]NewCodecFunc)
	NewCodecFuncMap[GobType] = NewGobCodec
}
