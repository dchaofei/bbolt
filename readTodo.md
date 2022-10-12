- [x] Open 打开db
- [x] DB.Begin() 开启事务
- [x] Tx.CreateBucketIfNotExists 如果存储桶不存在就创建，否则直接返回这个桶
- [x] Tx.Bucket 查找桶，不存在返回nil
- [x] Bucket.Put()
- [x] Bucket.Get()
- [x] Cursor.First() // 把游标移到桶内第一个元素，并返回 key value
- [x] Cursor.Next() // 把游标移到下一个元素，并返回 key value 有 bug 空节点异常
- [x] Cursor.Last() // 把游标移到最后一个元素，并返回 key value
- [x] Cursor.Prev() // 把游标移到上一个元素，并返回 key value
- [x] Cursor.Seek() // 把游标移到给定的key，并返回 key value, 如果没找到，返回下一个元素的


资料：https://jaydenwen123.github.io/boltdb/chapter3/boltdb%E7%9A%84Bucket%E7%BB%93%E6%9E%84.html

图片很关键
![bucket存储图片](bucket存储图片.png)