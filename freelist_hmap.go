package bbolt

import "sort"

// hashmapFreeCount returns count of free pages(hashmap version)
func (f *freelist) hashmapFreeCount() int {
	// use the forwardMap to get the total count
	count := 0
	for _, size := range f.forwardMap {
		count += int(size)
	}
	return count
}

// hashmapAllocate serves the same purpose as arrayAllocate, but use hashmap as backend
func (f *freelist) hashmapAllocate(txid txid, n int) pgid {
	if n == 0 {
		return 0
	}

	// if we have a exact size match just return short path
	// 如果 freemaps 里有相同长度的连续页，直接拿来用
	if bm, ok := f.freemaps[uint64(n)]; ok {
		// 这里循环删除不太科学吧，不应该从 bm 列表里取一个用 删掉吗？怎么把所有连续页都删了？？？
		// 哈哈 我看错了，循环里有个 return，也就是说取到第一个删掉就 return 了
		for pid := range bm {
			// remove the span
			f.delSpan(pid, uint64(n))

			f.allocs[pid] = txid

			for i := pgid(0); i < pgid(n); i++ {
				delete(f.cache, pid+i)
			}
			return pid
		}
	}

	// lookup the map to find larger span
	// 找到一个大于 n 的连续页，从中截取需要的长度
	for size, bm := range f.freemaps {
		if size < uint64(n) {
			continue
		}

		for pid := range bm {
			// remove the initial
			f.delSpan(pid, size)

			f.allocs[pid] = txid

			// 剩余的连续页长度
			remain := size - uint64(n)

			// add remain span
			// 把剩余的连续页添加到 span
			f.addSpan(pid+pgid(n), remain)

			for i := pgid(0); i < pgid(n); i++ {
				delete(f.cache, pid+i)
			}
			return pid
		}
	}

	return 0
}

// hashmapReadIDs reads pgids as input an initial the freelist(hashmap version)
func (f *freelist) hashmapReadIDs(pgids []pgid) {
	f.init(pgids)

	// Rebuild the page cache.
	f.reindex()
}

// hashmapGetFreePageIDs returns the sorted free page ids
func (f *freelist) hashmapGetFreePageIDs() []pgid {
	count := f.free_count()
	if count == 0 {
		return nil
	}

	m := make([]pgid, 0, count)
	for start, size := range f.forwardMap {
		for i := 0; i < int(size); i++ {
			m = append(m, start+pgid(i))
		}
	}
	sort.Sort(pgids(m))

	return m
}

// hashmapMergeSpans try to merge list of pages(represented by pgids) with existing spans
func (f *freelist) hashmapMergeSpans(ids pgids) {
	for _, id := range ids {
		// try to see if we can merge and update
		f.mergeWithExistingSpan(id)
	}
}

// mergeWithExistingSpan merges pid to the existing free spans, try to merge it backward and forward
func (f *freelist) mergeWithExistingSpan(pid pgid) {
	prev := pid - 1
	next := pid + 1

	preSize, mergeWithPrev := f.backwardMap[prev]
	nextSize, mergeWithNext := f.forwardMap[next]
	newStart := pid
	newSize := uint64(1)

	if mergeWithPrev {
		//merge with previous span
		start := prev + 1 - pgid(preSize)
		f.delSpan(start, preSize)

		newStart -= pgid(preSize)
		newSize += preSize
	}

	if mergeWithNext {
		// merge with next span
		f.delSpan(next, nextSize)
		newSize += nextSize
	}

	f.addSpan(newStart, newSize)
}

func (f *freelist) addSpan(start pgid, size uint64) {
	// 记录连续页的结束页
	f.backwardMap[start-1+pgid(size)] = size
	// 记录连续页的开始页页
	f.forwardMap[start] = size

	// 记录连续页的数量与连续页的开始页的映射
	if _, ok := f.freemaps[size]; !ok {
		f.freemaps[size] = make(map[pgid]struct{})
	}

	f.freemaps[size][start] = struct{}{}
}

func (f *freelist) delSpan(start pgid, size uint64) {
	delete(f.forwardMap, start)
	delete(f.backwardMap, start+pgid(size-1))
	delete(f.freemaps[size], start)
	if len(f.freemaps[size]) == 0 {
		delete(f.freemaps, size)
	}
}

// initial from pgids using when use hashmap version
// pgids must be sorted
func (f *freelist) init(pgids []pgid) {
	if len(pgids) == 0 {
		return
	}

	size := uint64(1)
	start := pgids[0]

	if !sort.SliceIsSorted([]pgid(pgids), func(i, j int) bool { return pgids[i] < pgids[j] }) {
		panic("pgids not sorted")
	}

	f.freemaps = make(map[uint64]pidSet)
	f.forwardMap = make(map[pgid]uint64)
	f.backwardMap = make(map[pgid]uint64)

	for i := 1; i < len(pgids); i++ {
		// continuous page
		// 当前页id == 上一页id+1 那就代表这两个页是连续的
		// size 就是记录连续的页数量
		if pgids[i] == pgids[i-1]+1 {
			size++
		} else {
			// 如果当前页与上一页不再连续就记录下来
			f.addSpan(start, size)

			// 把连续数量致为1
			size = 1
			// 开始页是当前页
			start = pgids[i]
		}
	}

	// 记录最后页，有可能有连续页也有可能没连续页
	// init the tail
	if size != 0 && start != 0 {
		f.addSpan(start, size)
	}
}
