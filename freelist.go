package bbolt

import (
	"fmt"
	"sort"
	"unsafe"
)

// txPending holds a list of pgids and corresponding allocation txns
// that are pending to be freed.
type txPending struct {
	ids []pgid // 等待释放到空闲列表的页，新的事务会把上一个事物使用的页放到这里，等到 commit 时放到空闲列表页里
	// @question: 是给分配当前页的事务id, 应该与 ids 是一一对应的
	alloctx          []txid // txids allocating the ids
	lastReleaseBegin txid   // beginning txid of last matching releaseRange
}

// pidSet holds the set of starting pgids which have the same span size
type pidSet map[pgid]struct{}

// freelist represents a list of all pages that are available for allocation.
// It also tracks pages that have been freed but are still in use by open transactions.
type freelist struct {
	freelistType FreelistType // freelist type
	ids          []pgid       // all free and available free page ids.
	// @question 分配的页与 txid 的映射，如果一次分配多个连续页，只记录连续开始页id 为什么？有什么用
	// 是因为一次分配多个页，只有第一页有id，剩余的是溢出页只是为了扩展连续页开始页的大小
	allocs  map[pgid]txid       // mapping of txid that allocated a pgid.
	pending map[txid]*txPending // mapping of soon-to-be free page ids by tx.  // 当前事务中在等待被释放的页，属于空闲页, 事务回滚会删除 txid 对应的 pgid
	cache   map[pgid]bool       // fast lookup of all free and pending page ids.  标记这个页是不是空闲的,目前看到释放某个页时会标记为true

	// 存储连续页面， key是有几个连续页面，value 是连续页面开始的pid集合
	// 比如有以下页面id：1,2,3,4, 8,9,10,11 15,16
	// 那么 freemaps结构就是
	//	map[uint64]pidSet{}{
	//		4: {1:{}, 8:{}},    包含4个连续页的是 （1，2，3，4） (8，9，10，11)， 只存连续页开始的id 1，8
	//		2: {15:{}}  包含2个连续页的是 （15，16）， 只存连续页开始的 id 15
	//	}
	freemaps map[uint64]pidSet // key is the size of continuous pages(span), value is a set which contains the starting pgids of same size

	// key 是连续页开始的id，value是有多少连续页
	forwardMap map[pgid]uint64 // key is start pgid, value is its span size
	// key 是连续页结束的id， value是有多少连续页
	// 所以假如 一个连续页开始id是4，连续数量是3那么存储结构应该是
	// forwardMap:map[pgid]uint64{4:3}
	// backwardMap:map[pgid]uint64{6:3}
	backwardMap    map[pgid]uint64             // key is end pgid, value is its span size
	allocate       func(txid txid, n int) pgid // the freelist allocate func
	free_count     func() int                  // the function which gives you free page number
	mergeSpans     func(ids pgids)             // the mergeSpan func
	getFreePageIDs func() []pgid               // get free pgids func
	readIDs        func(pgids []pgid)          // readIDs func reads list of pages and init the freelist
}

// newFreelist returns an empty, initialized freelist.
func newFreelist(freelistType FreelistType) *freelist {
	f := &freelist{
		freelistType: freelistType,
		allocs:       make(map[pgid]txid),
		pending:      make(map[txid]*txPending),
		cache:        make(map[pgid]bool),
		freemaps:     make(map[uint64]pidSet),
		forwardMap:   make(map[pgid]uint64),
		backwardMap:  make(map[pgid]uint64),
	}

	if freelistType == FreelistMapType {
		f.allocate = f.hashmapAllocate
		f.free_count = f.hashmapFreeCount
		f.mergeSpans = f.hashmapMergeSpans
		f.getFreePageIDs = f.hashmapGetFreePageIDs
		f.readIDs = f.hashmapReadIDs
	} else {
		f.allocate = f.arrayAllocate
		f.free_count = f.arrayFreeCount
		f.mergeSpans = f.arrayMergeSpans
		f.getFreePageIDs = f.arrayGetFreePageIDs
		f.readIDs = f.arrayReadIDs
	}

	return f
}

