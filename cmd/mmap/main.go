package main

import (
	"fmt"
	"golang.org/x/sys/unix"
	"math"
	"os"
	"syscall"
	"time"
	"unsafe"
)

// dd 命令生成指定大小的文件
// dd if=/dev/zero of=a.txt bs=1k count=32
func main() {
	fmt.Println(math.MaxUint16)
	return
	f, err := os.Open("a.txt")
	if err != nil {
		panic(err)
	}
	defer f.Close()

	b, err := unix.Mmap(int(f.Fd()), 0, 16*2*1024, syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		panic(err)
	}
	a := (*[16384]byte)(unsafe.Pointer(&b[0]))
	fmt.Printf("len=[%d] b old %p\n", len(b), b)
	fmt.Printf("len=[%d] a old %p\n", len(a), a)

	//c, err := unix.Mmap(int(f.Fd()), 0, 100, syscall.PROT_READ, syscall.MAP_SHARED)
	//if err != nil {
	//	panic(err)
	//}

	fmt.Println("等待")
	time.Sleep(10 * time.Second)
	fmt.Println(string(b[:16385]))
	fmt.Println(string(a[0:100]))
	fmt.Println(a[16383])
	//fmt.Printf("len=[%d] new %p   %s\n", len(c), c, string(c))
}
