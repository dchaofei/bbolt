package main

import (
	"encoding/binary"
	bolt "go.etcd.io/bbolt"
	"log"
	"time"
)

func main() {
	db, err := bolt.Open("my.db", 0600, &bolt.Options{
		Timeout:        3 * time.Second,
		NoGrowSync:     false,
		NoFreelistSync: true,
		FreelistType:   "",
		ReadOnly:       false,
		MmapFlags:      0,
		NoSync:         false,
		OpenFile:       nil,
		Mlock:          false,
	})
	if err != nil {
		log.Fatal(err)
	}

	defer db.Close()

	tx, err := db.Begin(true)
	a1, err := tx.CreateBucketIfNotExists([]byte("a"))
	a1.Put([]byte("1"), []byte("1"))
	a1.Put([]byte("2"), []byte("2"))
	checkErr(err)
	a2, err := a1.CreateBucketIfNotExists([]byte("a1"))
	checkErr(err)
	a2.Put([]byte("1"), []byte("1"))
	a2.Put([]byte("2"), []byte("3"))
	a2.Put([]byte("2"), []byte("4"))

	//err = tx.DeleteBucket([]byte("a"))
	checkErr(err)

	//err = c.Put([]byte("aa"), []byte("bb"))
	//err = c.Put([]byte("cc"), []byte("bb"))
	//err = c.Put([]byte("dd"), []byte("bb"))
	//err = c.Put([]byte("ee"), []byte("bb"))
	//err = c.Put([]byte("ff"), []byte("bb"))
	//checkErr(err)
	checkErr(tx.Commit())
	//
	//tx, err = db.Begin(true)
	//c, err = tx.CreateBucketIfNotExists([]byte("b"))
	//checkErr(err)
	//err = c.Put([]byte("aa"), []byte("bb"))
	//err = c.Put([]byte("cc"), []byte("bb"))
	//err = c.Put([]byte("dd"), []byte("bb"))
	//err = c.Put([]byte("ee"), []byte("bb"))
	//err = c.Put([]byte("ff"), []byte("bb"))
	//checkErr(err)
	//checkErr(tx.Commit())
	//fmt.Println("test")
	//err = db.Update(func(tx *bolt.Tx) error {
	//	_, err := tx.CreateBucket([]byte("ding"))
	//	return err
	//})
	//checkErr(err)
	//s := new(runtime.MemStats)
	//runtime.ReadMemStats(s)
	//fmt.Printf("%+v\n", s.Sys)
	//fmt.Printf("%+v\n", s.HeapAlloc)
	//time.Sleep(2 * time.Hour)

	//db.View(func(tx *bolt.Tx) error {
	//	f, err := os.OpenFile("back.db", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	//	if err != nil {
	//		panic(err)
	//	}
	//	defer f.Close()
	//	_, err = tx.WriteTo(f)
	//	checkErr(err)
	//	return nil
	//})

	//count := int64(0)
	//
	//start := time.Now()
	//defer func() {
	//	fmt.Println(time.Since(start), count)
	//}()
	//a := sync.WaitGroup{}
	//for i := 0; i <= 1000; i++ {
	//	i := i
	//	a.Add(1)
	//	go func() {
	//		defer a.Done()
	//		start := time.Now()
	//		defer func() {
	//			count := atomic.AddInt64(&count, 1)
	//			fmt.Println(time.Since(start), count)
	//		}()
	//		err := db.Update(func(tx *bolt.Tx) error {
	//			a, err := tx.CreateBucketIfNotExists([]byte("a"))
	//			checkErr(err)
	//			err = a.Put([]byte("b"), []byte(fmt.Sprintf("%v", i)))
	//			checkErr(err)
	//			//res := a.Get([]byte("b"))
	//			//if string(res) != fmt.Sprintf("%v", i) {
	//			//	panic("不等")
	//			//}
	//			return nil
	//		})
	//		err = db.View(func(tx *bolt.Tx) error {
	//			a := tx.Bucket([]byte("a"))
	//			res := a.Get([]byte("b"))
	//			//if string(res) != fmt.Sprintf("%v", i) {
	//			//	panic("不等")
	//			//}
	//			d := res
	//			_ = d
	//			_ = i
	//			fmt.Println(string(res))
	//			return nil
	//		})
	//		checkErr(err)
	//	}()
	//}
	//
	//a.Wait()
}
func itob(v int) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(v))
	return b
}

func b(s string) []byte {
	return []byte(s)
}

func checkErr(err error) {
	if err == nil {
		return
	}
	panic(err)
}