// size returns the size of the page after serialization.
func (f *freelist) size() int {
	// 空闲页数量
	n := f.count()
	// 因为 p.count 是 uint16 大小，所以最大元素数是0xFFFF
	if n >= 0xFFFF {
		// The first element will be used to store the count. See freelist.write.
		// 因为最大只能存取 uint16 0xFFFF 个元素，所以空闲列表ids第一个元素将用于存储计数，不再使用count来存储计数
		n++
	}
	return int(pageHeaderSize) + (int(unsafe.Sizeof(pgid(0))) * n)
}

// count returns count of pages on the freelist
func (f *freelist) count() int {
	return f.free_count() + f.pending_count()
}

// arrayFreeCount returns count of free pages(array version)
func (f *freelist) arrayFreeCount() int {
	return len(f.ids)
}

// pending_count returns count of pending pages
func (f *freelist) pending_count() int {
	var count int
	for _, txp := range f.pending {
		count += len(txp.ids)
	}
	return count
}

// copyall copies a list of all free ids and all pending ids in one sorted list.
// f.count returns the minimum length required for dst.
func (f *freelist) copyall(dst []pgid) {
	m := make(pgids, 0, f.pending_count())
	for _, txp := range f.pending {
		m = append(m, txp.ids...)
	}
	sort.Sort(m)
	mergepgids(dst, f.getFreePageIDs(), m)
}

// arrayAllocate returns the starting page id of a contiguous list of pages of a given size.
// If a contiguous block cannot be found then 0 is returned.
func (f *freelist) arrayAllocate(txid txid, n int) pgid {
	if len(f.ids) == 0 {
		return 0
	}

	var initial, previd pgid
	for i, id := range f.ids {
		if id <= 1 {
			panic(fmt.Sprintf("invalid page allocation: %d", id))
		}

		// Reset initial page if this is not contiguous.
		// 当前给的页id-上一页的id!=1 就说明不是连续页
		// 用当前给的页当作连续页的首个
		if previd == 0 || id-previd != 1 {
			initial = id
		}

		// If we found a contiguous block then remove it and return it.
		// 找到了足够的连续页，从空闲列表中删除这些连续页
		if (id-initial)+1 == pgid(n) {
			//If we're allocating off the beginning then take the fast path
			//and just adjust the existing slice. This will use extra memory
			//temporarily but the append() in free() will realloc the slice
			//as is necessary.
			if (i + 1) == n {
				// 如果从数组首为到现在都是连续页，直接快速删除，不会分配内存
				f.ids = f.ids[i+1:]
			} else {
				// 这种应该是从中间找到的空闲连续页
				copy(f.ids[i-n+1:], f.ids[i+1:]) // 把后边的页拷贝到前边，覆盖中间要删除的连续页，这种应也不会重新分配内存
				f.ids = f.ids[:len(f.ids)-n]     // 然后截取-n
			}

			// Remove from the free cache.
			// 从空闲缓存中删除这些页
			for i := pgid(0); i < pgid(n); i++ {
				delete(f.cache, initial+i)
			}
			// @question: 分配的连续页的开始页与 txid 的映射？ 为什么？
			f.allocs[initial] = txid
			return initial
		}

		previd = id
	}
	return 0
}

// free releases a page and its overflow for a given transaction id.
// If the page is already free then a panic will occur.
func (f *freelist) free(txid txid, p *page) {
	if p.id <= 1 {
		panic(fmt.Sprintf("cannot free page 0 or 1: %d", p.id))
	}

	// Free page and all its overflow pages.
	txp := f.pending[txid]
	if txp == nil {
		txp = &txPending{}
		f.pending[txid] = txp
	}
	allocTxid, ok := f.allocs[p.id]
	if ok {
		delete(f.allocs, p.id)
	} else if (p.flags & freelistPageFlag) != 0 {
		// Freelist is always allocated by prior tx.
		// @question: 空闲列表页是由先前的事务分配的, 猜测每次事务提交都会重新刷新页，随机选一个空闲页作为空闲列表页
		allocTxid = txid - 1
	}

	// 溢出页也要一起释放
	for id := p.id; id <= p.id+pgid(p.overflow); id++ {
		// Verify that page is not already free.
		if f.cache[id] {
			panic(fmt.Sprintf("page %d already freed", id))
		}
		// Add to the freelist and cache.
		txp.ids = append(txp.ids, id)                // 把当前页id加到待释放数组，等待释放
		txp.alloctx = append(txp.alloctx, allocTxid) // 把分配这个页的事务与这个页关联
		f.cache[id] = true                           // 标记当前页已空闲
	}
}

