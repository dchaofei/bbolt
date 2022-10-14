package bbolt

import (
	"bytes"
	"fmt"
	"sort"
	"unsafe"
)

// node represents an in-memory, deserialized page.
// 是页的内存中表示，node 是由页转换而来
type node struct {
	bucket     *Bucket
	isLeaf     bool
	unbalanced bool
	spilled    bool // 是否已经将node节点数据写入到 page
	key        []byte
	pgid       pgid
	parent     *node
	children   nodes  // 子节点列表
	inodes     inodes // 当节点是分支节点是，inodes是子节点的索引列表，指向子节点的位置，当节点是叶子节点是，inodes是 key value 数据
}

// root returns the top-level node this node is attached to.
func (n *node) root() *node {
	if n.parent == nil {
		return n
	}
	return n.parent.root()
}

// minKeys returns the minimum number of inodes this node should have.
func (n *node) minKeys() int {
	if n.isLeaf {
		return 1
	}
	return 2
}

// size returns the size of the node after serialization.
func (n *node) size() int {
	// 页头大小+每个元素大小+key+value
	sz, elsz := pageHeaderSize, n.pageElementSize()
	for i := 0; i < len(n.inodes); i++ {
		item := &n.inodes[i]
		sz += elsz + uintptr(len(item.key)) + uintptr(len(item.value))
	}
	return int(sz)
}

// sizeLessThan returns true if the node is less than a given size.
// This is an optimization to avoid calculating a large node when we only need
// to know if it fits inside a certain page size.
func (n *node) sizeLessThan(v uintptr) bool {
	sz, elsz := pageHeaderSize, n.pageElementSize()
	for i := 0; i < len(n.inodes); i++ {
		item := &n.inodes[i]
		sz += elsz + uintptr(len(item.key)) + uintptr(len(item.value))
		if sz >= v {
			return false
		}
	}
	return true
}

// pageElementSize returns the size of each page element based on the type of node.
func (n *node) pageElementSize() uintptr {
	if n.isLeaf {
		return leafPageElementSize
	}
	return branchPageElementSize
}

// childAt returns the child node at a given index.
func (n *node) childAt(index int) *node {
	if n.isLeaf {
		panic(fmt.Sprintf("invalid childAt(%d) on a leaf node", index))
	}
	return n.bucket.node(n.inodes[index].pgid, n)
}

// childIndex returns the index of a given child node.
func (n *node) childIndex(child *node) int {
	index := sort.Search(len(n.inodes), func(i int) bool { return bytes.Compare(n.inodes[i].key, child.key) != -1 })
	return index
}

// numChildren returns the number of children.
func (n *node) numChildren() int {
	return len(n.inodes)
}

// nextSibling returns the next node with the same parent.
func (n *node) nextSibling() *node {
	if n.parent == nil {
		return nil
	}
	index := n.parent.childIndex(n)
	if index >= n.parent.numChildren()-1 {
		return nil
	}
	return n.parent.childAt(index + 1)
}

// prevSibling returns the previous node with the same parent.
func (n *node) prevSibling() *node {
	if n.parent == nil {
		return nil
	}
	index := n.parent.childIndex(n)
	if index == 0 {
		return nil
	}
	return n.parent.childAt(index - 1)
}

// put inserts a key/value.
// 注意，put 操作并没有改原 mmap 映射地址的值哈，只是把新值赋值给了结构体，如果改了 mmap 原地址的值肯定会报错的
func (n *node) put(oldKey, newKey, value []byte, pgid pgid, flags uint32) {
	if pgid >= n.bucket.tx.meta.pgid {
		panic(fmt.Sprintf("pgid (%d) above high water mark (%d)", pgid, n.bucket.tx.meta.pgid))
	} else if len(oldKey) <= 0 {
		panic("put: zero-length old key")
	} else if len(newKey) <= 0 {
		panic("put: zero-length new key")
	}

	// Find insertion index.
	index := sort.Search(len(n.inodes), func(i int) bool { return bytes.Compare(n.inodes[i].key, oldKey) != -1 })

	// Add capacity and shift nodes if we don't have an exact match and need to insert.
	exact := (len(n.inodes) > 0 && index < len(n.inodes) && bytes.Equal(n.inodes[index].key, oldKey))
	if !exact {
		n.inodes = append(n.inodes, inode{})
		copy(n.inodes[index+1:], n.inodes[index:]) // 插入一个节点： 先 append 一个，然后把index后的节点复制到 index+1， index 就变成了要插入的位置
	}

	inode := &n.inodes[index]
	inode.flags = flags
	inode.key = newKey
	inode.value = value
	inode.pgid = pgid
	_assert(len(inode.key) > 0, "put: zero-length inode key")
}

