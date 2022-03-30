package tiny

import (
	"go/ast"
	"log"
	"reflect"
	"sync/atomic"
)

type methodType struct {
	method    reflect.Method
	ArgType   reflect.Type
	ReplyType reflect.Type
	numCalls  uint64
}

func (t *methodType) NumCalls() uint64 {
	return atomic.LoadUint64(&t.numCalls)
}

func (t *methodType) newArgv() reflect.Value {
	var argv reflect.Value
	// 如果是指针
	if t.ArgType.Kind() == reflect.Ptr {
		// 如果是指针，则仍要反射出指向该类型的指针
		argv = reflect.New(t.ArgType.Elem())
	} else {
		// 返回给类型的结构体
		argv = reflect.New(t.ArgType).Elem()
	}
	return argv
}

func (t *methodType) newReply() reflect.Value {
	// reply一定是指针
	replyv := reflect.New(t.ReplyType.Elem())
	switch t.ReplyType.Elem().Kind() {
	// 特别处理map和slice类型
	case reflect.Map:
		{
			replyv.Elem().Set(reflect.MakeMap(t.ReplyType.Elem()))
		}
	case reflect.Slice:
		{
			replyv.Elem().Set(reflect.MakeSlice(t.ReplyType.Elem(), 0, 0))
		}
	}
	return replyv
}

type service struct {
	name   string                 // 结构体的名称
	typ    reflect.Type           // 结构体的类型
	rcvr   reflect.Value          // 结构体实力本身，调用时需要作为第0个参数
	method map[string]*methodType // 结构体中符合条件的方法
}

func newService(rcvr interface{}) *service {
	s := new(service)
	// 结构体的实例本身
	s.rcvr = reflect.ValueOf(rcvr)
	// 结构体的名字
	s.name = reflect.Indirect(s.rcvr).Type().Name()
	// 类型
	s.typ = reflect.TypeOf(rcvr)
	// 该结构体是否包导出
	if !ast.IsExported(s.name) {
		log.Fatalf("rpc server: %s is not a valid service name", s.name)
	}
	s.registerMethods()
	return s
}

func (s *service) registerMethods() {
	s.method = make(map[string]*methodType)
	for i := 0; i < s.typ.NumMethod(); i++ {
		method := s.typ.Method(i)
		mType := method.Type
		// 在rpc注册的方法稚应有三个参数，一个回参
		if mType.NumIn() != 3 || mType.NumOut() != 1 {
			continue
		}
		if mType.Out(0) != reflect.TypeOf((*error)(nil)).Elem() {
			continue
		}
		argType, replyType := mType.In(1), mType.In(2)
		if !isExportedOrBuiltinType(argType) || !isExportedOrBuiltinType(replyType) {
			continue
		}
		s.method[method.Name] = &methodType{
			method:    method,
			ArgType:   argType,
			ReplyType: replyType,
		}
		log.Printf("rpc server: register %s.%s\n", s.name, method.Name)
	}
}

func isExportedOrBuiltinType(t reflect.Type) bool {
	return ast.IsExported(t.Name()) || t.PkgPath() == ""
}

func (s *service) call(m *methodType, argv reflect.Value, replyv reflect.Value) error {
	atomic.AddUint64(&m.numCalls, 1)
	f := m.method.Func
	// 调用方法
	returnValues := f.Call([]reflect.Value{s.rcvr, argv, replyv})
	if errInter := returnValues[0].Interface(); errInter != nil {
		return errInter.(error)
	}
	return nil
}