// release moves all page ids for a transaction id (or older) to the freelist.
func (f *freelist) release(txid txid) {
	m := make(pgids, 0)
	for tid, txp := range f.pending {
		if tid <= txid {
			// Move transaction's pending pages to the available freelist.
			// Don't remove from the cache since the page is still free.
			m = append(m, txp.ids...)
			delete(f.pending, tid)
		}
	}
	// 把新的空闲出来 pids 合并到 空闲列表，并更新span
	f.mergeSpans(m)
}

// releaseRange moves pending pages allocated within an extent [begin,end] to the free list.
func (f *freelist) releaseRange(begin, end txid) {
	if begin > end {
		return
	}
	var m pgids
	for tid, txp := range f.pending {
		if tid < begin || tid > end {
			continue
		}
		// Don't recompute freed pages if ranges haven't updated.
		if txp.lastReleaseBegin == begin {
			continue
		}
		for i := 0; i < len(txp.ids); i++ {
			if atx := txp.alloctx[i]; atx < begin || atx > end {
				continue
			}
			m = append(m, txp.ids[i])
			txp.ids[i] = txp.ids[len(txp.ids)-1]
			txp.ids = txp.ids[:len(txp.ids)-1]
			txp.alloctx[i] = txp.alloctx[len(txp.alloctx)-1]
			txp.alloctx = txp.alloctx[:len(txp.alloctx)-1]
			i--
		}
		txp.lastReleaseBegin = begin
		if len(txp.ids) == 0 {
			delete(f.pending, tid)
		}
	}
	f.mergeSpans(m)
}

// rollback removes the pages from a given pending tx.
func (f *freelist) rollback(txid txid) {
	// Remove page ids from cache.
	txp := f.pending[txid]
	if txp == nil {
		return
	}
	var m pgids
	for i, pgid := range txp.ids {
		delete(f.cache, pgid)
		tx := txp.alloctx[i]
		if tx == 0 {
			continue
		}
		if tx != txid {
			// Pending free aborted; restore page back to alloc list.
			f.allocs[pgid] = tx
		} else {
			// Freed page was allocated by this txn; OK to throw away.
			m = append(m, pgid)
		}
	}
	// Remove pages from pending list and mark as free if allocated by txid.
	delete(f.pending, txid)
	f.mergeSpans(m)
}

// freed returns whether a given page is in the free list.
func (f *freelist) freed(pgid pgid) bool {
	return f.cache[pgid]
}

// read initializes the freelist from a freelist page.
func (f *freelist) read(p *page) {
	if (p.flags & freelistPageFlag) == 0 {
		panic(fmt.Sprintf("invalid freelist page: %d, page type is %s", p.id, p.typ()))
	}
	// If the page.count is at the max uint16 value (64k) then it's considered
	// an overflow and the size of the freelist is stored as the first element.
	var idx, count = 0, int(p.count)
	if count == 0xFFFF {
		idx = 1
		// 如果空闲页个数超出了 0xFFFF（因为page.count 是 uint16 最大是 0xFFFF 65535），那么空闲数组的第一个元素将存储的是空闲页的个数，负责数组的个数存在 page.count
		// https://jaydenwen123.github.io/boltdb/chapter2/%E7%A9%BA%E9%97%B2%E5%88%97%E8%A1%A8%E9%A1%B5.html
		c := *(*pgid)(unsafeAdd(unsafe.Pointer(p), unsafe.Sizeof(*p)))
		count = int(c)
		if count < 0 {
			panic(fmt.Sprintf("leading element count %d overflows int", c))
		}
	}

	// Copy the list of page ids from the freelist.
	if count == 0 {
		f.ids = nil
	} else {
		var ids []pgid
		// 获取空闲数组内存启始地址
		data := unsafeIndex(unsafe.Pointer(p), unsafe.Sizeof(*p), unsafe.Sizeof(ids[0]), idx)
		// 把空闲数组内存转为 slice 给到 ids
		unsafeSlice(unsafe.Pointer(&ids), data, count)

		// copy the ids, so we don't modify on the freelist page directly
		// copy ids， 应为上边的ids是直接引用的内存，如果之后排序会影响到freelist
		idsCopy := make([]pgid, count)
		copy(idsCopy, ids)
		// Make sure they're sorted.
		sort.Sort(pgids(idsCopy))

		f.readIDs(idsCopy)
	}
}