// del removes a key from the node.
func (n *node) del(key []byte) {
	// Find index of key.
	index := sort.Search(len(n.inodes), func(i int) bool { return bytes.Compare(n.inodes[i].key, key) != -1 })

	// Exit if the key isn't found.
	if index >= len(n.inodes) || !bytes.Equal(n.inodes[index].key, key) {
		return
	}

	// Delete inode from the node.
	n.inodes = append(n.inodes[:index], n.inodes[index+1:]...)

	// Mark the node as needing rebalancing.
	n.unbalanced = true
}

// read initializes the node from a page.
func (n *node) read(p *page) {
	n.pgid = p.id
	n.isLeaf = ((p.flags & leafPageFlag) != 0)
	n.inodes = make(inodes, int(p.count))

	for i := 0; i < int(p.count); i++ {
		inode := &n.inodes[i]
		if n.isLeaf {
			elem := p.leafPageElement(uint16(i))
			inode.flags = elem.flags
			inode.key = elem.key()
			inode.value = elem.value()
		} else {
			elem := p.branchPageElement(uint16(i))
			inode.pgid = elem.pgid
			inode.key = elem.key()
		}
		_assert(len(inode.key) > 0, "read: zero-length inode key")
	}

	// Save first key so we can find the node in the parent when we spill.
	if len(n.inodes) > 0 {
		n.key = n.inodes[0].key
		_assert(len(n.key) > 0, "read: zero-length node key")
	} else {
		n.key = nil
	}
}

// write writes the items onto one or more pages.
// 把 node 写到 page
func (n *node) write(p *page) {
	// Initialize page.
	if n.isLeaf {
		p.flags |= leafPageFlag
	} else {
		p.flags |= branchPageFlag
	}

	// @question： 为什么是 >= 0xFFFF
	if len(n.inodes) >= 0xFFFF {
		panic(fmt.Sprintf("inode overflow: %d (pgid=%d)", len(n.inodes), p.id))
	}
	p.count = uint16(len(n.inodes))

	// Stop here if there are no items to write.
	if p.count == 0 {
		return
	}

	// Loop over each item and write it to the page.
	// off tracks the offset into p of the start of the next data.

	// 初始值是这个节点所需的初key value 所需的大小
	// 也就是页头大小 加所有的 leafPageElement or branchPageElement 的大小
	off := unsafe.Sizeof(*p) + n.pageElementSize()*uintptr(len(n.inodes))
	for i, item := range n.inodes {
		_assert(len(item.key) > 0, "write: zero-length inode key")

		//Create a slice to write into of needed size and advance
		//byte pointer for next iteration.
		sz := len(item.key) + len(item.value)
		// 从先前分配的内存地址中找到 key value 插入的位置并转为 byte
		b := unsafeByteSlice(unsafe.Pointer(p), off, 0, sz)
		off += uintptr(sz)

		// Write the page element.
		if n.isLeaf {
			// 从 p 中找到转换成 elem 的指针
			elem := p.leafPageElement(uint16(i))
			// 相对 pos 的值就是 key 所在的指针-元素所在的指针
			elem.pos = uint32(uintptr(unsafe.Pointer(&b[0])) - uintptr(unsafe.Pointer(elem)))
			elem.flags = item.flags
			elem.ksize = uint32(len(item.key))
			elem.vsize = uint32(len(item.value))
		} else {
			elem := p.branchPageElement(uint16(i))
			elem.pos = uint32(uintptr(unsafe.Pointer(&b[0])) - uintptr(unsafe.Pointer(elem)))
			elem.ksize = uint32(len(item.key))
			elem.pgid = item.pgid
			// 如果子节点id=父节点，循环依赖
			_assert(elem.pgid != p.id, "write: circular dependency occurred")
		}

		// Write data for the element to the end of the page.
		l := copy(b, item.key)
		copy(b[l:], item.value)
	}

	// DEBUG ONLY: n.dump()
}

// split breaks up a node into multiple smaller nodes, if appropriate.
// This should only be called from the spill() function.
// 将一个节点拆分为多个更小的节点
func (n *node) split(pageSize uintptr) []*node {
	var nodes []*node

	node := n
	for {
		// Split node into two.
		// 每次拆分出两个节点，被拆分的节点 a 肯定小于等于 pageSize， 所以符合大小
		// 再拆b，以此循环直到拆到都符合大小
		// 比如一个节点大小是103，pageSize是5，假设阈值是1
		// 那么会拆分出 100/5 = 20 个节点
		a, b := node.splitTwo(pageSize)
		nodes = append(nodes, a)

		// If we can't split then exit the loop.
		if b == nil {
			break
		}

		// Set node to b so it gets split on the next iteration.
		node = b
	}

	return nodes
}

