package wgRPC

import (
	"go/ast"
	"log"
	"reflect"
	"sync/atomic"
)

//将结构体的方法映射为服务
/**
对 net/rpc 而言，一个函数需要能够被远程调用，需要满足如下五个条件:
1.方法所属类型是导出的
2.方式是导出的
3.两个入参均为导出或内置类型
4.第二个入参必须是一个指针
5.返回值为 error 类型
即：func (t *T) MethodName(argType T1,replyType *T2)error  {}
借助反射来使映射过程自动化，获取某个结构体的所有方法，获取该方法的所有参数类型和返回值
*/

// 方法类型
type methodType struct {
	method    reflect.Method //方法本身
	ArgType   reflect.Type   //方法的第一个参数的类型
	ReplyType reflect.Type   //第二个参数的类型
	numCalls  uint64         //用于统计方法调用次数
}

// NumCalls 原子获取调用次数
func (m *methodType) NumCalls() uint64 {
	return atomic.LoadUint64(&m.numCalls)
}

// 创建Argv的实例
func (m *methodType) newArgv() reflect.Value {
	var argv reflect.Value
	if m.ArgType.Kind() == reflect.Ptr {
		argv = reflect.New(m.ArgType.Elem())
	} else {
		argv = reflect.New(m.ArgType).Elem()
	}
	return argv
}

// 创建Replyv的实例
func (m *methodType) newReplyv() reflect.Value {
	replyv := reflect.New(m.ReplyType.Elem())
	switch m.ReplyType.Elem().Kind() {
	case reflect.Map:
		replyv.Elem().Set(reflect.MakeMap(m.ReplyType.Elem()))
	case reflect.Slice:
		replyv.Elem().Set(reflect.MakeSlice(m.ReplyType.Elem(), 0, 0))
	}
	return replyv
}

type service struct {
	name   string                 //映射的结构体的名称
	typ    reflect.Type           //结构体的类型
	rcvr   reflect.Value          //结构体的实例本身
	method map[string]*methodType //存储映射的结构体的所有符合条件的方法。
}

// 根据要映射的结构体来创建service
func newService(rcvr interface{}) *service {
	s := new(service)
	s.rcvr = reflect.ValueOf(rcvr)
	s.name = reflect.Indirect(s.rcvr).Type().Name()
	s.typ = reflect.TypeOf(rcvr)
	//判断结构体是否外部可见
	if !ast.IsExported(s.name) {
		log.Fatalf("service.newService: %s is not a valid service name", s.name)
	}
	s.registerMethods()
	return s
}

// 过滤出符合条件的方法，放入service.method里
func (service *service) registerMethods() {
	service.method = make(map[string]*methodType)
	//遍历该结构体的所有方法
	for i := 0; i < service.typ.NumMethod(); i++ {
		method := service.typ.Method(i)
		mType := method.Type
		//判断入参是否等于3
		if mType.NumIn() != 3 || mType.NumOut() != 1 {
			continue
		}
		if mType.Out(0) != reflect.TypeOf((*error)(nil)).Elem() {
			continue
		}
		//0是它自身（即this），1是第一个参数，2是第二个参数
		argType, replyType := mType.In(1), mType.In(2)
		if !isExportedOrBuiltinType(argType) || !isExportedOrBuiltinType(replyType) {
			continue
		}
		//放入service.method中
		service.method[method.Name] = &methodType{
			method:    method,
			ArgType:   argType,
			ReplyType: replyType,
		}
		log.Printf("rpc server: register %s.%s\n", service.name, method.Name)
	}
}

func isExportedOrBuiltinType(t reflect.Type) bool {
	return ast.IsExported(t.Name()) || t.PkgPath() == ""
}

// 通过反射值调用方法
func (service *service) call(m *methodType, argv, replyv reflect.Value) error {
	atomic.AddUint64(&m.numCalls, 1)
	f := m.method.Func
	returnValues := f.Call([]reflect.Value{service.rcvr, argv, replyv})
	if errInter := returnValues[0].Interface(); errInter != nil {
		return errInter.(error)
	}
	return nil
}