// arrayReadIDs initializes the freelist from a given list of ids.
func (f *freelist) arrayReadIDs(ids []pgid) {
	f.ids = ids
	f.reindex()
}

func (f *freelist) arrayGetFreePageIDs() []pgid {
	return f.ids
}

// write writes the page ids onto a freelist page. All free and pending ids are
// saved to disk since in the event of a program crash, all pending ids will
// become free.
func (f *freelist) write(p *page) error {
	// Combine the old free pgids and pgids waiting on an open transaction.

	// Update the header flag.
	p.flags |= freelistPageFlag

	// The page.count can only hold up to 64k elements so if we overflow that
	// number then we handle it by putting the size in the first element.
	l := f.count()
	if l == 0 {
		p.count = uint16(l)
	} else if l < 0xFFFF {
		p.count = uint16(l)
		var ids []pgid
		data := unsafeAdd(unsafe.Pointer(p), unsafe.Sizeof(*p))
		unsafeSlice(unsafe.Pointer(&ids), data, l)
		f.copyall(ids)
	} else {
		// count 是 uint16 类型，真实的空闲id数量已经超过了 count， 所以把 空闲id列表 的第一个元素作为 count
		p.count = 0xFFFF
		var ids []pgid
		data := unsafeAdd(unsafe.Pointer(p), unsafe.Sizeof(*p))
		unsafeSlice(unsafe.Pointer(&ids), data, l+1)
		ids[0] = pgid(l)
		f.copyall(ids[1:])
	}

	return nil
}

// reload reads the freelist from a page and filters out pending items.
func (f *freelist) reload(p *page) {
	f.read(p)

	// Build a cache of only pending pages.
	pcache := make(map[pgid]bool)
	for _, txp := range f.pending {
		for _, pendingID := range txp.ids {
			pcache[pendingID] = true
		}
	}

	// Check each page in the freelist and build a new available freelist
	// with any pages not in the pending lists.
	var a []pgid
	for _, id := range f.getFreePageIDs() {
		if !pcache[id] {
			a = append(a, id)
		}
	}

	f.readIDs(a)
}

// noSyncReload reads the freelist from pgids and filters out pending items.
func (f *freelist) noSyncReload(pgids []pgid) {
	// Build a cache of only pending pages.
	pcache := make(map[pgid]bool)
	for _, txp := range f.pending {
		for _, pendingID := range txp.ids {
			pcache[pendingID] = true
		}
	}

	// Check each page in the freelist and build a new available freelist
	// with any pages not in the pending lists.
	var a []pgid
	for _, id := range pgids {
		if !pcache[id] {
			a = append(a, id)
		}
	}

	f.readIDs(a)
}

// reindex rebuilds the free cache based on available and pending free lists.
func (f *freelist) reindex() {
	ids := f.getFreePageIDs()
	f.cache = make(map[pgid]bool, len(ids))
	for _, id := range ids {
		f.cache[id] = true
	}
	for _, txp := range f.pending {
		for _, pendingID := range txp.ids {
			f.cache[pendingID] = true
		}
	}
}

// arrayMergeSpans try to merge list of pages(represented by pgids) with existing spans but using array
func (f *freelist) arrayMergeSpans(ids pgids) {
	sort.Sort(ids)
	f.ids = pgids(f.ids).merge(ids)
}