// splitTwo breaks up a node into two smaller nodes, if appropriate.
// This should only be called from the split() function.
func (n *node) splitTwo(pageSize uintptr) (*node, *node) {
	// Ignore the split if the page doesn't have at least enough nodes for
	// two pages or if the nodes can fit in a single page.
	// 第一个判断没看懂，是必须这个页存的key>4才可以拆分两个页吗？是怎么确定当key数量<=4时这个页可以容纳完？是key，value大小有限制？懂了
	// > 因为 4 个节点好像无法判断拆分节点是否大于阈值，可以看 n.splitIndex 里 for 循环的判断
	// > 如果你有4个节点，每个页最少要有两个节点，确实无法拆分页
	// > 假如每页最大大小是 10， 第一个节点占2个 第二节点占3， 第三个节点占3，第四个节点占20，
	// > 节点一、二、三大小是8，再加第四个节点大于页大小了，所以节点一、二、三分到一个页，但是还剩一个四节点无法分配到页，因为每个页最少要两个节点
	// 第二个判断可以理解，如果当前页总大小小于页大小就代表可以容纳，不需要拆分
	if len(n.inodes) <= (minKeysPerPage*2) || n.sizeLessThan(pageSize) {
		return n, nil
	}

	// Determine the threshold before starting a new node.
	var fillPercent = n.bucket.FillPercent
	if fillPercent < minFillPercent {
		fillPercent = minFillPercent
	} else if fillPercent > maxFillPercent {
		fillPercent = maxFillPercent
	}
	threshold := int(float64(pageSize) * fillPercent)

	// Determine split position and sizes of the two pages.
	splitIndex, _ := n.splitIndex(threshold)

	// Split node into two separate nodes.
	// If there's no parent then we'll need to create one.
	// 拆分为了两个节点。所以必须要有一个父节点，没有就创建一个父节点
	if n.parent == nil {
		n.parent = &node{bucket: n.bucket, children: []*node{n}}
	}

	// Create a new node and add it to the parent.
	// 创建第二个节点添加到父节点
	next := &node{bucket: n.bucket, isLeaf: n.isLeaf, parent: n.parent}
	n.parent.children = append(n.parent.children, next)

	// Split inodes across two nodes.
	// 把索引节点查分到两个节点
	next.inodes = n.inodes[splitIndex:]
	n.inodes = n.inodes[:splitIndex]

	// Update the statistics.
	n.bucket.tx.stats.Split++

	return n, next
}

// splitIndex finds the position where a page will fill a given threshold.
// It returns the index as well as the size of the first page.
// This is only be called from split().
// 找到需要分割的位置,threshold 可以理解为是分割点
func (n *node) splitIndex(threshold int) (index, sz uintptr) {
	sz = pageHeaderSize

	// Loop until we only have the minimum number of keys required for the second page.
	for i := 0; i < len(n.inodes)-minKeysPerPage; i++ {
		index = uintptr(i)
		inode := n.inodes[i]
		elsize := n.pageElementSize() + uintptr(len(inode.key)) + uintptr(len(inode.value))

		// If we have at least the minimum number of keys and adding another
		// node would put us over the threshold then exit and return.
		// 如果我们已经最少有了两个key，并且再添加一个key会超过分割点，那就直接返回可以分割的位置，取出[0:index]为第一节点，[index:]为第二个节点
		if index >= minKeysPerPage && sz+elsize > uintptr(threshold) {
			break
		}

		// Add the element size to the total size.
		sz += elsize
	}

	return
}

