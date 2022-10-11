[x] Open 打开db
[x] DB.Begin() 开启事务
[x] Tx.CreateBucketIfNotExists 如果存储桶不存在就创建，否则直接返回这个桶
[x] Tx.Bucket 查找桶，不存在返回nil
[x] Bucket.Put()


资料：https://jaydenwen123.github.io/boltdb/chapter3/boltdb%E7%9A%84Bucket%E7%BB%93%E6%9E%84.html

图片很关键
![bucket存储图片](bucket存储图片.png)