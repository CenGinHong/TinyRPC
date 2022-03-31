package tiny

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

type Foo int

type Args struct {
	Num1 int
	Num2 int
}

func (f Foo) Sum(args Args, reply *int) error {
	*reply = args.Num1 + args.Num2
	return nil
}

func (f Foo) sum(args Args, reply *int) error {
	*reply = args.Num1 + args.Num2
	return nil
}

func _assert(condition bool, msg string, v ...interface{}) {
	if !condition {
		panic(fmt.Sprintf("assertion filed: "+msg, v))
	}
}

func TestNewService(t *testing.T) {
	var foo Foo
	s := newService(&foo)
	mType := s.method["Sum"]
	argv := mType.newArgv()
	replyv := mType.newReply()
	argv.Set(reflect.ValueOf(Args{Num1: 1, Num2: 3}))
	err := s.call(mType, argv, replyv)
	_assert(err == nil && *replyv.Interface().(*int) == 4 && mType.NumCalls() == 1, "failed to call Foo.Sum")
}

func TestMethodType_Call(t *testing.T) {
	var foo Foo
	s := newService(&foo)
	mType := s.method["Sum"]
	argv := mType.newArgv()
	replyv := mType.newReply()
	argv.Set(reflect.ValueOf(Args{Num1: 1, Num2: 3}))
	err := s.call(mType, argv, replyv)
	_assert(err == nil && *replyv.Interface().(*int) == 4 && mType.NumCalls() == 1, "failed to call Foo.Sum")
}

func TestMi(t *testing.T) {
	var wg sync.WaitGroup
	cancel, cancelFunc := context.WithCancel(context.Background())
	defer cancelFunc()
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(ctx context.Context, i int) {
			defer wg.Done()
			if i > 50 {
				cancelFunc()
			}
			select {
			case <-time.After(3 * time.Second):
				fmt.Println(i)
			case <-cancel.Done():
				fmt.Println("cancel")
			}
		}(cancel, i)
	}
	wg.Wait()
}