// spill writes the nodes to dirty pages and splits nodes as it goes.
// Returns an error if dirty pages cannot be allocated.
// 把节点拆分到合适大小的多个节点中，并都分配好 page， 写入到 page 使用的内存里
func (n *node) spill() error {
	var tx = n.bucket.tx
	if n.spilled {
		return nil
	}

	// Spill child nodes first. Child nodes can materialize sibling nodes in
	// the case of split-merge so we cannot use a range loop. We have to check
	// the children size on every loop iteration.
	sort.Sort(n.children)
	for i := 0; i < len(n.children); i++ {
		if err := n.children[i].spill(); err != nil {
			return err
		}
	}

	// We no longer need the child list because it's only used for spill tracking.
	// @question：是因为 children 节点已经拆分了，所以不要了？
	n.children = nil

	// Split nodes into appropriate sizes. The first node will always be n.
	// 把一个节点拆分到合适大小的多个节点
	var nodes = n.split(uintptr(tx.db.pageSize))
	for _, node := range nodes {
		// Add node's page to the freelist if it's not new.
		// 新页面 pgid 是 0，不是新页面就把这个页释放掉，然后把 pgid 置为 0
		// 是为了重新分配内存吧 因为不能修改原来的 mmap 映射的内存
		if node.pgid > 0 {
			tx.db.freelist.free(tx.meta.txid, tx.page(node.pgid))
			node.pgid = 0
		}

		// Allocate contiguous space for the node.
		// 为节点分配连续空间
		// 根据size计算需要分配多少页
		// 是不是感觉这里计算是多余的？因为 n.split 分割出来的数据肯定是小于等于 pageSize 的，直接分配一页就行了啊
		// 这是因为 node.splitTwo 判断 len(n.inodes) <= (minKeysPerPage*2) 就不拆分了，因此 node 大小是有可能大于 pageSize 的
		p, err := tx.allocate((node.size() + tx.db.pageSize - 1) / tx.db.pageSize)
		if err != nil {
			return err
		}

		// Write the node.
		if p.id >= tx.meta.pgid {
			panic(fmt.Sprintf("pgid (%d) above high water mark (%d)", p.id, tx.meta.pgid))
		}
		node.pgid = p.id
		node.write(p)
		node.spilled = true

		// Insert into parent inodes.
		// 把当前节点插入到父节点
		if node.parent != nil {
			var key = node.key
			if key == nil {
				key = node.inodes[0].key
			}

			node.parent.put(key, node.inodes[0].key, nil, node.pgid, 0)
			node.key = node.inodes[0].key
			_assert(len(node.key) > 0, "spill: zero-length node key")
		}

		// Update the statistics.
		tx.stats.Spill++
	}

	// If the root node split and created a new root then we need to spill that
	// as well. We'll clear out the children to make sure it doesn't try to respill.
	if n.parent != nil && n.parent.pgid == 0 {
		// 走到这里说明孩子节点都已经 spill 完了，所以移除孩子节点防止被 spill
		n.children = nil
		return n.parent.spill()
	}

	return nil
}

// rebalance attempts to combine the node with sibling nodes if the node fill
// size is below a threshold or if there are not enough keys.
func (n *node) rebalance() {
	if !n.unbalanced {
		return
	}
	n.unbalanced = false

	// Update statistics.
	n.bucket.tx.stats.Rebalance++

	// Ignore if node is above threshold (25%) and has enough keys.
	// @question: 为什么页实际占用大小 > 操作系统页大小的25%并且节点（元素）数足够就不执行合并操作
	// 大概是页大小占用足够，不需要合并节点操作了，只针对小的节点合并节点
	var threshold = n.bucket.tx.db.pageSize / 4
	if n.size() > threshold && len(n.inodes) > n.minKeys() {
		return
	}

	// Root node has special handling.
	if n.parent == nil {
		// If root node is a branch and only has one node then collapse it. 如果根节点是分支节点并且节点数为1，那就把下一个节点提升为根节点
		if !n.isLeaf && len(n.inodes) == 1 {
			// Move root's child up.
			child := n.bucket.node(n.inodes[0].pgid, n)
			n.isLeaf = child.isLeaf
			n.inodes = child.inodes[:]
			n.children = child.children

			// Reparent all child nodes being moved.  因为他们的父几点被删除了，提升为了根节点，所以把 parent 指向根节点
			for _, inode := range n.inodes {
				if child, ok := n.bucket.nodes[inode.pgid]; ok {
					child.parent = n
				}
			}

			// Remove old child.
			child.parent = nil
			delete(n.bucket.nodes, child.pgid)
			child.free() // 这个中间节点的内容已经被提升到根节点了，所以释放这个节点
		}

		return
	}

	// If node has no keys then just remove it. // 如果这个节点是空的，删除这个节点
	if n.numChildren() == 0 {
		n.parent.del(n.key)     // 调用父节点删除当前子节点inode
		n.parent.removeChild(n) // 调用父节点删除当前子节点
		delete(n.bucket.nodes, n.pgid)
		n.free()             // 释放节点，可能被以后的事务重新使用
		n.parent.rebalance() // 对父节点再平衡
		return
	}

	_assert(n.parent.numChildren() > 1, "parent must have at least 2 children")

	// Destination node is right sibling if idx == 0, otherwise left sibling.
	var target *node
	var useNextSibling = (n.parent.childIndex(n) == 0)
	if useNextSibling {
		target = n.nextSibling() // 右兄弟节点
	} else {
		target = n.prevSibling() // 左兄弟节点
	}

	// If both this node and the target node are too small then merge them.
	// 无非就是把左节点的数据合并到右节点，或把右节点的数据合并到左节点，最后释放掉被合并的页
	if useNextSibling {
		// Reparent all child nodes being moved.
		// 把右兄弟节点的数据都移到当前节点
		for _, inode := range target.inodes {
			if child, ok := n.bucket.nodes[inode.pgid]; ok {
				child.parent.removeChild(child)
				child.parent = n
				child.parent.children = append(child.parent.children, child)
			}
		}

		// Copy over inodes from target and remove target.
		n.inodes = append(n.inodes, target.inodes...)
		n.parent.del(target.key)
		n.parent.removeChild(target)
		delete(n.bucket.nodes, target.pgid)
		target.free()
	} else {
		// Reparent all child nodes being moved.
		// 把左兄弟节点的数据都移到当前节点，
		for _, inode := range n.inodes {
			if child, ok := n.bucket.nodes[inode.pgid]; ok {
				child.parent.removeChild(child)
				child.parent = target
				child.parent.children = append(child.parent.children, child)
			}
		}

		// Copy over inodes to target and remove node.
		target.inodes = append(target.inodes, n.inodes...)
		n.parent.del(n.key)
		n.parent.removeChild(n)
		delete(n.bucket.nodes, n.pgid)
		n.free()
	}

	// Either this node or the target node was deleted from the parent so rebalance it.
	// 重新平衡父节点
	n.parent.rebalance()
}

// removes a node from the list of in-memory children.
// This does not affect the inodes.
func (n *node) removeChild(target *node) {
	for i, child := range n.children {
		if child == target {
			n.children = append(n.children[:i], n.children[i+1:]...)
			return
		}
	}
}

// dereference causes the node to copy all its inode key/value references to heap memory.
// This is required when the mmap is reallocated so inodes are not pointing to stale data.
func (n *node) dereference() {
	if n.key != nil {
		key := make([]byte, len(n.key))
		copy(key, n.key)
		n.key = key
		_assert(n.pgid == 0 || len(n.key) > 0, "dereference: zero-length node key on existing node")
	}

	for i := range n.inodes {
		inode := &n.inodes[i]

		key := make([]byte, len(inode.key))
		copy(key, inode.key)
		inode.key = key
		_assert(len(inode.key) > 0, "dereference: zero-length inode key")

		value := make([]byte, len(inode.value))
		copy(value, inode.value)
		inode.value = value
	}

	// Recursively dereference children.
	for _, child := range n.children {
		child.dereference()
	}

	// Update statistics.
	n.bucket.tx.stats.NodeDeref++
}

// free adds the node's underlying page to the freelist.
func (n *node) free() {
	if n.pgid != 0 {
		n.bucket.tx.db.freelist.free(n.bucket.tx.meta.txid, n.bucket.tx.page(n.pgid))
		n.pgid = 0
	}
}

// dump writes the contents of the node to STDERR for debugging purposes.
/*
func (n *node) dump() {
	// Write node header.
	var typ = "branch"
	if n.isLeaf {
		typ = "leaf"
	}
	warnf("[NODE %d {type=%s count=%d}]", n.pgid, typ, len(n.inodes))

	// Write out abbreviated version of each item.
	for _, item := range n.inodes {
		if n.isLeaf {
			if item.flags&bucketLeafFlag != 0 {
				bucket := (*bucket)(unsafe.Pointer(&item.value[0]))
				warnf("+L %08x -> (bucket root=%d)", trunc(item.key, 4), bucket.root)
			} else {
				warnf("+L %08x -> %08x", trunc(item.key, 4), trunc(item.value, 4))
			}
		} else {
			warnf("+B %08x -> pgid=%d", trunc(item.key, 4), item.pgid)
		}
	}
	warn("")
}
*/

type nodes []*node

func (s nodes) Len() int      { return len(s) }
func (s nodes) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
func (s nodes) Less(i, j int) bool {
	return bytes.Compare(s[i].inodes[0].key, s[j].inodes[0].key) == -1
}

// inode represents an internal node inside of a node.
// It can be used to point to elements in a page or point
// to an element which hasn't been added to a page yet.
type inode struct {
	flags uint32
	pgid  pgid // 节点是分支节点时，指向下一个节点的页id
	key   []byte
	value []byte // 只有是是叶子节点时才有值吧
}

type inodes []inode
